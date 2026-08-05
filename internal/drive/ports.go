package drive

import (
	"context"
	"time"
)

type TenantSeed struct {
	TenantID    string
	DisplayName string
	RootNodeID  string
	Now         time.Time
}

type CreateDirectoryCommand struct {
	Identity       Identity
	ID             string
	ParentID       string
	DisplayName    string
	NormalizedName string
	Now            time.Time
	Idempotency    *IdempotencyRequest
}

type UpdateNodeCommand struct {
	Identity         Identity
	NodeID           string
	DisplayName      *string
	NormalizedName   *string
	ParentID         *string
	ExpectedRevision int64
	Now              time.Time
}

type CreateUploadCommand struct {
	Session     UploadSession
	Idempotency *IdempotencyRequest
}

type CommitUploadCommand struct {
	Identity  Identity
	SessionID string
	Digest    string
	Blob      Blob
	Version   FileVersion
	Node      Node
	Parts     []CompletedPart
	Now       time.Time
}

type Repository interface {
	Ready(context.Context) error
	Close()

	EnsureTenant(context.Context, TenantSeed) (Tenant, error)
	Tenant(context.Context, string) (Tenant, error)
	EnsureOIDCMember(context.Context, OIDCMemberSeed) (PrincipalRecord, error)
	ResolveOIDCPrincipal(context.Context, string, string, string) (PrincipalRecord, error)
	SetOIDCMemberStatus(context.Context, string, string, MemberStatus) error
	ListMembers(context.Context, string, CursorPosition, int) ([]PrincipalRecord, bool, error)
	UpdateMember(context.Context, UpdateMemberCommand) (PrincipalRecord, error)
	DeleteMember(context.Context, DeleteMemberCommand) error
	CreateInvitation(context.Context, CreateInvitationCommand) (TenantInvitation, error)
	ListInvitations(context.Context, string, InvitationStatus, int) ([]TenantInvitation, error)
	AcceptInvitation(context.Context, AcceptInvitationCommand) (TenantInvitation, PrincipalRecord, error)
	RevokeInvitation(context.Context, RevokeInvitationCommand) (TenantInvitation, error)
	CreateGroup(context.Context, CreateGroupCommand) (TenantGroup, error)
	ListGroups(context.Context, string) ([]TenantGroup, error)
	UpdateGroup(context.Context, UpdateGroupCommand) (TenantGroup, error)
	DeleteGroup(context.Context, DeleteGroupCommand) error
	AddGroupMember(context.Context, GroupMemberCommand) error
	RemoveGroupMember(context.Context, GroupMemberCommand) error
	ListGroupMembers(context.Context, string, string) ([]PrincipalRecord, error)
	ListNodeACL(context.Context, string, string) ([]NodeACLEntry, error)
	SetNodeACL(context.Context, SetNodeACLCommand) (NodeACLEntry, error)
	DeleteNodeACL(context.Context, DeleteNodeACLCommand) error
	AuthorizeNode(context.Context, Identity, string, NodeCapability) error
	AppendAudit(context.Context, AuditEvent) error
	ListAudit(context.Context, AuditFilter) ([]AuditEvent, error)
	ClaimIdempotency(context.Context, IdempotencyRequest) (IdempotencyRecord, error)
	ReleaseIdempotency(context.Context, IdempotencyRequest) error
	CreateDirectory(context.Context, CreateDirectoryCommand) (Node, error)
	Node(context.Context, Identity, string, bool) (Node, error)
	ListChildren(context.Context, Identity, string, CursorPosition, int) ([]Node, bool, error)
	UpdateNode(context.Context, UpdateNodeCommand) (Node, error)

	CreateUpload(context.Context, CreateUploadCommand) (UploadSession, error)
	Upload(context.Context, Identity, string) (UploadSession, error)
	MarkUploading(context.Context, Identity, string, time.Time) (UploadSession, error)
	BeginComplete(context.Context, Identity, string, string, []CompletedPart, time.Time) (UploadSession, error)
	FailUploadCompletion(context.Context, Identity, string, string, string, time.Time) (UploadSession, error)
	MarkObjectCompleted(context.Context, Identity, string, ObjectInfo, time.Time) (UploadSession, error)
	CommitUpload(context.Context, CommitUploadCommand) (CompleteResult, bool, error)
	AbortUpload(context.Context, Identity, string, UploadStatus, time.Time) (UploadSession, error)
	MarkUploadCleanupComplete(context.Context, Identity, string, time.Time) error
	ExpiredUploads(context.Context, time.Time, int) ([]UploadSession, error)
	ClaimUploadsForMaintenance(context.Context, string, time.Time, time.Time, time.Time, int) ([]UploadMaintenanceClaim, error)
	FinishUploadMaintenance(context.Context, string, string, UploadStatus, bool, time.Time, string, time.Time) error
	ClaimRecycleForMaintenance(context.Context, string, time.Time, time.Time, time.Time, int) ([]RecycleMaintenanceClaim, error)
	ReleaseRecycleMaintenance(context.Context, string, string, time.Time, string, time.Time) error
	DeleteExpiredIdempotency(context.Context, time.Time, int) (int, error)

	DownloadBlob(context.Context, Identity, string) (Node, Blob, error)
	Recycle(context.Context, Identity, string, int64, time.Time) error
	ListRecycle(context.Context, Identity, CursorPosition, int) ([]RecycleEntry, bool, error)
	Restore(context.Context, Identity, string, int64, time.Time) (Node, error)
	PreparePurge(context.Context, Identity, string, int64, time.Time) (PurgePlan, error)
	FinishPurge(context.Context, Identity, PurgePlan, time.Time) error
}

type StorageProvider interface {
	Ready(context.Context) error
	Bucket() string
	CreateMultipart(context.Context, string, string, Checksum) (string, error)
	SignUploadPart(context.Context, string, string, int, Checksum, time.Duration) (SignedPart, error)
	CompleteMultipart(context.Context, string, string, []CompletedPart) (ObjectInfo, error)
	AbortMultipart(context.Context, string, string) error
	StatObject(context.Context, string) (ObjectInfo, error)
	SignDownload(context.Context, string, string, time.Duration) (SignedDownload, error)
	DeleteObject(context.Context, string) error
}
