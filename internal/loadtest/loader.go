package loadtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
)

type LoaderOptions struct {
	DatabaseURL string
	TenantID    string
	TenantName  string
	RootID      string
	Count       int64
	Fanout      int64
	BatchSize   int
	Seed        string
	Now         time.Time
	Analyze     bool
}

type LoadReport struct {
	TenantID       string        `json:"tenant_id"`
	RootID         string        `json:"root_id"`
	Seed           string        `json:"seed"`
	RequestedNodes int64         `json:"requested_nodes"`
	InsertedNodes  int64         `json:"inserted_nodes"`
	ExistingNodes  int64         `json:"existing_nodes"`
	Fanout         int64         `json:"fanout"`
	BatchSize      int           `json:"batch_size"`
	Duration       time.Duration `json:"duration_ns"`
	RowsPerSecond  float64       `json:"rows_per_second"`
}

func (o LoaderOptions) Validate() error {
	if strings.TrimSpace(o.DatabaseURL) == "" {
		return fmt.Errorf("database url is required")
	}
	if !drive.ValidID(o.TenantID) {
		return fmt.Errorf("tenant id must be a UUID")
	}
	if o.RootID != "" && !drive.ValidID(o.RootID) {
		return fmt.Errorf("root id must be a UUID")
	}
	if o.Count < 0 {
		return fmt.Errorf("count must be non-negative")
	}
	if o.Fanout < 1 || o.Fanout > 10000 {
		return fmt.Errorf("fanout must be between 1 and 10000")
	}
	if o.BatchSize < 1 || o.BatchSize > 100000 {
		return fmt.Errorf("batch size must be between 1 and 100000")
	}
	if strings.TrimSpace(o.Seed) == "" {
		return fmt.Errorf("seed is required")
	}
	return nil
}

// Load creates the tenant/root if necessary, then uses COPY into a temporary
// table followed by batched INSERTs. This keeps client memory bounded while
// retaining the file_node foreign-key and uniqueness checks in PostgreSQL.
func Load(ctx context.Context, options LoaderOptions) (LoadReport, error) {
	if options.BatchSize == 0 {
		options.BatchSize = 10000
	}
	if options.Fanout == 0 {
		options.Fanout = 32
	}
	if options.Seed == "" {
		options.Seed = "asteria-loadtest-v1"
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if err := options.Validate(); err != nil {
		return LoadReport{}, err
	}
	started := time.Now()
	conn, err := pgx.Connect(ctx, options.DatabaseURL)
	if err != nil {
		return LoadReport{}, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer conn.Close(context.Background())

	rootID, err := ensureTenantAndRoot(ctx, conn, options)
	if err != nil {
		return LoadReport{}, err
	}
	if options.Count == 0 {
		return LoadReport{TenantID: options.TenantID, RootID: rootID, Seed: options.Seed, Fanout: options.Fanout,
			BatchSize: options.BatchSize, Duration: time.Since(started)}, nil
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return LoadReport{}, fmt.Errorf("begin load transaction: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS file_node_parent_fk DEFERRED`); err != nil {
		return LoadReport{}, fmt.Errorf("defer parent constraint: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE asteria_load_nodes (
		idx bigint NOT NULL, id uuid NOT NULL, parent_id uuid NOT NULL,
		display_name text NOT NULL, normalized_name text NOT NULL
	) ON COMMIT DROP`); err != nil {
		return LoadReport{}, fmt.Errorf("create load staging table: %w", err)
	}

	report := LoadReport{TenantID: options.TenantID, RootID: rootID, Seed: options.Seed,
		RequestedNodes: options.Count, Fanout: options.Fanout, BatchSize: options.BatchSize}
	buffer := make([]TreeNode, 0, options.BatchSize)
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `TRUNCATE asteria_load_nodes`); err != nil {
			return fmt.Errorf("truncate load staging table: %w", err)
		}
		rows := make([][]any, len(buffer))
		for i, node := range buffer {
			rows[i] = []any{node.Index, node.ID, node.ParentID, node.DisplayName, node.NormalizedName}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"asteria_load_nodes"}, []string{"idx", "id", "parent_id", "display_name", "normalized_name"}, pgx.CopyFromRows(rows)); err != nil {
			return fmt.Errorf("copy load batch: %w", err)
		}
		result, err := tx.Exec(ctx, `INSERT INTO file_node
			(id, tenant_id, parent_id, kind, display_name, normalized_name, size_bytes,
			 mime_type, status, revision, created_at, updated_at)
			SELECT id, $1, parent_id, 'directory', display_name, normalized_name, 0,
			 '', 'active', 1, $2, $2
			FROM asteria_load_nodes ORDER BY idx
			ON CONFLICT DO NOTHING`, options.TenantID, options.Now)
		if err != nil {
			return fmt.Errorf("insert load batch: %w", err)
		}
		report.InsertedNodes += result.RowsAffected()
		var matched int64
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM asteria_load_nodes staged
			JOIN file_node node ON node.id = staged.id
			WHERE node.tenant_id = $1
			  AND node.parent_id = staged.parent_id
			  AND node.kind = 'directory'
			  AND node.display_name = staged.display_name
			  AND node.normalized_name = staged.normalized_name
			  AND node.status = 'active'`, options.TenantID).Scan(&matched); err != nil {
			return fmt.Errorf("verify load batch: %w", err)
		}
		if matched != int64(len(buffer)) {
			return fmt.Errorf("load batch contains conflicting existing nodes: matched %d of %d deterministic rows", matched, len(buffer))
		}
		report.ExistingNodes += int64(len(buffer)) - result.RowsAffected()
		buffer = buffer[:0]
		return nil
	}
	if err := Generate(TreeOptions{TenantID: options.TenantID, RootID: rootID, Count: options.Count,
		Fanout: options.Fanout, Seed: options.Seed}, func(node TreeNode) error {
		buffer = append(buffer, node)
		if len(buffer) >= options.BatchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return LoadReport{}, err
	}
	if err := flush(); err != nil {
		return LoadReport{}, err
	}
	if options.Analyze {
		if _, err := tx.Exec(ctx, `ANALYZE file_node`); err != nil {
			return LoadReport{}, fmt.Errorf("analyze file_node: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return LoadReport{}, fmt.Errorf("commit load transaction: %w", err)
	}
	report.Duration = time.Since(started)
	if report.Duration > 0 {
		report.RowsPerSecond = float64(report.InsertedNodes) / report.Duration.Seconds()
	}
	return report, nil
}

func ensureTenantAndRoot(ctx context.Context, conn *pgx.Conn, options LoaderOptions) (string, error) {
	var rootID string
	err := conn.QueryRow(ctx, `SELECT COALESCE(root_node_id::text, '') FROM tenant WHERE id=$1`, options.TenantID).Scan(&rootID)
	if err != nil && err != pgx.ErrNoRows {
		return "", fmt.Errorf("read tenant: %w", err)
	}
	if err == pgx.ErrNoRows {
		if options.RootID == "" {
			options.RootID = DeterministicUUID(options.TenantID+":"+options.Seed+":root", 0)
		}
		tx, txErr := conn.Begin(ctx)
		if txErr != nil {
			return "", fmt.Errorf("begin tenant setup: %w", txErr)
		}
		defer tx.Rollback(context.Background())
		// The tenant/root foreign key is deferred, so the root can be created
		// in the same transaction without a partially initialized tenant.
		if _, txErr = tx.Exec(ctx, `INSERT INTO tenant(id, display_name, root_node_id, created_at) VALUES($1,$2,NULL,$3)`, options.TenantID, options.TenantName, options.Now); txErr != nil {
			return "", fmt.Errorf("create tenant: %w", txErr)
		}
		if _, txErr = tx.Exec(ctx, `INSERT INTO file_node(id,tenant_id,kind,display_name,normalized_name,size_bytes,mime_type,status,revision,created_at,updated_at) VALUES($1,$2,'directory','','',0,'','active',1,$3,$3)`, options.RootID, options.TenantID, options.Now); txErr != nil {
			return "", fmt.Errorf("create tenant root: %w", txErr)
		}
		if _, txErr = tx.Exec(ctx, `UPDATE tenant SET root_node_id=$2 WHERE id=$1`, options.TenantID, options.RootID); txErr != nil {
			return "", fmt.Errorf("link tenant root: %w", txErr)
		}
		if txErr = tx.Commit(ctx); txErr != nil {
			return "", fmt.Errorf("commit tenant setup: %w", txErr)
		}
		return options.RootID, nil
	}
	if rootID == "" {
		return "", fmt.Errorf("tenant %q has no root node", options.TenantID)
	}
	if options.RootID != "" && options.RootID != rootID {
		return "", fmt.Errorf("tenant root id is %s, requested %s", rootID, options.RootID)
	}
	return rootID, nil
}
