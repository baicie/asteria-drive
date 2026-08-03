package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type Repository struct {
	mu       sync.RWMutex
	tenants  map[string]drive.Tenant
	nodes    map[string]drive.Node
	blobs    map[string]drive.Blob
	versions map[string]drive.FileVersion
	uploads  map[string]drive.UploadSession
	parts    map[string][]drive.CompletedPart
	readyErr error
}

func NewRepository() *Repository {
	return &Repository{
		tenants: make(map[string]drive.Tenant), nodes: make(map[string]drive.Node),
		blobs: make(map[string]drive.Blob), versions: make(map[string]drive.FileVersion),
		uploads: make(map[string]drive.UploadSession), parts: make(map[string][]drive.CompletedPart),
	}
}

func (r *Repository) SetReadyError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readyErr = err
}

func (r *Repository) Ready(context.Context) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.readyErr
}

func (r *Repository) Close() {}

func (r *Repository) EnsureTenant(_ context.Context, seed drive.TenantSeed) (drive.Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tenant, ok := r.tenants[seed.TenantID]; ok {
		return tenant, nil
	}
	tenant := drive.Tenant{ID: seed.TenantID, DisplayName: seed.DisplayName, RootNodeID: seed.RootNodeID, CreatedAt: seed.Now}
	root := drive.Node{
		ID: seed.RootNodeID, TenantID: seed.TenantID, Kind: drive.NodeDirectory,
		DisplayName: "", NormalizedName: "", Status: drive.NodeActive, Revision: 1,
		CreatedAt: seed.Now, UpdatedAt: seed.Now,
	}
	r.tenants[tenant.ID] = tenant
	r.nodes[root.ID] = root
	return tenant, nil
}

func (r *Repository) Tenant(_ context.Context, tenantID string) (drive.Tenant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tenant, ok := r.tenants[tenantID]
	if !ok {
		return drive.Tenant{}, drive.E(drive.CodeNotFound, "tenant was not found")
	}
	return tenant, nil
}

func (r *Repository) CreateDirectory(_ context.Context, command drive.CreateDirectoryCommand) (drive.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	parent, ok := r.activeNode(command.Identity.TenantID, command.ParentID)
	if !ok || parent.Kind != drive.NodeDirectory {
		return drive.Node{}, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	if r.nameExists(command.Identity.TenantID, command.ParentID, command.NormalizedName, "") {
		return drive.Node{}, drive.E(drive.CodeNameConflict, "an active item with this name already exists")
	}
	node := drive.Node{
		ID: command.ID, TenantID: command.Identity.TenantID, ParentID: command.ParentID,
		Kind: drive.NodeDirectory, DisplayName: command.DisplayName, NormalizedName: command.NormalizedName,
		Status: drive.NodeActive, Revision: 1, CreatedAt: command.Now, UpdatedAt: command.Now,
	}
	r.nodes[node.ID] = node
	return node, nil
}

func (r *Repository) Node(_ context.Context, identity drive.Identity, id string, includeTrashed bool) (drive.Node, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[id]
	if !ok || node.TenantID != identity.TenantID || (!includeTrashed && !visible(node)) {
		return drive.Node{}, drive.E(drive.CodeNotFound, "resource was not found")
	}
	return node, nil
}

func (r *Repository) ListChildren(_ context.Context, identity drive.Identity, parentID string, after drive.CursorPosition, limit int) ([]drive.Node, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	parent, ok := r.nodes[parentID]
	if !ok || parent.TenantID != identity.TenantID || parent.Kind != drive.NodeDirectory || !visible(parent) {
		return nil, false, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	items := make([]drive.Node, 0)
	for _, node := range r.nodes {
		if node.TenantID == identity.TenantID && node.ParentID == parentID && visible(node) && afterPosition(node, after) {
			items = append(items, node)
		}
	}
	sortNodes(items)
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) UpdateNode(_ context.Context, command drive.UpdateNodeCommand) (drive.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.activeNode(command.Identity.TenantID, command.NodeID)
	if !ok {
		return drive.Node{}, drive.E(drive.CodeNotFound, "resource was not found")
	}
	if tenant := r.tenants[command.Identity.TenantID]; tenant.RootNodeID == node.ID {
		return drive.Node{}, drive.E(drive.CodeInvalidRequest, "root directory cannot be changed")
	}
	if node.Revision != command.ExpectedRevision {
		return drive.Node{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	targetParent := node.ParentID
	if command.ParentID != nil {
		targetParent = *command.ParentID
		parent, exists := r.activeNode(command.Identity.TenantID, targetParent)
		if !exists || parent.Kind != drive.NodeDirectory {
			return drive.Node{}, drive.E(drive.CodeNotFound, "target directory was not found")
		}
		if node.Kind == drive.NodeDirectory && r.isDescendant(targetParent, node.ID, command.Identity.TenantID) {
			return drive.Node{}, drive.E(drive.CodeInvalidRequest, "directory cannot be moved into itself or a descendant")
		}
	}
	targetName := node.NormalizedName
	if command.NormalizedName != nil {
		targetName = *command.NormalizedName
	}
	if r.nameExists(command.Identity.TenantID, targetParent, targetName, node.ID) {
		return drive.Node{}, drive.E(drive.CodeNameConflict, "an active item with this name already exists")
	}
	if command.DisplayName != nil {
		node.DisplayName = *command.DisplayName
		node.NormalizedName = *command.NormalizedName
	}
	if command.ParentID != nil {
		node.ParentID = *command.ParentID
	}
	node.Revision++
	node.UpdatedAt = command.Now
	r.nodes[node.ID] = node
	return node, nil
}

func (r *Repository) CreateUpload(_ context.Context, command drive.CreateUploadCommand) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := command.Session
	parent, ok := r.activeNode(session.TenantID, session.ParentID)
	if !ok || parent.Kind != drive.NodeDirectory {
		return drive.UploadSession{}, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	if r.nameExists(session.TenantID, session.ParentID, session.NormalizedName, "") {
		return drive.UploadSession{}, drive.E(drive.CodeNameConflict, "an active item with this name already exists")
	}
	if _, exists := r.uploads[session.ID]; exists {
		return drive.UploadSession{}, drive.E(drive.CodeIdempotencyConflict, "upload id already exists")
	}
	r.uploads[session.ID] = session
	return session, nil
}

func (r *Repository) Upload(_ context.Context, identity drive.Identity, id string) (drive.UploadSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.uploadFor(identity, id)
}

func (r *Repository) MarkUploading(_ context.Context, identity drive.Identity, id string, now time.Time) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return drive.UploadSession{}, err
	}
	if session.Status == drive.UploadUploading {
		return session, nil
	}
	if session.Status != drive.UploadCreated {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot transition to uploading")
	}
	session.Status = drive.UploadUploading
	session.Revision++
	session.UpdatedAt = now
	r.uploads[id] = session
	return session, nil
}

func (r *Repository) BeginComplete(_ context.Context, identity drive.Identity, id, digest string, parts []drive.CompletedPart, now time.Time) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return drive.UploadSession{}, err
	}
	if session.CompletionDigest != "" && session.CompletionDigest != digest {
		return drive.UploadSession{}, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadCommitted || session.Status == drive.UploadObjectCompleted || session.Status == drive.UploadCompleting {
		return session, nil
	}
	if session.Status != drive.UploadCreated && session.Status != drive.UploadUploading {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot be completed")
	}
	if !now.Before(session.ExpiresAt) {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session has expired")
	}
	session.Status = drive.UploadCompleting
	session.CompletionDigest = digest
	session.Revision++
	session.UpdatedAt = now
	r.uploads[id] = session
	r.parts[id] = append([]drive.CompletedPart(nil), parts...)
	return session, nil
}

func (r *Repository) FailUploadCompletion(_ context.Context, identity drive.Identity, id, digest, failureCode string, now time.Time) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if failureCode == "" {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidRequest, "upload failure code is required")
	}
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return drive.UploadSession{}, err
	}
	if session.CompletionDigest != digest {
		return drive.UploadSession{}, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadFailed {
		return session, nil
	}
	if session.Status != drive.UploadCompleting {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload completion cannot transition to failed")
	}
	session.Status = drive.UploadFailed
	session.FailureCode = failureCode
	session.Revision++
	session.UpdatedAt = now
	r.uploads[id] = session
	return session, nil
}

func (r *Repository) MarkObjectCompleted(_ context.Context, identity drive.Identity, id string, _ drive.ObjectInfo, now time.Time) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return drive.UploadSession{}, err
	}
	if session.Status == drive.UploadObjectCompleted || session.Status == drive.UploadCommitted {
		return session, nil
	}
	if session.Status != drive.UploadCompleting {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload object cannot be marked complete")
	}
	session.Status = drive.UploadObjectCompleted
	session.Revision++
	session.UpdatedAt = now
	r.uploads[id] = session
	return session, nil
}

func (r *Repository) CommitUpload(_ context.Context, command drive.CommitUploadCommand) (drive.CompleteResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(command.Identity, command.SessionID)
	if err != nil {
		return drive.CompleteResult{}, false, err
	}
	if session.CompletionDigest != command.Digest {
		return drive.CompleteResult{}, false, drive.E(drive.CodeIdempotencyConflict, "completion payload differs from the first request")
	}
	if session.Status == drive.UploadCommitted {
		return r.completeResult(session), false, nil
	}
	if session.Status != drive.UploadObjectCompleted {
		return drive.CompleteResult{}, false, drive.E(drive.CodeInvalidState, "upload object is not completed")
	}
	parent, ok := r.activeNode(command.Identity.TenantID, session.ParentID)
	if !ok || parent.Kind != drive.NodeDirectory {
		session.Status = drive.UploadFailed
		session.FailureCode = drive.UploadFailureParentUnavailable
		session.Revision++
		session.UpdatedAt = command.Now
		r.uploads[session.ID] = session
		return drive.CompleteResult{}, false, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	if r.nameExists(session.TenantID, session.ParentID, session.NormalizedName, "") {
		session.Status = drive.UploadFailed
		session.FailureCode = drive.UploadFailureNameConflict
		session.Revision++
		session.UpdatedAt = command.Now
		r.uploads[session.ID] = session
		return drive.CompleteResult{}, false, drive.E(drive.CodeNameConflict, "an active item with this name already exists")
	}
	r.blobs[command.Blob.ID] = command.Blob
	r.versions[command.Version.ID] = command.Version
	r.nodes[command.Node.ID] = command.Node
	session.Status = drive.UploadCommitted
	session.CommittedNodeID = command.Node.ID
	session.Revision++
	session.UpdatedAt = command.Now
	r.uploads[session.ID] = session
	return drive.CompleteResult{Upload: session, Node: command.Node, Blob: command.Blob, Version: command.Version}, true, nil
}

func (r *Repository) AbortUpload(_ context.Context, identity drive.Identity, id string, target drive.UploadStatus, now time.Time) (drive.UploadSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return drive.UploadSession{}, err
	}
	if session.Status == drive.UploadCommitted {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "committed upload cannot be aborted")
	}
	if session.Status == drive.UploadAborted || session.Status == drive.UploadExpired || session.Status == drive.UploadFailed {
		return session, nil
	}
	if session.Status != drive.UploadCreated && session.Status != drive.UploadUploading {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "upload session cannot be aborted while completing")
	}
	if target != drive.UploadAborted && target != drive.UploadExpired && target != drive.UploadFailed {
		return drive.UploadSession{}, drive.E(drive.CodeInvalidState, "invalid upload terminal state")
	}
	session.Status = target
	session.Revision++
	session.UpdatedAt = now
	r.uploads[id] = session
	return session, nil
}

func (r *Repository) ExpiredUploads(_ context.Context, now time.Time, limit int) ([]drive.UploadSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]drive.UploadSession, 0)
	for _, session := range r.uploads {
		if !session.Status.Terminal() && !now.Before(session.ExpiresAt) {
			result = append(result, session)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExpiresAt.Before(result[j].ExpiresAt) })
	if len(result) > limit && limit > 0 {
		result = result[:limit]
	}
	return result, nil
}

func (r *Repository) DownloadBlob(_ context.Context, identity drive.Identity, nodeID string) (drive.Node, drive.Blob, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[nodeID]
	if !ok || node.TenantID != identity.TenantID || node.Kind != drive.NodeFile || !visible(node) {
		return drive.Node{}, drive.Blob{}, drive.E(drive.CodeNotFound, "file was not found")
	}
	version, ok := r.versions[node.CurrentVersionID]
	if !ok || version.TenantID != identity.TenantID {
		return drive.Node{}, drive.Blob{}, drive.E(drive.CodeInternal, "file version is missing")
	}
	blob, ok := r.blobs[version.BlobID]
	if !ok || blob.TenantID != identity.TenantID || blob.Status != drive.BlobAvailable {
		return drive.Node{}, drive.Blob{}, drive.E(drive.CodeNotFound, "file content was not found")
	}
	return node, blob, nil
}

func (r *Repository) Recycle(_ context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[nodeID]
	if !ok || node.TenantID != identity.TenantID {
		return drive.E(drive.CodeNotFound, "resource was not found")
	}
	if r.tenants[identity.TenantID].RootNodeID == nodeID {
		return drive.E(drive.CodeInvalidRequest, "root directory cannot be recycled")
	}
	if node.Status == drive.NodeTrashed && node.TrashedRootID == node.ID {
		return nil
	}
	if !visible(node) {
		return drive.E(drive.CodeNotFound, "resource was not found")
	}
	if node.Revision != revision {
		return drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	ids := r.activeSubtree(node.ID, identity.TenantID)
	for _, id := range ids {
		child := r.nodes[id]
		child.TrashedRootID = node.ID
		child.UpdatedAt = now
		if id == node.ID {
			child.Status = drive.NodeTrashed
			child.OriginalParentID = child.ParentID
			child.Revision++
			deletedAt := now
			child.DeletedAt = &deletedAt
		}
		r.nodes[id] = child
	}
	return nil
}

func (r *Repository) ListRecycle(_ context.Context, identity drive.Identity, after drive.CursorPosition, limit int) ([]drive.RecycleEntry, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]drive.RecycleEntry, 0)
	for _, node := range r.nodes {
		if node.TenantID == identity.TenantID && node.Status == drive.NodeTrashed && node.TrashedRootID == node.ID && afterPosition(node, after) {
			deletedAt := time.Time{}
			if node.DeletedAt != nil {
				deletedAt = *node.DeletedAt
			}
			items = append(items, drive.RecycleEntry{Node: node, OriginalParentID: node.OriginalParentID, DeletedAt: deletedAt})
		}
	}
	sort.Slice(items, func(i, j int) bool { return nodeLess(items[i].Node, items[j].Node) })
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) Restore(_ context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) (drive.Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[nodeID]
	if !ok || node.TenantID != identity.TenantID || node.Status != drive.NodeTrashed || node.TrashedRootID != node.ID {
		return drive.Node{}, drive.E(drive.CodeNotFound, "recycle entry was not found")
	}
	if node.Revision != revision {
		return drive.Node{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	parent, ok := r.activeNode(identity.TenantID, node.OriginalParentID)
	if !ok || parent.Kind != drive.NodeDirectory || r.nameExists(identity.TenantID, node.OriginalParentID, node.NormalizedName, node.ID) {
		return drive.Node{}, drive.E(drive.CodeRestoreConflict, "original location is unavailable")
	}
	for _, id := range r.trashedSubtree(node.ID, identity.TenantID) {
		child := r.nodes[id]
		child.TrashedRootID = ""
		child.UpdatedAt = now
		if id == node.ID {
			child.Status = drive.NodeActive
			child.ParentID = child.OriginalParentID
			child.OriginalParentID = ""
			child.DeletedAt = nil
			child.Revision++
			node = child
		}
		r.nodes[id] = child
	}
	return node, nil
}

func (r *Repository) PreparePurge(_ context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) (drive.PurgePlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	node, ok := r.nodes[nodeID]
	if !ok || node.TenantID != identity.TenantID || (node.Status != drive.NodeTrashed && node.Status != drive.NodePurging) || node.TrashedRootID != node.ID {
		return drive.PurgePlan{}, drive.E(drive.CodeNotFound, "recycle entry was not found")
	}
	if node.Revision != revision {
		return drive.PurgePlan{}, drive.E(drive.CodeRevisionMismatch, "resource revision does not match")
	}
	subtree := r.trashedSubtree(node.ID, identity.TenantID)
	inSubtree := make(map[string]bool, len(subtree))
	for _, id := range subtree {
		inSubtree[id] = true
		child := r.nodes[id]
		child.Status = drive.NodePurging
		child.UpdatedAt = now
		r.nodes[id] = child
	}
	blobIDs := make(map[string]bool)
	for _, version := range r.versions {
		if version.TenantID == identity.TenantID && inSubtree[version.NodeID] {
			blobIDs[version.BlobID] = true
		}
	}
	plan := drive.PurgePlan{RootID: node.ID}
	for blobID := range blobIDs {
		referencedOutside := false
		for _, version := range r.versions {
			if version.BlobID == blobID && !inSubtree[version.NodeID] {
				referencedOutside = true
				break
			}
		}
		blob := r.blobs[blobID]
		if !referencedOutside && blob.Status != drive.BlobDeleted {
			blob.Status = drive.BlobPendingDelete
			blob.ReferenceCount = 0
			r.blobs[blobID] = blob
			plan.Blobs = append(plan.Blobs, blob)
		}
	}
	return plan, nil
}

func (r *Repository) FinishPurge(_ context.Context, identity drive.Identity, plan drive.PurgePlan, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	root, ok := r.nodes[plan.RootID]
	if !ok || root.TenantID != identity.TenantID || root.Status != drive.NodePurging {
		return drive.E(drive.CodeNotFound, "purge plan was not found")
	}
	for _, planned := range plan.Blobs {
		blob, ok := r.blobs[planned.ID]
		if ok && blob.TenantID == identity.TenantID && blob.Status == drive.BlobPendingDelete {
			deletedAt := now
			blob.Status = drive.BlobDeleted
			blob.DeletedAt = &deletedAt
			r.blobs[blob.ID] = blob
		}
	}
	return nil
}

func (r *Repository) activeNode(tenantID, id string) (drive.Node, bool) {
	node, ok := r.nodes[id]
	return node, ok && node.TenantID == tenantID && visible(node)
}

func (r *Repository) uploadFor(identity drive.Identity, id string) (drive.UploadSession, error) {
	session, ok := r.uploads[id]
	if !ok || session.TenantID != identity.TenantID {
		return drive.UploadSession{}, drive.E(drive.CodeNotFound, "upload session was not found")
	}
	return session, nil
}

func (r *Repository) nameExists(tenantID, parentID, normalizedName, exceptID string) bool {
	for _, node := range r.nodes {
		if node.ID != exceptID && node.TenantID == tenantID && node.ParentID == parentID && node.NormalizedName == normalizedName && visible(node) {
			return true
		}
	}
	return false
}

func (r *Repository) isDescendant(candidateID, ancestorID, tenantID string) bool {
	current := candidateID
	for current != "" {
		if current == ancestorID {
			return true
		}
		node, ok := r.nodes[current]
		if !ok || node.TenantID != tenantID {
			return false
		}
		current = node.ParentID
	}
	return false
}

func (r *Repository) activeSubtree(rootID, tenantID string) []string {
	result := []string{rootID}
	for i := 0; i < len(result); i++ {
		for _, node := range r.nodes {
			if node.TenantID == tenantID && node.ParentID == result[i] && visible(node) {
				result = append(result, node.ID)
			}
		}
	}
	return result
}

func (r *Repository) trashedSubtree(rootID, tenantID string) []string {
	result := make([]string, 0)
	for _, node := range r.nodes {
		if node.TenantID == tenantID && node.TrashedRootID == rootID {
			result = append(result, node.ID)
		}
	}
	return result
}

func (r *Repository) completeResult(session drive.UploadSession) drive.CompleteResult {
	node := r.nodes[session.CommittedNodeID]
	version := r.versions[node.CurrentVersionID]
	blob := r.blobs[version.BlobID]
	return drive.CompleteResult{Upload: session, Node: node, Blob: blob, Version: version}
}

func visible(node drive.Node) bool {
	return node.Status == drive.NodeActive && node.TrashedRootID == ""
}

func afterPosition(node drive.Node, after drive.CursorPosition) bool {
	return after.ID == "" || node.NormalizedName > after.Name || (node.NormalizedName == after.Name && node.ID > after.ID)
}

func nodeLess(a, b drive.Node) bool {
	return a.NormalizedName < b.NormalizedName || (a.NormalizedName == b.NormalizedName && a.ID < b.ID)
}

func sortNodes(nodes []drive.Node) {
	sort.Slice(nodes, func(i, j int) bool { return nodeLess(nodes[i], nodes[j]) })
}

var _ drive.Repository = (*Repository)(nil)
