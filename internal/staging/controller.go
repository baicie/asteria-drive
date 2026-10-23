package staging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// State is the persisted release-controller state. It is intentionally small
// and can be reconstructed from the host snapshot plus the state file.
type State string

const (
	StateIdle              State = "IDLE"
	StatePrepared          State = "PREPARED"
	StateActivating        State = "ACTIVATING"
	StateVerifying         State = "VERIFYING"
	StateActive            State = "ACTIVE"
	StateRollingBack       State = "ROLLING_BACK"
	StateRollbackVerifying State = "ROLLBACK_VERIFYING"
	StateFailed            State = "FAILED"
	StateRecoveryRequired  State = "RECOVERY_REQUIRED"
)

const RecordFormat = "asteria-drive-staging-state/v1"

var (
	ErrLocked            = errors.New("staging controller lock is held")
	ErrUnknownVersion    = errors.New("version is not in the approved allowlist")
	ErrRecoveryRequired  = errors.New("staging requires manual recovery")
	ErrInProgress        = errors.New("another staging operation is in progress")
	ErrInvalidRequest    = errors.New("invalid staging controller request")
	ErrInvalidTransition = errors.New("invalid staging state transition")
)

var transitionTable = map[State]map[State]bool{
	StateIdle:              {StatePrepared: true, StateRollingBack: true, StateFailed: true, StateRecoveryRequired: true},
	StatePrepared:          {StateActivating: true, StateFailed: true, StateRecoveryRequired: true},
	StateActivating:        {StateVerifying: true, StateFailed: true, StateRollingBack: true, StateRecoveryRequired: true},
	StateVerifying:         {StateActive: true, StateFailed: true, StateRollingBack: true, StateRecoveryRequired: true},
	StateActive:            {StatePrepared: true, StateRollingBack: true, StateFailed: true, StateRecoveryRequired: true},
	StateRollingBack:       {StateRollbackVerifying: true, StateFailed: true, StateRecoveryRequired: true},
	StateRollbackVerifying: {StateActive: true, StateRecoveryRequired: true},
	StateFailed:            {StatePrepared: true, StateRecoveryRequired: true},
	StateRecoveryRequired:  {},
}

func TransitionAllowed(from, to State) bool {
	next, ok := transitionTable[from]
	return ok && next[to]
}

// Record is persisted on the host across workflow runs. It never contains a
// secret; only identifiers, versions, digests and sanitized error strings.
type Record struct {
	Format                 string `json:"format"`
	Operation              string `json:"operation"`
	RunID                  string `json:"run_id"`
	Attempt                string `json:"attempt"`
	TargetVersion          string `json:"target_version"`
	ExpectedCurrentVersion string `json:"expected_current_version"`
	PreviousVersion        string `json:"previous_version"`
	ActiveVersion          string `json:"active_version"`
	State                  State  `json:"state"`
	ActiveComposeSHA256    string `json:"active_compose_sha256"`
	ActiveImage            string `json:"active_image"`
	SchemaVersion          int    `json:"schema_version"`
	EvidenceDigest         string `json:"evidence_digest"`
	LastError              string `json:"last_error"`
	UpdatedAt              string `json:"updated_at"`
}

// ActiveState is the real host state read back from Docker/DB, not a cached
// workflow value.
type ActiveState struct {
	Version       string
	ComposeSHA256 string
	Image         string
	SchemaVersion int
	Healthy       bool
}

// Evidence is the sanitized verification result.
type Evidence struct {
	Version       string
	ComposeSHA256 string
	Image         string
	SchemaVersion int
	Healthy       bool
	Digest        string
}

type Host interface {
	ReadActive(ctx context.Context) (ActiveState, error)
	Activate(ctx context.Context, bundle Bundle) (ActiveState, error)
	Verify(ctx context.Context, bundle Bundle) (Evidence, error)
	Rollback(ctx context.Context, bundle Bundle) (ActiveState, error)
	VerifyRollback(ctx context.Context, bundle Bundle) (Evidence, error)
}

type StateStore interface {
	Load(ctx context.Context) (Record, bool, error)
	Save(ctx context.Context, record Record) error
}

type Locker interface {
	Acquire(ctx context.Context, key string) (func(), error)
}

type Controller struct {
	Bundles BundleSet
	Host    Host
	Store   StateStore
	Locker  Locker
	Now     func() time.Time
}

type Request struct {
	RunID                  string
	Attempt                string
	TargetVersion          string
	ExpectedCurrentVersion string
}

var numericIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)
var attemptPattern = regexp.MustCompile(`^[0-9]{1,6}$`)

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Controller) validate() error {
	if c.Host == nil || c.Store == nil || c.Locker == nil || len(c.Bundles) == 0 {
		return fmt.Errorf("staging controller dependencies are incomplete")
	}
	return nil
}

func (c *Controller) validateRequest(request Request) (Bundle, error) {
	if !numericIDPattern.MatchString(request.RunID) || !attemptPattern.MatchString(request.Attempt) {
		return Bundle{}, fmt.Errorf("%w: run and attempt must be numeric", ErrInvalidRequest)
	}
	bundle, ok := c.Bundles[request.TargetVersion]
	if !ok {
		return Bundle{}, fmt.Errorf("%w: %s", ErrUnknownVersion, request.TargetVersion)
	}
	if request.ExpectedCurrentVersion != "" {
		if _, ok := c.Bundles[request.ExpectedCurrentVersion]; !ok {
			return Bundle{}, fmt.Errorf("%w: expected current %s", ErrUnknownVersion, request.ExpectedCurrentVersion)
		}
	}
	return bundle, nil
}

func (c *Controller) acquire(ctx context.Context) (func(), error) {
	unlock, err := c.Locker.Acquire(ctx, "asteria-drive-staging")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	return unlock, nil
}

func (c *Controller) load(ctx context.Context) (Record, error) {
	record, exists, err := c.Store.Load(ctx)
	if err != nil {
		return Record{}, err
	}
	if !exists {
		return Record{Format: RecordFormat, State: StateIdle}, nil
	}
	if record.Format != RecordFormat {
		return Record{}, fmt.Errorf("invalid staging state format %q", record.Format)
	}
	return record, nil
}

func (c *Controller) save(ctx context.Context, record *Record, next State) error {
	if record.State == next {
		return nil
	}
	if !TransitionAllowed(record.State, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, record.State, next)
	}
	record.State = next
	record.UpdatedAt = c.now().Format(time.RFC3339)
	if err := c.Store.Save(ctx, *record); err != nil {
		return fmt.Errorf("persist staging state: %w", err)
	}
	return nil
}

func (c *Controller) Deploy(ctx context.Context, request Request) (Record, error) {
	if err := c.validate(); err != nil {
		return Record{}, err
	}
	bundle, err := c.validateRequest(request)
	if err != nil {
		return Record{}, err
	}
	unlock, err := c.acquire(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()

	record, err := c.load(ctx)
	if err != nil {
		return Record{}, err
	}
	if existing, done, err := replay(record, "deploy", request); done || err != nil {
		return existing, err
	}
	if record.State == StateRecoveryRequired {
		return record, ErrRecoveryRequired
	}
	if record.RunID == request.RunID && record.Attempt == request.Attempt &&
		(record.Operation != "deploy" || record.TargetVersion != request.TargetVersion) {
		return record, fmt.Errorf("%w: run identity was already used for a different operation", ErrInvalidRequest)
	}
	if inProgress(record) {
		return record, ErrInProgress
	}
	active, err := c.Host.ReadActive(ctx)
	if err != nil {
		return c.markFailed(ctx, record, "read active state: "+err.Error(), err)
	}
	if request.ExpectedCurrentVersion != "" && active.Version != request.ExpectedCurrentVersion {
		return c.markFailed(ctx, record, "expected current version "+request.ExpectedCurrentVersion+" but found "+active.Version, fmt.Errorf("active version mismatch"))
	}
	if active.Version == bundle.Version {
		if err := bundle.ValidateActive(active); err != nil {
			return c.markFailed(ctx, record, "active target is unhealthy: "+err.Error(), err)
		}
		return c.markActive(ctx, record, bundle, "deploy", request)
	}

	previous, previousOK := c.Bundles[active.Version]
	record.Operation = "deploy"
	record.RunID = request.RunID
	record.Attempt = request.Attempt
	record.TargetVersion = bundle.Version
	record.ExpectedCurrentVersion = request.ExpectedCurrentVersion
	record.PreviousVersion = active.Version
	record.LastError = ""

	if err := c.save(ctx, &record, StatePrepared); err != nil {
		return record, err
	}
	if err := c.save(ctx, &record, StateActivating); err != nil {
		return record, err
	}
	if _, err := c.Host.Activate(ctx, bundle); err != nil {
		return c.compensate(ctx, record, previous, previousOK, "activate failed: "+err.Error(), err)
	}
	if err := c.save(ctx, &record, StateVerifying); err != nil {
		return record, err
	}
	evidence, err := c.Host.Verify(ctx, bundle)
	if err != nil {
		return c.compensate(ctx, record, previous, previousOK, "verify failed: "+err.Error(), err)
	}
	if !evidence.Healthy || evidence.Version != bundle.Version ||
		evidence.ComposeSHA256 != bundle.ComposeSHA256 || evidence.Image != bundle.Image ||
		evidence.SchemaVersion != bundle.SchemaVersion {
		return c.compensate(ctx, record, previous, previousOK, "verification evidence does not match target bundle", fmt.Errorf("verification mismatch"))
	}
	record.EvidenceDigest = evidence.Digest
	if err := c.save(ctx, &record, StateActive); err != nil {
		return record, err
	}
	record.ActiveVersion = bundle.Version
	record.ActiveComposeSHA256 = bundle.ComposeSHA256
	record.ActiveImage = bundle.Image
	record.SchemaVersion = bundle.SchemaVersion
	if err := c.Store.Save(ctx, record); err != nil {
		return record, fmt.Errorf("persist active state: %w", err)
	}
	return record, nil
}

func (c *Controller) Rollback(ctx context.Context, request Request) (Record, error) {
	if err := c.validate(); err != nil {
		return Record{}, err
	}
	bundle, err := c.validateRequest(request)
	if err != nil {
		return Record{}, err
	}
	unlock, err := c.acquire(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()

	record, err := c.load(ctx)
	if err != nil {
		return Record{}, err
	}
	if existing, done, err := replay(record, "rollback", request); done || err != nil {
		return existing, err
	}
	if record.State == StateRecoveryRequired {
		return record, ErrRecoveryRequired
	}
	if record.RunID == request.RunID && record.Attempt == request.Attempt &&
		(record.Operation != "rollback" || record.TargetVersion != request.TargetVersion) {
		return record, fmt.Errorf("%w: run identity was already used for a different operation", ErrInvalidRequest)
	}
	if inProgress(record) {
		return record, ErrInProgress
	}
	active, err := c.Host.ReadActive(ctx)
	if err != nil {
		return c.markFailed(ctx, record, "read active state: "+err.Error(), err)
	}
	if request.ExpectedCurrentVersion != "" && active.Version != request.ExpectedCurrentVersion {
		return c.markFailed(ctx, record, "expected current version "+request.ExpectedCurrentVersion+" but found "+active.Version, fmt.Errorf("active version mismatch"))
	}
	if active.Version == bundle.Version {
		if err := bundle.ValidateActive(active); err != nil {
			return c.markFailed(ctx, record, "active target is unhealthy: "+err.Error(), err)
		}
		return c.markActive(ctx, record, bundle, "rollback", request)
	}
	_, previousOK := c.Bundles[active.Version]
	if !previousOK {
		return c.markRecovery(ctx, record, "unknown active version "+active.Version, fmt.Errorf("unknown active version"))
	}
	record.Operation = "rollback"
	record.RunID = request.RunID
	record.Attempt = request.Attempt
	record.TargetVersion = bundle.Version
	record.ExpectedCurrentVersion = request.ExpectedCurrentVersion
	record.PreviousVersion = active.Version
	record.LastError = ""
	if err := c.save(ctx, &record, StateRollingBack); err != nil {
		return record, err
	}
	if _, err := c.Host.Rollback(ctx, bundle); err != nil {
		return c.markRecovery(ctx, record, "rollback execution failed: "+err.Error(), err)
	}
	if err := c.save(ctx, &record, StateRollbackVerifying); err != nil {
		return record, err
	}
	evidence, err := c.Host.VerifyRollback(ctx, bundle)
	if err != nil || !evidence.Healthy || evidence.Version != bundle.Version ||
		evidence.ComposeSHA256 != bundle.ComposeSHA256 || evidence.Image != bundle.Image {
		message := "rollback verification failed"
		if err != nil {
			message += ": " + err.Error()
		}
		return c.markRecovery(ctx, record, message, fmt.Errorf("rollback verification failed"))
	}
	record.EvidenceDigest = evidence.Digest
	record.ActiveVersion = bundle.Version
	record.ActiveComposeSHA256 = bundle.ComposeSHA256
	record.ActiveImage = bundle.Image
	record.SchemaVersion = bundle.SchemaVersion
	if err := c.save(ctx, &record, StateActive); err != nil {
		return record, err
	}
	if err := c.Store.Save(ctx, record); err != nil {
		return record, fmt.Errorf("persist active state: %w", err)
	}
	return record, nil
}

// Monitor validates that the real active version is in the approved allowlist
// and matches its bundle. Unknown versions are rejected, never silently
// accepted by hash alone.
func (c *Controller) Monitor(ctx context.Context) (Evidence, error) {
	if err := c.validate(); err != nil {
		return Evidence{}, err
	}
	active, err := c.Host.ReadActive(ctx)
	if err != nil {
		return Evidence{}, fmt.Errorf("read active state: %w", err)
	}
	bundle, ok := c.Bundles[active.Version]
	if !ok {
		return Evidence{}, fmt.Errorf("%w: %s", ErrUnknownVersion, active.Version)
	}
	if err := bundle.ValidateActive(active); err != nil {
		return Evidence{}, err
	}
	return Evidence{
		Version: active.Version, ComposeSHA256: active.ComposeSHA256, Image: active.Image,
		SchemaVersion: active.SchemaVersion, Healthy: true,
	}, nil
}

// Resume reconstructs from the persisted state and the real host snapshot after
// a runner/host interruption. It never starts a new switch automatically.
func (c *Controller) Resume(ctx context.Context, runID, attempt string) (Record, error) {
	if err := c.validate(); err != nil {
		return Record{}, err
	}
	unlock, err := c.acquire(ctx)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record, err := c.load(ctx)
	if err != nil {
		return Record{}, err
	}
	if record.RunID != runID || record.Attempt != attempt {
		return record, fmt.Errorf("resume request does not match persisted run identity")
	}
	if record.State == StateActive {
		return record, nil
	}
	if !inProgress(record) && record.State != StateFailed {
		return record, nil
	}
	active, err := c.Host.ReadActive(ctx)
	if err != nil {
		record.State = StateRecoveryRequired
		record.LastError = "resume could not read host state: " + err.Error()
		record.UpdatedAt = c.now().Format(time.RFC3339)
		_ = c.Store.Save(ctx, record)
		return record, ErrRecoveryRequired
	}
	target, targetOK := c.Bundles[record.TargetVersion]
	if targetOK && active.Version == target.Version && target.ValidateActive(active) == nil {
		record.State = StateActive
		record.ActiveVersion = active.Version
		record.ActiveComposeSHA256 = active.ComposeSHA256
		record.ActiveImage = active.Image
		record.SchemaVersion = active.SchemaVersion
		record.UpdatedAt = c.now().Format(time.RFC3339)
		if err := c.Store.Save(ctx, record); err != nil {
			return record, err
		}
		return record, nil
	}
	previous, previousOK := c.Bundles[record.PreviousVersion]
	if previousOK && active.Version == previous.Version && previous.ValidateActive(active) == nil {
		record.State = StateActive
		record.ActiveVersion = active.Version
		record.ActiveComposeSHA256 = active.ComposeSHA256
		record.ActiveImage = active.Image
		record.SchemaVersion = active.SchemaVersion
		record.UpdatedAt = c.now().Format(time.RFC3339)
		if err := c.Store.Save(ctx, record); err != nil {
			return record, err
		}
		return record, nil
	}
	record.State = StateRecoveryRequired
	record.LastError = "resume found an active version that does not match target or previous"
	record.UpdatedAt = c.now().Format(time.RFC3339)
	_ = c.Store.Save(ctx, record)
	return record, ErrRecoveryRequired
}

func replay(record Record, operation string, request Request) (Record, bool, error) {
	if record.RunID != request.RunID || record.Attempt != request.Attempt ||
		record.Operation != operation || record.TargetVersion != request.TargetVersion {
		return record, false, nil
	}
	switch record.State {
	case StateActive:
		return record, true, nil
	case StateRecoveryRequired:
		return record, true, ErrRecoveryRequired
	case StatePrepared, StateActivating, StateVerifying, StateRollingBack, StateRollbackVerifying:
		return record, true, ErrInProgress
	default:
		return record, false, nil
	}
}

func inProgress(record Record) bool {
	switch record.State {
	case StatePrepared, StateActivating, StateVerifying, StateRollingBack, StateRollbackVerifying:
		return true
	default:
		return false
	}
}

func (c *Controller) markActive(ctx context.Context, record Record, bundle Bundle, operation string, request Request) (Record, error) {
	record.Operation = operation
	record.RunID = request.RunID
	record.Attempt = request.Attempt
	record.TargetVersion = bundle.Version
	record.ExpectedCurrentVersion = request.ExpectedCurrentVersion
	record.ActiveVersion = bundle.Version
	record.ActiveComposeSHA256 = bundle.ComposeSHA256
	record.ActiveImage = bundle.Image
	record.SchemaVersion = bundle.SchemaVersion
	record.LastError = ""
	record.State = StateActive
	record.UpdatedAt = c.now().Format(time.RFC3339)
	if err := c.Store.Save(ctx, record); err != nil {
		return record, fmt.Errorf("persist active state: %w", err)
	}
	return record, nil
}

func (c *Controller) markFailed(ctx context.Context, record Record, message string, cause error) (Record, error) {
	record.LastError = message
	if err := c.save(ctx, &record, StateFailed); err != nil {
		return record, err
	}
	return record, cause
}

func (c *Controller) markRecovery(ctx context.Context, record Record, message string, cause error) (Record, error) {
	record.LastError = message
	if record.State == StateRecoveryRequired {
		_ = c.Store.Save(ctx, record)
		return record, ErrRecoveryRequired
	}
	if err := c.save(ctx, &record, StateRecoveryRequired); err != nil {
		return record, err
	}
	return record, ErrRecoveryRequired
}

func (c *Controller) compensate(ctx context.Context, record Record, previous Bundle, previousOK bool, message string, cause error) (Record, error) {
	record.LastError = message
	if !previousOK {
		return c.markRecovery(ctx, record, message+"; no approved previous bundle", ErrRecoveryRequired)
	}
	if err := c.save(ctx, &record, StateRollingBack); err != nil {
		return record, err
	}
	if _, err := c.Host.Rollback(ctx, previous); err != nil {
		return c.markRecovery(ctx, record, message+"; rollback failed: "+err.Error(), ErrRecoveryRequired)
	}
	if err := c.save(ctx, &record, StateRollbackVerifying); err != nil {
		return record, err
	}
	evidence, err := c.Host.VerifyRollback(ctx, previous)
	if err != nil || !evidence.Healthy || evidence.Version != previous.Version ||
		evidence.ComposeSHA256 != previous.ComposeSHA256 || evidence.Image != previous.Image {
		return c.markRecovery(ctx, record, message+"; rollback verification failed", ErrRecoveryRequired)
	}
	record.State = StateActive
	record.ActiveVersion = previous.Version
	record.ActiveComposeSHA256 = previous.ComposeSHA256
	record.ActiveImage = previous.Image
	record.SchemaVersion = previous.SchemaVersion
	record.EvidenceDigest = evidence.Digest
	record.UpdatedAt = c.now().Format(time.RFC3339)
	if err := c.Store.Save(ctx, record); err != nil {
		return record, err
	}
	return record, cause
}

// FileStore persists state next to the active deployment root. Writes are
// atomic so a runner/host interruption cannot leave a truncated JSON record.
type FileStore struct {
	Path string
}

func (store FileStore) Load(ctx context.Context) (Record, bool, error) {
	contents, err := os.ReadFile(store.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("read staging state: %w", err)
	}
	var record Record
	if err := json.Unmarshal(contents, &record); err != nil {
		return Record{}, false, fmt.Errorf("decode staging state: %w", err)
	}
	return record, true, nil
}

func (store FileStore) Save(ctx context.Context, record Record) error {
	if store.Path == "" {
		return fmt.Errorf("staging state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode staging state: %w", err)
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(store.Path), ".staging-state-*")
	if err != nil {
		return fmt.Errorf("create staging state temporary file: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set staging state permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write staging state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staging state: %w", err)
	}
	if err := os.Rename(name, store.Path); err != nil {
		return fmt.Errorf("publish staging state: %w", err)
	}
	return nil
}
