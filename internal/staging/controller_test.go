package staging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testBundles(t *testing.T) BundleSet {
	t.Helper()
	bundles, err := LoadBundles("../../infra/docker/staging/releases")
	if err != nil {
		t.Fatalf("load approved bundles: %v", err)
	}
	return bundles
}

type fakeHost struct {
	active              ActiveState
	readErr             error
	bundles             BundleSet
	activateErr         error
	verifyErr           error
	verifyEvidence      *Evidence
	rollbackErr         error
	rollbackVerify      *Evidence
	activeAfterRollback *ActiveState
	activateCalls       []string
	rollbackCalls       []string
	deletedVolumes      bool
}

func (host *fakeHost) ReadActive(context.Context) (ActiveState, error) {
	if host.readErr != nil {
		return ActiveState{}, host.readErr
	}
	active := host.active
	if active.ComposeSHA256 == "" {
		if bundle, ok := host.bundles[active.Version]; ok {
			active.ComposeSHA256 = bundle.ComposeSHA256
			active.Image = bundle.Image
			active.SchemaVersion = bundle.SchemaVersion
		}
	}
	return active, nil
}

func (host *fakeHost) Activate(_ context.Context, bundle Bundle) (ActiveState, error) {
	host.activateCalls = append(host.activateCalls, bundle.Version)
	if host.activateErr != nil {
		return ActiveState{}, host.activateErr
	}
	host.active = ActiveState{Version: bundle.Version, ComposeSHA256: bundle.ComposeSHA256, Image: bundle.Image, SchemaVersion: bundle.SchemaVersion, Healthy: true}
	return host.active, nil
}

func (host *fakeHost) Verify(_ context.Context, bundle Bundle) (Evidence, error) {
	if host.verifyErr != nil {
		return Evidence{}, host.verifyErr
	}
	if host.verifyEvidence != nil {
		return *host.verifyEvidence, nil
	}
	return Evidence{Version: bundle.Version, ComposeSHA256: bundle.ComposeSHA256, Image: bundle.Image, SchemaVersion: bundle.SchemaVersion, Healthy: true, Digest: "fake-evidence"}, nil
}

func (host *fakeHost) Rollback(_ context.Context, bundle Bundle) (ActiveState, error) {
	host.rollbackCalls = append(host.rollbackCalls, bundle.Version)
	if host.rollbackErr != nil {
		return ActiveState{}, host.rollbackErr
	}
	if host.activeAfterRollback != nil {
		host.active = *host.activeAfterRollback
	} else {
		host.active = ActiveState{Version: bundle.Version, ComposeSHA256: bundle.ComposeSHA256, Image: bundle.Image, SchemaVersion: bundle.SchemaVersion, Healthy: true}
	}
	return host.active, nil
}

func (host *fakeHost) VerifyRollback(_ context.Context, bundle Bundle) (Evidence, error) {
	if host.rollbackVerify != nil {
		return *host.rollbackVerify, nil
	}
	return Evidence{Version: bundle.Version, ComposeSHA256: bundle.ComposeSHA256, Image: bundle.Image, SchemaVersion: bundle.SchemaVersion, Healthy: true, Digest: "fake-rollback-evidence"}, nil
}

type fakeStore struct {
	record    Record
	exists    bool
	saveCalls int
	failAt    int
}

func (store *fakeStore) Load(context.Context) (Record, bool, error) {
	return store.record, store.exists, nil
}

func (store *fakeStore) Save(_ context.Context, record Record) error {
	store.saveCalls++
	if store.failAt > 0 && store.saveCalls == store.failAt {
		return errors.New("injected state write failure")
	}
	store.record = record
	store.exists = true
	return nil
}

type fakeLocker struct {
	locked bool
	calls  int
}

func (locker *fakeLocker) Acquire(context.Context, string) (func(), error) {
	locker.calls++
	if locker.locked {
		return nil, errors.New("held")
	}
	locker.locked = true
	return func() { locker.locked = false }, nil
}

func newTestController(t *testing.T, host Host) (*Controller, BundleSet, *fakeStore, *fakeLocker) {
	t.Helper()
	bundles := testBundles(t)
	if fake, ok := host.(*fakeHost); ok {
		fake.bundles = bundles
	}
	store := &fakeStore{}
	locker := &fakeLocker{}
	controller := &Controller{Bundles: bundles, Host: host, Store: store, Locker: locker}
	return controller, bundles, store, locker
}

func request(runID, target, expected string) Request {
	return Request{RunID: runID, Attempt: "1", TargetVersion: target, ExpectedCurrentVersion: expected}
}

func TestDeployV011ToV020(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: testBundles(t)[VersionV011].ComposeSHA256, Image: testBundles(t)[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, bundles, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("1", VersionV020, VersionV011))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if record.State != StateActive || record.ActiveVersion != VersionV020 {
		t.Fatalf("unexpected record: %#v", record)
	}
	if len(host.activateCalls) != 1 || host.activateCalls[0] != VersionV020 {
		t.Fatalf("unexpected activate calls: %#v", host.activateCalls)
	}
	if host.active.ComposeSHA256 != bundles[VersionV020].ComposeSHA256 || host.active.Image != bundles[VersionV020].Image {
		t.Fatalf("host active does not match target bundle: %#v", host.active)
	}
	if host.deletedVolumes {
		t.Fatal("controller deleted a volume")
	}
}

func TestRollbackV020ToV011(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("2", VersionV011, VersionV020))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("unexpected record: %#v", record)
	}
	if len(host.rollbackCalls) != 1 || host.rollbackCalls[0] != VersionV011 {
		t.Fatalf("unexpected rollback calls: %#v", host.rollbackCalls)
	}
}

func TestDeployActivationFailureCompensatesToPrevious(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}, activateErr: errors.New("injected activation failure")}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("3", VersionV020, VersionV011))
	if err == nil {
		t.Fatal("expected activation failure")
	}
	if record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("expected compensated ACTIVE v0.1.1, got %#v", record)
	}
	if len(host.rollbackCalls) != 1 || host.rollbackCalls[0] != VersionV011 {
		t.Fatalf("unexpected rollback calls: %#v", host.rollbackCalls)
	}
}

func TestVerifyMismatchCompensatesToPrevious(t *testing.T) {
	set := testBundles(t)
	wrong := Evidence{Version: VersionV020, ComposeSHA256: "deadbeef", Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}, verifyEvidence: &wrong}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("4", VersionV020, VersionV011))
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("expected compensated ACTIVE v0.1.1, got %#v", record)
	}
}

func TestUnknownTargetVersion(t *testing.T) {
	controller, _, _, locker := newTestController(t, &fakeHost{})
	_, err := controller.Deploy(context.Background(), request("5", "v9.9.9", ""))
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("error = %v, want ErrUnknownVersion", err)
	}
	if locker.calls != 0 {
		t.Fatalf("locker called for invalid request: %d", locker.calls)
	}
}

func TestExpectedCurrentVersionMismatch(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: VersionV011, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("6", VersionV020, VersionV020))
	if err == nil {
		t.Fatal("expected active version mismatch")
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s, want RECOVERY_REQUIRED", record.State)
	}
	if len(host.activateCalls) != 0 {
		t.Fatal("activated despite expected-current mismatch")
	}
}

func TestSameRunReplayIsIdempotent(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	first, err := controller.Deploy(context.Background(), request("7", VersionV020, VersionV011))
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.Deploy(context.Background(), request("7", VersionV020, VersionV011))
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateActive || second.State != StateActive || len(host.activateCalls) != 1 {
		t.Fatalf("replay was not idempotent: first=%#v second=%#v calls=%#v", first, second, host.activateCalls)
	}
}

func TestConcurrentLockRejected(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: VersionV011, SchemaVersion: 3, Healthy: true}}
	controller, _, _, locker := newTestController(t, host)
	locker.locked = true
	_, err := controller.Deploy(context.Background(), request("8", VersionV020, VersionV011))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("error = %v, want ErrLocked", err)
	}
}

func TestStateWriteFailureFailsClosed(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: VersionV011, SchemaVersion: 3, Healthy: true}}
	controller, _, store, _ := newTestController(t, host)
	store.failAt = 1
	_, err := controller.Deploy(context.Background(), request("9", VersionV020, VersionV011))
	if err == nil {
		t.Fatal("expected state write failure")
	}
	if len(host.activateCalls) != 0 {
		t.Fatal("activated after state persistence failure")
	}
}

func TestResumeAfterActivationTargetsActive(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, store, _ := newTestController(t, host)
	store.record = Record{Format: RecordFormat, Operation: "deploy", RunID: "10", Attempt: "1", TargetVersion: VersionV020, PreviousVersion: VersionV011, State: StateActivating}
	store.exists = true
	record, err := controller.Resume(context.Background(), "10", "1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if record.State != StateActive || record.ActiveVersion != VersionV020 {
		t.Fatalf("unexpected resumed record: %#v", record)
	}
}

func TestResumeAfterActivationPreviousActive(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	store := controller.Store.(*fakeStore)
	store.record = Record{Format: RecordFormat, Operation: "deploy", RunID: "11", Attempt: "1", TargetVersion: VersionV020, PreviousVersion: VersionV011, State: StateVerifying}
	store.exists = true
	record, err := controller.Resume(context.Background(), "11", "1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("unexpected resumed record: %#v", record)
	}
}

func TestRollbackVerificationFailureRequiresRecovery(t *testing.T) {
	set := testBundles(t)
	wrong := Evidence{Version: VersionV011, ComposeSHA256: "deadbeef", Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}, rollbackVerify: &wrong}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("12", VersionV011, VersionV020))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s, want RECOVERY_REQUIRED", record.State)
	}
}

func TestSchemaMismatchRejected(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 2, Healthy: true}}
	if err := set[VersionV020].ValidateActive(host.active); err == nil {
		t.Fatal("schema mismatch was accepted")
	}
}

func TestMonitorRejectsUnknownVersion(t *testing.T) {
	controller, _, _, _ := newTestController(t, &fakeHost{active: ActiveState{Version: "v9.9.9", SchemaVersion: 3, Healthy: true}})
	if _, err := controller.Monitor(context.Background()); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("error = %v, want ErrUnknownVersion", err)
	}
}

func TestMonitorAcceptsApprovedOldVersion(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	evidence, err := controller.Monitor(context.Background())
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if evidence.Version != VersionV011 {
		t.Fatalf("version = %s, want %s", evidence.Version, VersionV011)
	}
}

func TestInvalidRunIdentityRejected(t *testing.T) {
	controller, _, _, locker := newTestController(t, &fakeHost{})
	_, err := controller.Deploy(context.Background(), Request{RunID: "not-a-run", Attempt: "1", TargetVersion: VersionV020})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if locker.calls != 0 {
		t.Fatal("locker called for invalid run")
	}
}

func TestSameRunDifferentTargetRejected(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	if _, err := controller.Deploy(context.Background(), request("13", VersionV020, VersionV011)); err != nil {
		t.Fatal(err)
	}
	_, err := controller.Deploy(context.Background(), request("13", VersionV011, VersionV020))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestMonitorRejectsComposeMismatch(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: "deadbeef", Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	if _, err := controller.Monitor(context.Background()); err == nil {
		t.Fatal("monitor accepted a mismatched Compose hash")
	}
}

func TestRollbackUnknownActiveVersionRequiresRecovery(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: "v9.9.9", SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("14", VersionV011, ""))
	if err == nil {
		t.Fatal("rollback accepted an unknown active version")
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s, want RECOVERY_REQUIRED", record.State)
	}
}

func TestVerifyImageMismatchCompensatesToPrevious(t *testing.T) {
	set := testBundles(t)
	wrong := Evidence{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: "sha256:deadbeef", SchemaVersion: 3, Healthy: true}
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}, verifyEvidence: &wrong}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("15", VersionV020, VersionV011))
	if err == nil {
		t.Fatal("expected image verification failure")
	}
	if record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("expected compensated ACTIVE v0.1.1, got %#v", record)
	}
}

func TestRollbackEvidenceMissingRequiresRecovery(t *testing.T) {
	set := testBundles(t)
	missing := Evidence{}
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}, rollbackVerify: &missing}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("16", VersionV011, VersionV020))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s, want RECOVERY_REQUIRED", record.State)
	}
}

func TestMonitorRejectsImageMismatch(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: "sha256:deadbeef", SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	if _, err := controller.Monitor(context.Background()); err == nil {
		t.Fatal("monitor accepted a mismatched image")
	}
}

func TestLoadBundlesMatchesFiles(t *testing.T) {
	set := testBundles(t)
	if len(set) != 2 {
		t.Fatalf("bundle count = %d, want 2", len(set))
	}
	for _, version := range []string{VersionV011, VersionV020} {
		bundle, ok := set.Get(version)
		if !ok {
			t.Fatalf("missing bundle %s", version)
		}
		if err := bundle.ValidateActive(ActiveState{Version: version, ComposeSHA256: bundle.ComposeSHA256, Image: bundle.Image, SchemaVersion: 3, Healthy: true}); err != nil {
			t.Fatalf("bundle %s failed self-check: %v", version, err)
		}
	}
}

func TestTransitionTableRejectsIllegalTransition(t *testing.T) {
	if TransitionAllowed(StateIdle, StateActive) {
		t.Fatal("IDLE -> ACTIVE must not be allowed")
	}
	if !TransitionAllowed(StateIdle, StatePrepared) || !TransitionAllowed(StateActivating, StateVerifying) {
		t.Fatal("approved transitions were rejected")
	}
}
func TestPreSwitchUnreadableBlocksDeploy(t *testing.T) {
	host := &fakeHost{readErr: errors.New("docker unavailable")}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("20", VersionV020, VersionV011))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired || len(host.activateCalls) != 0 {
		t.Fatalf("unexpected record/calls: %#v %#v", record, host.activateCalls)
	}
}

func TestPreSwitchUnknownActiveBlocksDeploy(t *testing.T) {
	host := &fakeHost{active: ActiveState{Version: "v9.9.9", Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("21", VersionV020, ""))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired || len(host.activateCalls) != 0 {
		t.Fatalf("unexpected record/calls: %#v %#v", record, host.activateCalls)
	}
}

func TestPreSwitchActiveComposeDriftBlocksDeploy(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: "deadbeef", Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("22", VersionV020, VersionV011))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired || len(host.activateCalls) != 0 {
		t.Fatalf("unexpected record/calls: %#v %#v", record, host.activateCalls)
	}
}

func TestPreSwitchUnhealthyActiveBlocksDeploy(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: false}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("23", VersionV020, VersionV011))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired || len(host.activateCalls) != 0 {
		t.Fatalf("unexpected record/calls: %#v %#v", record, host.activateCalls)
	}
}

func TestActiveTargetUnhealthyBlocksDeploy(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: false}}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("24", VersionV020, VersionV020))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s", record.State)
	}
}

func TestRollbackSchemaMismatchRequiresRecovery(t *testing.T) {
	set := testBundles(t)
	wrongSchema := Evidence{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 2, Healthy: true, Digest: "evidence"}
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}, rollbackVerify: &wrongSchema}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("25", VersionV011, VersionV020))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("error = %v, want ErrRecoveryRequired", err)
	}
	if record.State != StateRecoveryRequired {
		t.Fatalf("state = %s", record.State)
	}
}

func TestDeployVerifyMissingDigestCompensates(t *testing.T) {
	set := testBundles(t)
	missingDigest := Evidence{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}, verifyEvidence: &missingDigest}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("26", VersionV020, VersionV011))
	if err == nil || record.State != StateActive || record.ActiveVersion != VersionV011 {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestCompensateSchemaMismatchRequiresRecovery(t *testing.T) {
	set := testBundles(t)
	wrongTarget := Evidence{Version: VersionV020, ComposeSHA256: "deadbeef", Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true, Digest: "e"}
	wrongRollback := Evidence{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 2, Healthy: true, Digest: "e"}
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}, verifyEvidence: &wrongTarget, rollbackVerify: &wrongRollback}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Deploy(context.Background(), request("27", VersionV020, VersionV011))
	if !errors.Is(err, ErrRecoveryRequired) || record.State != StateRecoveryRequired {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestRollbackPostReadImageMismatchRequiresRecovery(t *testing.T) {
	set := testBundles(t)
	bad := ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: "sha256:deadbeef", SchemaVersion: 3, Healthy: true}
	host := &fakeHost{active: ActiveState{Version: VersionV020, ComposeSHA256: set[VersionV020].ComposeSHA256, Image: set[VersionV020].Image, SchemaVersion: 3, Healthy: true}, activeAfterRollback: &bad}
	controller, _, _, _ := newTestController(t, host)
	record, err := controller.Rollback(context.Background(), request("28", VersionV011, VersionV020))
	if !errors.Is(err, ErrRecoveryRequired) || record.State != StateRecoveryRequired {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestFinalActivePersistFailureRemainsNonActive(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, store, _ := newTestController(t, host)
	store.failAt = 4
	record, err := controller.Deploy(context.Background(), request("29", VersionV020, VersionV011))
	if err == nil {
		t.Fatal("expected final persistence failure")
	}
	if record.State != StateVerifying || store.record.State == StateActive {
		t.Fatalf("record=%#v persisted=%#v", record, store.record)
	}
}

func TestReplayRevalidatesRealHost(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, _, _ := newTestController(t, host)
	if _, err := controller.Deploy(context.Background(), request("30", VersionV020, VersionV011)); err != nil {
		t.Fatal(err)
	}
	host.active = ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}
	if _, err := controller.Deploy(context.Background(), request("30", VersionV020, VersionV011)); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("replay error = %v, want ErrRecoveryRequired", err)
	}
}

func TestResumeActiveRevalidatesRealHost(t *testing.T) {
	set := testBundles(t)
	host := &fakeHost{active: ActiveState{Version: VersionV011, ComposeSHA256: set[VersionV011].ComposeSHA256, Image: set[VersionV011].Image, SchemaVersion: 3, Healthy: true}}
	controller, _, store, _ := newTestController(t, host)
	store.record = Record{Format: RecordFormat, Operation: "deploy", RunID: "31", Attempt: "1", TargetVersion: VersionV020, ActiveVersion: VersionV020, ActiveComposeSHA256: set[VersionV020].ComposeSHA256, ActiveImage: set[VersionV020].Image, SchemaVersion: 3, State: StateActive}
	store.exists = true
	if _, err := controller.Resume(context.Background(), "31", "1"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("resume error = %v, want ErrRecoveryRequired", err)
	}
}

func TestFileStoreRoundTripAndCorruptRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := FileStore{Path: path}
	record := Record{Format: RecordFormat, RunID: "1", Attempt: "1", Operation: "deploy", State: StateActive, ActiveVersion: VersionV020}
	if err := store.Save(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load(context.Background())
	if err != nil || !ok || loaded.ActiveVersion != VersionV020 {
		t.Fatalf("loaded=%#v ok=%v err=%v", loaded, ok, err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background()); err == nil {
		t.Fatal("corrupt state was accepted")
	}
}
