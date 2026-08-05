package memory

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func (r *Repository) DeleteMember(ctx context.Context, c drive.DeleteMemberCommand) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.E(drive.CodeForbidden, "only active owners and admins may manage members")
	}
	m, ok := r.members[memberKey(c.TenantID, c.PrincipalID)]
	if !ok {
		return drive.E(drive.CodeNotFound, "tenant member was not found")
	}
	if c.ActorRole == drive.RoleAdmin && m.Role == drive.RoleOwner {
		return drive.E(drive.CodeForbidden, "admins cannot delete owners")
	}
	if m.Role == drive.RoleOwner && m.Status == drive.MemberStatusActive && r.activeOwnerCount(c.TenantID, c.PrincipalID) == 0 {
		return drive.E(drive.CodeInvalidState, "the last active owner cannot be removed")
	}
	delete(r.members, memberKey(c.TenantID, c.PrincipalID))
	for k := range r.groupMembers {
		if k == groupMemberKey(c.TenantID, "", c.PrincipalID) || hasMemberSuffix(k, c.TenantID, c.PrincipalID) {
			delete(r.groupMembers, k)
		}
	}
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.member.deleted", TargetType: "principal", TargetID: c.PrincipalID, OccurredAt: c.Now, Metadata: map[string]string{"role": string(m.Role)}})
}

func (r *Repository) CreateInvitation(ctx context.Context, c drive.CreateInvitationCommand) (drive.TenantInvitation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.TenantInvitation{}, drive.E(drive.CodeForbidden, "only owners and admins may invite members")
	}
	if !drive.ValidID(c.ID) || !drive.ValidID(c.TenantID) || !drive.ValidID(c.ActorPrincipalID) || !drive.ValidAccessRole(c.ActorRole) ||
		c.Issuer == "" || c.Subject == "" || strings.TrimSpace(c.Issuer) == "" || strings.TrimSpace(c.Subject) == "" || len(c.Issuer) > 512 || len(c.Subject) > 512 || len(c.DisplayName) > 256 || len(c.TokenHash) != 64 || !drive.ValidAccessRole(c.Role) || !c.ExpiresAt.After(c.Now) {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidRequest, "invitation is invalid")
	}
	if c.ActorRole == drive.RoleAdmin && c.Role == drive.RoleOwner {
		return drive.TenantInvitation{}, drive.E(drive.CodeForbidden, "admins cannot invite owners")
	}
	for _, x := range r.invitations {
		if x.TenantID == c.TenantID && x.Issuer == c.Issuer && x.Subject == c.Subject && x.Status == drive.InvitationPending {
			return drive.TenantInvitation{}, drive.E(drive.CodeNameConflict, "a pending invitation already exists")
		}
	}
	v := drive.TenantInvitation{ID: c.ID, TenantID: c.TenantID, Issuer: c.Issuer, Subject: c.Subject, DisplayName: c.DisplayName, Role: c.Role, Status: drive.InvitationPending, CreatedBy: c.ActorPrincipalID, ExpiresAt: c.ExpiresAt, CreatedAt: c.Now, UpdatedAt: c.Now}
	r.invitations[v.ID] = v
	r.invitationTokens[c.TokenHash] = v.ID
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.invitation.created", TargetType: "invitation", TargetID: c.ID, OccurredAt: c.Now, Metadata: map[string]string{"role": string(c.Role)}}); err != nil {
		delete(r.invitations, v.ID)
		delete(r.invitationTokens, c.TokenHash)
		return drive.TenantInvitation{}, err
	}
	return v, nil
}
func (r *Repository) ListInvitations(_ context.Context, tenant string, status drive.InvitationStatus, limit int) ([]drive.TenantInvitation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[tenant]; !ok {
		return nil, drive.E(drive.CodeNotFound, "tenant was not found")
	}
	out := []drive.TenantInvitation{}
	now := time.Now().UTC()
	for id, v := range r.invitations {
		if v.TenantID != tenant {
			continue
		}
		if v.Status == drive.InvitationPending && !v.ExpiresAt.After(now) {
			v.Status = drive.InvitationExpired
			v.UpdatedAt = now
			r.invitations[id] = v
		}
		if status == "" || v.Status == status {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *Repository) AcceptInvitation(ctx context.Context, c drive.AcceptInvitationCommand) (drive.TenantInvitation, drive.PrincipalRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(c.TokenHash) != 64 || !drive.ValidID(c.CandidatePrincipalID) || c.Issuer == "" || c.Subject == "" || strings.TrimSpace(c.Issuer) == "" || strings.TrimSpace(c.Subject) == "" || len(c.Issuer) > 512 || len(c.Subject) > 512 {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidRequest, "invitation acceptance is invalid")
	}
	id, ok := r.invitationTokens[c.TokenHash]
	if !ok {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeNotFound, "invitation was not found")
	}
	v := r.invitations[id]
	if v.Issuer != c.Issuer || v.Subject != c.Subject {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, drive.E(drive.CodeForbidden, "invitation identity does not match the authenticated identity")
	}
	if v.Status == drive.InvitationAccepted {
		m, ok := r.members[memberKey(v.TenantID, v.AcceptedPrincipalID)]
		if !ok {
			return v, drive.PrincipalRecord{}, drive.E(drive.CodeInternal, "accepted invitation has no membership")
		}
		return v, m, nil
	}
	if v.Status != drive.InvitationPending {
		return v, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "invitation is not pending")
	}
	if !v.ExpiresAt.After(c.Now) {
		v.Status = drive.InvitationExpired
		v.UpdatedAt = c.Now
		r.invitations[id] = v
		return v, drive.PrincipalRecord{}, drive.E(drive.CodeInvalidState, "invitation has expired")
	}
	p, exists := r.principals[principalKey(v.Issuer, v.Subject)]
	if exists && p.Identity.PrincipalID != c.CandidatePrincipalID {
		return v, drive.PrincipalRecord{}, drive.E(drive.CodeNameConflict, "OIDC identity is mapped to another principal")
	}
	if !exists {
		p = drive.PrincipalRecord{Identity: drive.Identity{PrincipalID: c.CandidatePrincipalID}, Issuer: v.Issuer, Subject: v.Subject, DisplayName: v.DisplayName}
		r.principals[principalKey(v.Issuer, v.Subject)] = p
	}
	m := drive.PrincipalRecord{Identity: drive.Identity{TenantID: v.TenantID, PrincipalID: p.Identity.PrincipalID}, Issuer: p.Issuer, Subject: p.Subject, DisplayName: p.DisplayName, TenantDisplayName: r.tenants[v.TenantID].DisplayName, Role: v.Role, Status: drive.MemberStatusActive}
	if old, ok := r.members[memberKey(v.TenantID, p.Identity.PrincipalID)]; ok {
		m = old
	}
	r.members[memberKey(v.TenantID, p.Identity.PrincipalID)] = m
	v.Status = drive.InvitationAccepted
	v.AcceptedPrincipalID = p.Identity.PrincipalID
	v.AcceptedAt = &c.Now
	v.UpdatedAt = c.Now
	r.invitations[id] = v
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: v.TenantID, ActorPrincipalID: p.Identity.PrincipalID, Action: "tenant.invitation.accepted", TargetType: "invitation", TargetID: v.ID, OccurredAt: c.Now, Metadata: map[string]string{"principal_id": p.Identity.PrincipalID}}); err != nil {
		return drive.TenantInvitation{}, drive.PrincipalRecord{}, err
	}
	return v, m, nil
}
func (r *Repository) RevokeInvitation(ctx context.Context, c drive.RevokeInvitationCommand) (drive.TenantInvitation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !drive.ValidID(c.TenantID) || !drive.ValidID(c.InvitationID) || !drive.ValidID(c.ActorPrincipalID) || !drive.ValidAccessRole(c.ActorRole) {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidRequest, "invitation revocation is invalid")
	}
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.TenantInvitation{}, drive.E(drive.CodeForbidden, "only owners and admins may revoke invitations")
	}
	v, ok := r.invitations[c.InvitationID]
	if !ok || v.TenantID != c.TenantID {
		return drive.TenantInvitation{}, drive.E(drive.CodeNotFound, "invitation was not found")
	}
	if v.Status != drive.InvitationPending {
		return drive.TenantInvitation{}, drive.E(drive.CodeInvalidState, "invitation is not pending")
	}
	v.Status = drive.InvitationRevoked
	v.RevokedAt = &c.Now
	v.UpdatedAt = c.Now
	r.invitations[v.ID] = v
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.invitation.revoked", TargetType: "invitation", TargetID: v.ID, OccurredAt: c.Now, Metadata: map[string]string{}}); err != nil {
		return drive.TenantInvitation{}, err
	}
	return v, nil
}

func (r *Repository) managingActor(t, p string, role drive.AccessRole) bool {
	x, ok := r.members[memberKey(t, p)]
	return ok && x.Status == drive.MemberStatusActive && x.Role == role && (role == drive.RoleOwner || role == drive.RoleAdmin)
}
func groupMemberKey(t, g, p string) string { return t + "/" + g + "/" + p }
func hasMemberSuffix(k, t, p string) bool {
	return len(k) > len(t)+len(p)+2 && k[:len(t)+1] == t+"/" && k[len(k)-len(p):] == p
}

func (r *Repository) CreateGroup(ctx context.Context, c drive.CreateGroupCommand) (drive.TenantGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.TenantGroup{}, drive.E(drive.CodeForbidden, "only owners and admins may manage groups")
	}
	for _, g := range r.groups {
		if g.TenantID == c.TenantID && g.NormalizedName == c.NormalizedName {
			return drive.TenantGroup{}, drive.E(drive.CodeNameConflict, "group name already exists")
		}
	}
	g := drive.TenantGroup{ID: c.ID, TenantID: c.TenantID, DisplayName: c.DisplayName, NormalizedName: c.NormalizedName, CreatedBy: c.ActorPrincipalID, CreatedAt: c.Now, UpdatedAt: c.Now}
	r.groups[g.ID] = g
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.group.created", TargetType: "group", TargetID: g.ID, OccurredAt: c.Now, Metadata: map[string]string{"name": g.DisplayName}}); err != nil {
		delete(r.groups, g.ID)
		return drive.TenantGroup{}, err
	}
	return g, nil
}
func (r *Repository) ListGroups(_ context.Context, t string) ([]drive.TenantGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []drive.TenantGroup{}
	for _, g := range r.groups {
		if g.TenantID == t {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NormalizedName < out[j].NormalizedName })
	return out, nil
}
func (r *Repository) UpdateGroup(ctx context.Context, c drive.UpdateGroupCommand) (drive.TenantGroup, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.TenantGroup{}, drive.E(drive.CodeForbidden, "only owners and admins may manage groups")
	}
	group, ok := r.groups[c.GroupID]
	if !ok || group.TenantID != c.TenantID {
		return drive.TenantGroup{}, drive.E(drive.CodeNotFound, "group was not found")
	}
	for id, candidate := range r.groups {
		if id != group.ID && candidate.TenantID == c.TenantID && candidate.NormalizedName == c.NormalizedName {
			return drive.TenantGroup{}, drive.E(drive.CodeNameConflict, "group name already exists")
		}
	}
	group.DisplayName, group.NormalizedName, group.UpdatedAt = c.DisplayName, c.NormalizedName, c.Now
	r.groups[group.ID] = group
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.group.updated", TargetType: "group", TargetID: group.ID, OccurredAt: c.Now, Metadata: map[string]string{"name": group.DisplayName}}); err != nil {
		return drive.TenantGroup{}, err
	}
	return group, nil
}
func (r *Repository) DeleteGroup(ctx context.Context, c drive.DeleteGroupCommand) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.E(drive.CodeForbidden, "only owners and admins may manage groups")
	}
	g, ok := r.groups[c.GroupID]
	if !ok || g.TenantID != c.TenantID {
		return drive.E(drive.CodeNotFound, "group was not found")
	}
	delete(r.groups, g.ID)
	for k := range r.groupMembers {
		if len(k) > len(c.TenantID)+len(g.ID)+2 && k[:len(c.TenantID)+len(g.ID)+2] == c.TenantID+"/"+g.ID+"/" {
			delete(r.groupMembers, k)
		}
	}
	for k, a := range r.acls {
		if a.TenantID == c.TenantID && a.SubjectType == drive.ACLSubjectGroup && a.SubjectID == g.ID {
			delete(r.acls, k)
		}
	}
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.group.deleted", TargetType: "group", TargetID: c.GroupID, OccurredAt: c.Now, Metadata: map[string]string{}})
}
func (r *Repository) groupMember(ctx context.Context, c drive.GroupMemberCommand, add bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.managingActor(c.TenantID, c.ActorPrincipalID, c.ActorRole) {
		return drive.E(drive.CodeForbidden, "only owners and admins may manage groups")
	}
	g, ok := r.groups[c.GroupID]
	if !ok || g.TenantID != c.TenantID {
		return drive.E(drive.CodeNotFound, "group was not found")
	}
	k := groupMemberKey(c.TenantID, c.GroupID, c.PrincipalID)
	changed := false
	if add {
		if _, ok := r.members[memberKey(c.TenantID, c.PrincipalID)]; !ok {
			return drive.E(drive.CodeNotFound, "tenant member was not found")
		}
		if _, exists := r.groupMembers[k]; !exists {
			r.groupMembers[k] = struct{}{}
			changed = true
		}
	} else {
		if _, exists := r.groupMembers[k]; exists {
			delete(r.groupMembers, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if add {
		return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.group.member_added", TargetType: "group", TargetID: c.GroupID, OccurredAt: c.Now, Metadata: map[string]string{"principal_id": c.PrincipalID}})
	}
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "tenant.group.member_removed", TargetType: "group", TargetID: c.GroupID, OccurredAt: c.Now, Metadata: map[string]string{"principal_id": c.PrincipalID}})
}
func (r *Repository) AddGroupMember(ctx context.Context, c drive.GroupMemberCommand) error {
	return r.groupMember(ctx, c, true)
}
func (r *Repository) RemoveGroupMember(ctx context.Context, c drive.GroupMemberCommand) error {
	return r.groupMember(ctx, c, false)
}
func (r *Repository) ListGroupMembers(_ context.Context, tenantID, groupID string) ([]drive.PrincipalRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	group, ok := r.groups[groupID]
	if !ok || group.TenantID != tenantID {
		return nil, drive.E(drive.CodeNotFound, "group was not found")
	}
	items := []drive.PrincipalRecord{}
	for key := range r.groupMembers {
		prefix := tenantID + "/" + groupID + "/"
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}
		if member, ok := r.members[memberKey(tenantID, key[len(prefix):])]; ok {
			items = append(items, member)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Identity.PrincipalID < items[j].Identity.PrincipalID })
	return items, nil
}
func (r *Repository) ListNodeACL(_ context.Context, t, n string) ([]drive.NodeACLEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []drive.NodeACLEntry{}
	for _, a := range r.acls {
		if a.TenantID == t && a.NodeID == n {
			out = append(out, a)
		}
	}
	return out, nil
}
func (r *Repository) SetNodeACL(ctx context.Context, c drive.SetNodeACLCommand) (drive.NodeACLEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.canManageACLLocked(c.TenantID, c.ActorPrincipalID, c.ActorRole, c.NodeID) {
		return drive.NodeACLEntry{}, drive.E(drive.CodeForbidden, "only owners and admins may manage ACLs")
	}
	if !drive.ValidACLSubjectType(c.SubjectType) || !drive.ValidACLRole(c.Role) {
		return drive.NodeACLEntry{}, drive.E(drive.CodeInvalidRequest, "ACL is invalid")
	}
	if _, ok := r.activeNode(c.TenantID, c.NodeID); !ok {
		return drive.NodeACLEntry{}, drive.E(drive.CodeNotFound, "node was not found")
	}
	if c.SubjectType == drive.ACLSubjectPrincipal {
		if _, ok := r.members[memberKey(c.TenantID, c.SubjectID)]; !ok {
			return drive.NodeACLEntry{}, drive.E(drive.CodeNotFound, "member was not found")
		}
	} else {
		g, ok := r.groups[c.SubjectID]
		if !ok || g.TenantID != c.TenantID {
			return drive.NodeACLEntry{}, drive.E(drive.CodeNotFound, "group was not found")
		}
	}
	for id, a := range r.acls {
		if a.TenantID == c.TenantID && a.NodeID == c.NodeID && a.SubjectType == c.SubjectType && a.SubjectID == c.SubjectID {
			a.Role = c.Role
			a.UpdatedAt = c.Now
			r.acls[id] = a
			if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "node.acl.set", TargetType: "node_acl", TargetID: a.ID, OccurredAt: c.Now, Metadata: map[string]string{"node_id": c.NodeID, "role": string(c.Role), "subject_type": string(c.SubjectType), "subject_id": c.SubjectID}}); err != nil {
				return drive.NodeACLEntry{}, err
			}
			return a, nil
		}
	}
	a := drive.NodeACLEntry{ID: c.ID, TenantID: c.TenantID, NodeID: c.NodeID, SubjectType: c.SubjectType, SubjectID: c.SubjectID, Role: c.Role, CreatedBy: c.ActorPrincipalID, CreatedAt: c.Now, UpdatedAt: c.Now}
	r.acls[a.ID] = a
	if err := r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "node.acl.set", TargetType: "node_acl", TargetID: a.ID, OccurredAt: c.Now, Metadata: map[string]string{"node_id": c.NodeID, "role": string(c.Role), "subject_type": string(c.SubjectType), "subject_id": c.SubjectID}}); err != nil {
		delete(r.acls, a.ID)
		return drive.NodeACLEntry{}, err
	}
	return a, nil
}
func (r *Repository) DeleteNodeACL(ctx context.Context, c drive.DeleteNodeACLCommand) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.canManageACLLocked(c.TenantID, c.ActorPrincipalID, c.ActorRole, c.NodeID) {
		return drive.E(drive.CodeForbidden, "only owners and admins may manage ACLs")
	}
	a, ok := r.acls[c.EntryID]
	if !ok || a.TenantID != c.TenantID || a.NodeID != c.NodeID {
		return drive.E(drive.CodeNotFound, "ACL entry was not found")
	}
	delete(r.acls, c.EntryID)
	return r.appendAuditLocked(ctx, drive.AuditEvent{TenantID: c.TenantID, ActorPrincipalID: c.ActorPrincipalID, Action: "node.acl.deleted", TargetType: "node_acl", TargetID: c.EntryID, OccurredAt: c.Now, Metadata: map[string]string{"node_id": c.NodeID}})
}
func (r *Repository) AuthorizeNode(_ context.Context, i drive.Identity, n string, cap drive.NodeCapability) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authorizeNodeLocked(i, n, cap)
}

func (r *Repository) canManageACLLocked(tenantID, principalID string, role drive.AccessRole, nodeID string) bool {
	member, ok := r.members[memberKey(tenantID, principalID)]
	if !ok || member.Status != drive.MemberStatusActive || member.Role != role {
		return false
	}
	if role == drive.RoleOwner || role == drive.RoleAdmin {
		return true
	}
	return r.authorizeNodeLocked(drive.Identity{TenantID: tenantID, PrincipalID: principalID}, nodeID, drive.NodeCapabilityManageACL) == nil
}

func (r *Repository) authorizeNodeLocked(i drive.Identity, n string, cap drive.NodeCapability) error {
	if !drive.ValidNodeCapability(cap) {
		return drive.E(drive.CodeInvalidRequest, "node capability is invalid")
	}
	node, ok := r.nodes[n]
	if !ok || node.TenantID != i.TenantID {
		return drive.E(drive.CodeNotFound, "node was not found")
	}
	m, ok := r.members[memberKey(i.TenantID, i.PrincipalID)]
	if !ok || m.Status != drive.MemberStatusActive {
		return drive.E(drive.CodeForbidden, "node access denied")
	}
	if m.Role == drive.RoleOwner || m.Role == drive.RoleAdmin {
		return nil
	}
	needed := map[drive.NodeCapability]int{drive.NodeCapabilityRead: 1, drive.NodeCapabilityWrite: 2, drive.NodeCapabilityDelete: 3, drive.NodeCapabilityManageACL: 3}[cap]
	if (m.Role == drive.RoleEditor && needed <= 2) || (m.Role == drive.RoleViewer && needed <= 1) {
		return nil
	}
	for cur := n; cur != ""; {
		for _, a := range r.acls {
			if a.TenantID == i.TenantID && a.NodeID == cur && (a.SubjectType == drive.ACLSubjectPrincipal && a.SubjectID == i.PrincipalID || a.SubjectType == drive.ACLSubjectGroup) {
				if a.SubjectType == drive.ACLSubjectGroup {
					if _, ok := r.groupMembers[groupMemberKey(i.TenantID, a.SubjectID, i.PrincipalID)]; !ok {
						continue
					}
				}
				level := map[drive.ACLRole]int{drive.ACLReader: 1, drive.ACLContributor: 2, drive.ACLManager: 3}[a.Role]
				if level >= needed {
					return nil
				}
			}
		}
		node, ok := r.nodes[cur]
		if !ok {
			break
		}
		cur = node.ParentID
	}
	return drive.E(drive.CodeForbidden, "node access denied")
}

func (r *Repository) AppendAudit(ctx context.Context, e drive.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendAuditLocked(ctx, e)
}

func (r *Repository) appendAuditLocked(ctx context.Context, e drive.AuditEvent) error {
	metadata, err := boundedAuditMetadata(e.Metadata)
	if err != nil {
		return err
	}
	if e.ID == "" {
		e.ID, err = drive.NewID()
		if err != nil {
			return drive.E(drive.CodeInternal, "could not generate audit event id", err)
		}
	}
	e.Metadata = metadata
	if e.RequestID == "" {
		e.RequestID = drive.RequestIDFromContext(ctx)
	}
	r.auditSequence++
	e.Sequence = r.auditSequence
	r.audit = append(r.audit, e)
	return nil
}

func boundedAuditMetadata(metadata map[string]string) (map[string]string, error) {
	if metadata == nil {
		return map[string]string{}, nil
	}
	copy := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if key == "" || len(key) > 128 || len(value) > 2048 {
			return nil, drive.E(drive.CodeInvalidRequest, "audit metadata is invalid")
		}
		copy[key] = value
	}
	encoded, err := json.Marshal(copy)
	if err != nil || len(encoded) > 4096 {
		return nil, drive.E(drive.CodeInvalidRequest, "audit metadata is invalid")
	}
	return copy, nil
}
func (r *Repository) ListAudit(_ context.Context, f drive.AuditFilter) ([]drive.AuditEvent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []drive.AuditEvent{}
	for _, e := range r.audit {
		if e.TenantID == f.TenantID && e.Sequence > f.AfterSequence && (f.From.IsZero() || !e.OccurredAt.Before(f.From)) && (f.Until.IsZero() || !e.OccurredAt.After(f.Until)) {
			out = append(out, e)
			if f.Limit > 0 && len(out) >= f.Limit {
				break
			}
		}
	}
	return out, nil
}
