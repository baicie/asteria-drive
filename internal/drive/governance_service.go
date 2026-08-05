package drive

import (
	"context"
	"time"
)

func (s *Service) CreateGroup(ctx context.Context, actor MemberActor, name string) (TenantGroup, error) {
	if err := validateGovernanceManager(actor); err != nil {
		return TenantGroup{}, err
	}
	display, normalized, err := NormalizeName(name)
	if err != nil {
		return TenantGroup{}, err
	}
	id, err := NewID()
	if err != nil {
		return TenantGroup{}, E(CodeInternal, "could not generate group id", err)
	}
	return s.repository.CreateGroup(ctx, CreateGroupCommand{
		ID: id, TenantID: actor.Identity.TenantID, ActorPrincipalID: actor.Identity.PrincipalID,
		ActorRole: actor.Role, DisplayName: display, NormalizedName: normalized, Now: s.clock.Now(),
	})
}

func (s *Service) ListGroups(ctx context.Context, actor MemberActor) ([]TenantGroup, error) {
	if err := validateGovernanceManager(actor); err != nil {
		return nil, err
	}
	return s.repository.ListGroups(ctx, actor.Identity.TenantID)
}

func (s *Service) UpdateGroup(ctx context.Context, actor MemberActor, groupID, name string) (TenantGroup, error) {
	if err := validateGovernanceManager(actor); err != nil {
		return TenantGroup{}, err
	}
	if err := validateID(groupID); err != nil {
		return TenantGroup{}, err
	}
	display, normalized, err := NormalizeName(name)
	if err != nil {
		return TenantGroup{}, err
	}
	return s.repository.UpdateGroup(ctx, UpdateGroupCommand{
		TenantID: actor.Identity.TenantID, GroupID: groupID,
		ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role,
		DisplayName: display, NormalizedName: normalized, Now: s.clock.Now(),
	})
}

func (s *Service) DeleteGroup(ctx context.Context, actor MemberActor, groupID string) error {
	if err := validateGovernanceManager(actor); err != nil {
		return err
	}
	if err := validateID(groupID); err != nil {
		return err
	}
	return s.repository.DeleteGroup(ctx, DeleteGroupCommand{
		TenantID: actor.Identity.TenantID, GroupID: groupID,
		ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now(),
	})
}

func (s *Service) AddGroupMember(ctx context.Context, actor MemberActor, groupID, principalID string) error {
	return s.changeGroupMember(ctx, actor, groupID, principalID, true)
}

func (s *Service) RemoveGroupMember(ctx context.Context, actor MemberActor, groupID, principalID string) error {
	return s.changeGroupMember(ctx, actor, groupID, principalID, false)
}

func (s *Service) changeGroupMember(ctx context.Context, actor MemberActor, groupID, principalID string, add bool) error {
	if err := validateGovernanceManager(actor); err != nil {
		return err
	}
	if err := validateID(groupID); err != nil {
		return err
	}
	if err := validateID(principalID); err != nil {
		return err
	}
	command := GroupMemberCommand{
		TenantID: actor.Identity.TenantID, GroupID: groupID, PrincipalID: principalID,
		ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now(),
	}
	if add {
		return s.repository.AddGroupMember(ctx, command)
	}
	return s.repository.RemoveGroupMember(ctx, command)
}

func (s *Service) ListGroupMembers(ctx context.Context, actor MemberActor, groupID string) ([]PrincipalRecord, error) {
	if err := validateGovernanceManager(actor); err != nil {
		return nil, err
	}
	if err := validateID(groupID); err != nil {
		return nil, err
	}
	return s.repository.ListGroupMembers(ctx, actor.Identity.TenantID, groupID)
}

func (s *Service) AuthorizeNode(ctx context.Context, identity Identity, nodeID string, capability NodeCapability) error {
	if err := validateID(identity.TenantID); err != nil {
		return err
	}
	if err := validateID(identity.PrincipalID); err != nil {
		return err
	}
	if err := validateID(nodeID); err != nil {
		return err
	}
	if !ValidNodeCapability(capability) {
		return E(CodeInvalidRequest, "node capability is invalid")
	}
	return s.repository.AuthorizeNode(ctx, identity, nodeID, capability)
}

func (s *Service) ListNodeACL(ctx context.Context, actor MemberActor, nodeID string) ([]NodeACLEntry, error) {
	if err := validateMemberActor(actor); err != nil {
		return nil, err
	}
	if err := s.AuthorizeNode(ctx, actor.Identity, nodeID, NodeCapabilityManageACL); err != nil {
		return nil, err
	}
	return s.repository.ListNodeACL(ctx, actor.Identity.TenantID, nodeID)
}

func (s *Service) SetNodeACL(ctx context.Context, actor MemberActor, nodeID string, subjectType ACLSubjectType, subjectID string, role ACLRole) (NodeACLEntry, error) {
	if err := validateMemberActor(actor); err != nil {
		return NodeACLEntry{}, err
	}
	if err := validateID(nodeID); err != nil {
		return NodeACLEntry{}, err
	}
	if err := validateID(subjectID); err != nil {
		return NodeACLEntry{}, err
	}
	if !ValidACLSubjectType(subjectType) || !ValidACLRole(role) {
		return NodeACLEntry{}, E(CodeInvalidRequest, "ACL subject type or role is invalid")
	}
	id, err := NewID()
	if err != nil {
		return NodeACLEntry{}, E(CodeInternal, "could not generate ACL id", err)
	}
	return s.repository.SetNodeACL(ctx, SetNodeACLCommand{
		ID: id, TenantID: actor.Identity.TenantID, NodeID: nodeID,
		SubjectType: subjectType, SubjectID: subjectID, Role: role,
		ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now(),
	})
}

func (s *Service) DeleteNodeACL(ctx context.Context, actor MemberActor, nodeID, entryID string) error {
	if err := validateMemberActor(actor); err != nil {
		return err
	}
	if err := validateID(nodeID); err != nil {
		return err
	}
	if err := validateID(entryID); err != nil {
		return err
	}
	return s.repository.DeleteNodeACL(ctx, DeleteNodeACLCommand{
		TenantID: actor.Identity.TenantID, NodeID: nodeID, EntryID: entryID,
		ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now(),
	})
}

type AuditPage struct {
	Items        []AuditEvent
	NextSequence int64
}

func (s *Service) ListAudit(ctx context.Context, actor MemberActor, afterSequence int64, from, until time.Time, limit int) (AuditPage, error) {
	if err := validateGovernanceManager(actor); err != nil {
		return AuditPage{}, err
	}
	if afterSequence < 0 || limit < 0 || limit > 1000 || !from.IsZero() && !until.IsZero() && !until.After(from) {
		return AuditPage{}, E(CodeInvalidRequest, "audit filter is invalid")
	}
	if !from.IsZero() && !until.IsZero() && until.Sub(from) > 31*24*time.Hour {
		return AuditPage{}, E(CodeInvalidRequest, "audit time range cannot exceed 31 days")
	}
	if limit == 0 {
		limit = 100
	}
	items, err := s.repository.ListAudit(ctx, AuditFilter{
		TenantID: actor.Identity.TenantID, AfterSequence: afterSequence,
		From: from, Until: until, Limit: limit,
	})
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Items: items}
	if len(items) == limit {
		page.NextSequence = items[len(items)-1].Sequence
	}
	return page, nil
}

func validateGovernanceManager(actor MemberActor) error {
	if err := validateMemberActor(actor); err != nil {
		return err
	}
	if actor.Role != RoleOwner && actor.Role != RoleAdmin {
		return E(CodeForbidden, "only owners and admins may manage tenant governance")
	}
	return nil
}
