package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type Repository struct {
	mu                 sync.RWMutex
	tenants            map[string]drive.Tenant
	principals         map[string]drive.PrincipalRecord
	members            map[string]drive.PrincipalRecord
	nodes              map[string]drive.Node
	blobs              map[string]drive.Blob
	versions           map[string]drive.FileVersion
	uploads            map[string]drive.UploadSession
	parts              map[string][]drive.CompletedPart
	idempotency        map[string]drive.IdempotencyRecord
	uploadMaintenance  map[string]maintenanceLease
	recycleMaintenance map[string]maintenanceLease
	invitations        map[string]drive.TenantInvitation
	invitationTokens   map[string]string
	groups             map[string]drive.TenantGroup
	groupMembers       map[string]struct{}
	acls               map[string]drive.NodeACLEntry
	audit              []drive.AuditEvent
	auditSequence      int64
	readyErr           error
}

func NewRepository() *Repository {
	return &Repository{
		tenants: make(map[string]drive.Tenant), principals: make(map[string]drive.PrincipalRecord),
		members: make(map[string]drive.PrincipalRecord), nodes: make(map[string]drive.Node),
		blobs: make(map[string]drive.Blob), versions: make(map[string]drive.FileVersion),
		uploads: make(map[string]drive.UploadSession), parts: make(map[string][]drive.CompletedPart),
		idempotency:       make(map[string]drive.IdempotencyRecord),
		uploadMaintenance: make(map[string]maintenanceLease), recycleMaintenance: make(map[string]maintenanceLease),
		invitations: make(map[string]drive.TenantInvitation), invitationTokens: make(map[string]string),
		groups: make(map[string]drive.TenantGroup), groupMembers: make(map[string]struct{}), acls: make(map[string]drive.NodeACLEntry),
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

func (r *Repository) EnsureOIDCMember(_ context.Context, seed drive.OIDCMemberSeed) (drive.PrincipalRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seed.TenantID == "" || seed.PrincipalID == "" || seed.Issuer == "" || seed.Subject == "" || !drive.ValidAccessRole(seed.Role) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "OIDC member seed is invalid")
	}
	tenant, ok := r.tenants[seed.TenantID]
	if !ok {
		return drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "tenant was not found")
	}
	externalKey := principalKey(seed.Issuer, seed.Subject)
	principal, exists := r.principals[externalKey]
	if exists && principal.Identity.PrincipalID != seed.PrincipalID {
		return drive.PrincipalRecord{}, drive.E(drive.CodeNameConflict, "OIDC identity is mapped to another principal")
	}
	if !exists {
		for _, candidate := range r.principals {
			if candidate.Identity.PrincipalID == seed.PrincipalID && (candidate.Issuer != seed.Issuer || candidate.Subject != seed.Subject) {
				return drive.PrincipalRecord{}, drive.E(drive.CodeNameConflict, "principal id is mapped to another OIDC identity")
			}
		}
		principal = drive.PrincipalRecord{
			Identity: drive.Identity{TenantID: seed.TenantID, PrincipalID: seed.PrincipalID},
			Issuer:   seed.Issuer, Subject: seed.Subject, DisplayName: seed.DisplayName,
		}
		r.principals[externalKey] = principal
	}
	memberKey := memberKey(seed.TenantID, seed.PrincipalID)
	if member, exists := r.members[memberKey]; exists {
		return member, nil
	}
	member := drive.PrincipalRecord{
		Identity: drive.Identity{TenantID: seed.TenantID, PrincipalID: seed.PrincipalID},
		Issuer:   seed.Issuer, Subject: seed.Subject, DisplayName: seed.DisplayName,
		TenantDisplayName: tenant.DisplayName, Role: seed.Role, Status: drive.MemberStatusActive,
	}
	r.members[memberKey] = member
	return member, nil
}

func (r *Repository) ResolveOIDCPrincipal(_ context.Context, issuer, subject, tenantID string) (drive.PrincipalRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	principal, ok := r.principals[principalKey(issuer, subject)]
	if !ok {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "identity is not a member of this tenant")
	}
	member, ok := r.members[memberKey(tenantID, principal.Identity.PrincipalID)]
	if !ok || member.Status != drive.MemberStatusActive {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "identity is not a member of this tenant")
	}
	return member, nil
}

func (r *Repository) SetOIDCMemberStatus(_ context.Context, tenantID, principalID string, status drive.MemberStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tenantID == "" || principalID == "" || !drive.ValidMemberStatus(status) {
		return drive.E(drive.CodeInvalidRequest, "OIDC member status is invalid")
	}
	key := memberKey(tenantID, principalID)
	member, ok := r.members[key]
	if !ok {
		return drive.E(drive.CodeNotFound, "tenant member was not found")
	}
	member.Status = status
	r.members[key] = member
	return nil
}

func (r *Repository) ListMembers(_ context.Context, tenantID string, after drive.CursorPosition, limit int) ([]drive.PrincipalRecord, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.tenants[tenantID]; !ok {
		return nil, false, drive.E(drive.CodeNotFound, "tenant was not found")
	}
	items := make([]drive.PrincipalRecord, 0)
	for _, member := range r.members {
		if member.Identity.TenantID == tenantID && afterMemberPosition(member, after) {
			items = append(items, member)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Identity.PrincipalID < items[j].Identity.PrincipalID
	})
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *Repository) UpdateMember(ctx context.Context, command drive.UpdateMemberCommand) (drive.PrincipalRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if command.TenantID == "" || command.PrincipalID == "" || command.ActorPrincipalID == "" ||
		!drive.ValidAccessRole(command.ActorRole) || (command.Role == nil && command.Status == nil) ||
		command.Role != nil && !drive.ValidAccessRole(*command.Role) ||
		command.Status != nil && !drive.ValidMemberStatus(*command.Status) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "member update is invalid")
	}
	actor, ok := r.members[memberKey(command.TenantID, command.ActorPrincipalID)]
	if !ok || actor.Status != drive.MemberStatusActive || actor.Role != command.ActorRole ||
		(command.ActorRole != drive.RoleOwner && command.ActorRole != drive.RoleAdmin) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "only active owners and admins may manage members")
	}
	key := memberKey(command.TenantID, command.PrincipalID)
	member, ok := r.members[key]
	if !ok {
		return drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "tenant member was not found")
	}
	if command.ActorRole == drive.RoleAdmin && (member.Role == drive.RoleOwner || command.Role != nil && *command.Role == drive.RoleOwner) {
		return drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "admins cannot modify owners or grant the owner role")
	}
	newRole, newStatus := member.Role, member.Status
	if command.Role != nil {
		newRole = *command.Role
	}
	if command.Status != nil {
		newStatus = *command.Status
	}
	if member.Role == drive.RoleOwner && member.Status == drive.MemberStatusActive &&
		(newRole != drive.RoleOwner || newStatus != drive.MemberStatusActive) && r.activeOwnerCount(command.TenantID, command.PrincipalID) == 0 {
		return drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "the last active owner cannot be removed")
	}
	member.Role, member.Status = newRole, newStatus
	member.TenantDisplayName = r.tenants[command.TenantID].DisplayName
	r.members[key] = member
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: command.TenantID, ActorPrincipalID: command.ActorPrincipalID, Action: "tenant.member.updated", TargetType: "principal", TargetID: command.PrincipalID, OccurredAt: command.Now, Metadata: map[string]string{"role": string(member.Role), "status": string(member.Status)}}); err != nil {
		return drive.PrincipalRecord{}, err
	}
	return member, nil
}

func (r *Repository) CreateDirectory(ctx context.Context, command drive.CreateDirectoryCommand) (drive.Node, error) {
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
	if err := r.completeIdempotency(command.Idempotency, node.ID, command.Now); err != nil {
		return drive.Node{}, err
	}
	r.nodes[node.ID] = node
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: command.Identity.TenantID, ActorPrincipalID: command.Identity.PrincipalID, Action: "node.created", TargetType: "node", TargetID: node.ID, OccurredAt: command.Now, Metadata: map[string]string{"kind": string(node.Kind), "parent_id": node.ParentID}}); err != nil {
		delete(r.nodes, node.ID)
		return drive.Node{}, err
	}
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

func (r *Repository) UpdateNode(ctx context.Context, command drive.UpdateNodeCommand) (drive.Node, error) {
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
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: command.Identity.TenantID, ActorPrincipalID: command.Identity.PrincipalID, Action: "node.updated", TargetType: "node", TargetID: node.ID, OccurredAt: command.Now, Metadata: map[string]string{"parent_id": node.ParentID}}); err != nil {
		return drive.Node{}, err
	}
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
	if err := r.completeIdempotency(command.Idempotency, session.ID, session.CreatedAt); err != nil {
		return drive.UploadSession{}, err
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
	lease := r.uploadMaintenance[id]
	lease.cleanupPending = true
	r.uploadMaintenance[id] = lease
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

func (r *Repository) CommitUpload(ctx context.Context, command drive.CommitUploadCommand) (drive.CompleteResult, bool, error) {
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
		lease := r.uploadMaintenance[session.ID]
		lease.cleanupPending = true
		r.uploadMaintenance[session.ID] = lease
		return drive.CompleteResult{}, false, drive.E(drive.CodeNotFound, "parent directory was not found")
	}
	if r.nameExists(session.TenantID, session.ParentID, session.NormalizedName, "") {
		session.Status = drive.UploadFailed
		session.FailureCode = drive.UploadFailureNameConflict
		session.Revision++
		session.UpdatedAt = command.Now
		r.uploads[session.ID] = session
		lease := r.uploadMaintenance[session.ID]
		lease.cleanupPending = true
		r.uploadMaintenance[session.ID] = lease
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
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: command.Identity.TenantID, ActorPrincipalID: command.Identity.PrincipalID, Action: "upload.committed", TargetType: "node", TargetID: command.Node.ID, OccurredAt: command.Now, Metadata: map[string]string{"upload_id": session.ID, "kind": string(command.Node.Kind)}}); err != nil {
		return drive.CompleteResult{}, false, err
	}
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
	lease := r.uploadMaintenance[id]
	lease.cleanupPending = true
	r.uploadMaintenance[id] = lease
	return session, nil
}

func (r *Repository) MarkUploadCleanupComplete(_ context.Context, identity drive.Identity, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, err := r.uploadFor(identity, id)
	if err != nil {
		return err
	}
	if !session.Status.Terminal() {
		return drive.E(drive.CodeInvalidState, "upload cleanup cannot be completed")
	}
	lease := r.uploadMaintenance[id]
	lease.cleanupPending, lease.errorCode, lease.notBefore = false, "", time.Time{}
	r.uploadMaintenance[id] = lease
	return nil
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

func (r *Repository) Recycle(ctx context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) error {
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
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: identity.TenantID, ActorPrincipalID: identity.PrincipalID, Action: "node.recycled", TargetType: "node", TargetID: nodeID, OccurredAt: now, Metadata: map[string]string{}})
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

func (r *Repository) Restore(ctx context.Context, identity drive.Identity, nodeID string, revision int64, now time.Time) (drive.Node, error) {
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
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: identity.TenantID, ActorPrincipalID: identity.PrincipalID, Action: "node.restored", TargetType: "node", TargetID: nodeID, OccurredAt: now, Metadata: map[string]string{}}); err != nil {
		return drive.Node{}, err
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

func (r *Repository) FinishPurge(ctx context.Context, identity drive.Identity, plan drive.PurgePlan, now time.Time) error {
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
	lease := r.recycleMaintenance[plan.RootID]
	lease.owner, lease.until = "", time.Time{}
	lease.notBefore = time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC)
	lease.errorCode = ""
	r.recycleMaintenance[plan.RootID] = lease
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: identity.TenantID, ActorPrincipalID: identity.PrincipalID, Action: "node.purged", TargetType: "node", TargetID: plan.RootID, OccurredAt: now, Metadata: map[string]string{}})
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

func principalKey(issuer, subject string) string { return issuer + "\x00" + subject }

func memberKey(tenantID, principalID string) string { return tenantID + "\x00" + principalID }

func afterMemberPosition(member drive.PrincipalRecord, after drive.CursorPosition) bool {
	return after.ID == "" || member.Identity.PrincipalID > after.Name
}

func (r *Repository) activeOwnerCount(tenantID, excludePrincipalID string) int {
	count := 0
	for _, member := range r.members {
		if member.Identity.TenantID == tenantID && member.Identity.PrincipalID != excludePrincipalID &&
			member.Role == drive.RoleOwner && member.Status == drive.MemberStatusActive {
			count++
		}
	}
	return count
}

func sortNodes(nodes []drive.Node) {
	sort.Slice(nodes, func(i, j int) bool { return nodeLess(nodes[i], nodes[j]) })
}

var _ drive.Repository = (*Repository)(nil)
