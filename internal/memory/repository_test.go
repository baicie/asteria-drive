package memory

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestRepositoryNestedRecycleRootsRemainIndependent(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
	identity := drive.Identity{TenantID: "tenant-a", PrincipalID: "principal-a"}

	tenant, err := repository.EnsureTenant(ctx, drive.TenantSeed{
		TenantID: identity.TenantID, DisplayName: "Tenant A", RootNodeID: "root", Now: now,
	})
	assertNoError(t, err)
	parent, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "parent", ParentID: tenant.RootNodeID,
		DisplayName: "Parent", NormalizedName: "parent", Now: now.Add(time.Minute),
	})
	assertNoError(t, err)
	child, err := repository.CreateDirectory(ctx, drive.CreateDirectoryCommand{
		Identity: identity, ID: "child", ParentID: parent.ID,
		DisplayName: "Child", NormalizedName: "child", Now: now.Add(2 * time.Minute),
	})
	assertNoError(t, err)

	assertNoError(t, repository.Recycle(ctx, identity, child.ID, child.Revision, now.Add(3*time.Minute)))
	assertNoError(t, repository.Recycle(ctx, identity, parent.ID, parent.Revision, now.Add(4*time.Minute)))

	entries := listRecycle(t, repository, identity)
	assertRecycleRoots(t, entries, parent.ID, child.ID)

	restoredParent, err := repository.Restore(ctx, identity, parent.ID, parent.Revision+1, now.Add(5*time.Minute))
	assertNoError(t, err)
	if restoredParent.Status != drive.NodeActive || restoredParent.ParentID != tenant.RootNodeID || restoredParent.TrashedRootID != "" {
		t.Fatalf("unexpected restored parent: %+v", restoredParent)
	}
	trashedChild, err := repository.Node(ctx, identity, child.ID, true)
	assertNoError(t, err)
	if trashedChild.Status != drive.NodeTrashed || trashedChild.TrashedRootID != child.ID || trashedChild.OriginalParentID != parent.ID {
		t.Fatalf("child did not remain an independent recycle root: %+v", trashedChild)
	}
	if _, err := repository.Node(ctx, identity, child.ID, false); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("trashed child was visible after restoring parent: code=%s err=%v", drive.CodeOf(err), err)
	}
	entries = listRecycle(t, repository, identity)
	assertRecycleRoots(t, entries, child.ID)

	restoredChild, err := repository.Restore(ctx, identity, child.ID, child.Revision+1, now.Add(6*time.Minute))
	assertNoError(t, err)
	if restoredChild.Status != drive.NodeActive || restoredChild.ParentID != parent.ID || restoredChild.TrashedRootID != "" || restoredChild.OriginalParentID != "" {
		t.Fatalf("unexpected restored child: %+v", restoredChild)
	}
	children, more, err := repository.ListChildren(ctx, identity, parent.ID, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more || len(children) != 1 || children[0].ID != child.ID {
		t.Fatalf("restored parent-child relationship is incorrect: more=%v children=%+v", more, children)
	}
	if entries := listRecycle(t, repository, identity); len(entries) != 0 {
		t.Fatalf("recycle bin was not empty after both restores: %+v", entries)
	}
}

func listRecycle(t *testing.T, repository *Repository, identity drive.Identity) []drive.RecycleEntry {
	t.Helper()
	entries, more, err := repository.ListRecycle(context.Background(), identity, drive.CursorPosition{}, 10)
	assertNoError(t, err)
	if more {
		t.Fatal("unexpected recycle pagination")
	}
	return entries
}

func assertRecycleRoots(t *testing.T, entries []drive.RecycleEntry, expected ...string) {
	t.Helper()
	if len(entries) != len(expected) {
		t.Fatalf("recycle roots=%d, want %d: %+v", len(entries), len(expected), entries)
	}
	roots := make(map[string]drive.Node, len(entries))
	for _, entry := range entries {
		roots[entry.Node.ID] = entry.Node
	}
	for _, id := range expected {
		node, ok := roots[id]
		if !ok || node.Status != drive.NodeTrashed || node.TrashedRootID != id {
			t.Fatalf("missing independent recycle root %q: %+v", id, entries)
		}
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
