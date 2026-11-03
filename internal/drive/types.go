package drive

import "time"

type Identity struct {
	TenantID    string
	PrincipalID string
}

type AccessRole string

const (
	RoleOwner  AccessRole = "owner"
	RoleAdmin  AccessRole = "admin"
	RoleEditor AccessRole = "editor"
	RoleViewer AccessRole = "viewer"
)

type Permission string

const (
	PermissionTenantRead    Permission = "tenant:read"
	PermissionMembersRead   Permission = "tenant:members:read"
	PermissionMembersManage Permission = "tenant:members:manage"
	PermissionGroupsManage  Permission = "tenant:groups:manage"
	PermissionAuditRead     Permission = "tenant:audit:read"
	PermissionFilesRead     Permission = "files:read"
	PermissionFilesWrite    Permission = "files:write"
	PermissionFilesDelete   Permission = "files:delete"
)

func ValidAccessRole(role AccessRole) bool {
	switch role {
	case RoleOwner, RoleAdmin, RoleEditor, RoleViewer:
		return true
	default:
		return false
	}
}

func PermissionsForRole(role AccessRole) map[Permission]struct{} {
	permissions := make(map[Permission]struct{})
	switch role {
	case RoleOwner:
		permissions[PermissionTenantRead] = struct{}{}
		permissions[PermissionMembersRead] = struct{}{}
		permissions[PermissionMembersManage] = struct{}{}
		permissions[PermissionGroupsManage] = struct{}{}
		permissions[PermissionAuditRead] = struct{}{}
		permissions[PermissionFilesRead] = struct{}{}
		permissions[PermissionFilesWrite] = struct{}{}
		permissions[PermissionFilesDelete] = struct{}{}
	case RoleAdmin:
		permissions[PermissionTenantRead] = struct{}{}
		permissions[PermissionMembersRead] = struct{}{}
		permissions[PermissionMembersManage] = struct{}{}
		permissions[PermissionGroupsManage] = struct{}{}
		permissions[PermissionAuditRead] = struct{}{}
		permissions[PermissionFilesRead] = struct{}{}
		permissions[PermissionFilesWrite] = struct{}{}
		permissions[PermissionFilesDelete] = struct{}{}
	case RoleEditor:
		permissions[PermissionFilesRead] = struct{}{}
		permissions[PermissionFilesWrite] = struct{}{}
	case RoleViewer:
		permissions[PermissionFilesRead] = struct{}{}
	}
	return permissions
}

type MemberStatus string

const (
	MemberStatusActive    MemberStatus = "active"
	MemberStatusSuspended MemberStatus = "suspended"
)

func ValidMemberStatus(status MemberStatus) bool {
	return status == MemberStatusActive || status == MemberStatusSuspended
}

type PrincipalRecord struct {
	Identity          Identity
	Issuer            string
	Subject           string
	DisplayName       string
	TenantDisplayName string
	Role              AccessRole
	Status            MemberStatus
}

type OIDCMemberSeed struct {
	PrincipalID string
	TenantID    string
	Issuer      string
	Subject     string
	DisplayName string
	Role        AccessRole
	Now         time.Time
}

type UpdateMemberCommand struct {
	TenantID         string
	PrincipalID      string
	ActorPrincipalID string
	ActorRole        AccessRole
	Role             *AccessRole
	Status           *MemberStatus
	Now              time.Time
}

type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationRevoked  InvitationStatus = "revoked"
	InvitationExpired  InvitationStatus = "expired"
)

func ValidInvitationStatus(status InvitationStatus) bool {
	switch status {
	case InvitationPending, InvitationAccepted, InvitationRevoked, InvitationExpired:
		return true
	default:
		return false
	}
}

type TenantInvitation struct {
	ID                  string
	TenantID            string
	Issuer              string
	Subject             string
	DisplayName         string
	Role                AccessRole
	Status              InvitationStatus
	AcceptedPrincipalID string
	CreatedBy           string
	ExpiresAt           time.Time
	AcceptedAt          *time.Time
	RevokedAt           *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CreateInvitationCommand struct {
	ID               string
	TenantID         string
	ActorPrincipalID string
	ActorRole        AccessRole
	Issuer           string
	Subject          string
	DisplayName      string
	Role             AccessRole
	TokenHash        string
	ExpiresAt        time.Time
	Now              time.Time
}

type AcceptInvitationCommand struct {
	TokenHash            string
	CandidatePrincipalID string
	Issuer               string
	Subject              string
	Now                  time.Time
}

type RevokeInvitationCommand struct {
	TenantID         string
	InvitationID     string
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type DeleteMemberCommand struct {
	TenantID         string
	PrincipalID      string
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type TenantGroup struct {
	ID             string
	TenantID       string
	DisplayName    string
	NormalizedName string
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateGroupCommand struct {
	ID               string
	TenantID         string
	ActorPrincipalID string
	ActorRole        AccessRole
	DisplayName      string
	NormalizedName   string
	Now              time.Time
}

type UpdateGroupCommand struct {
	TenantID         string
	GroupID          string
	ActorPrincipalID string
	ActorRole        AccessRole
	DisplayName      string
	NormalizedName   string
	Now              time.Time
}

type DeleteGroupCommand struct {
	TenantID         string
	GroupID          string
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type GroupMemberCommand struct {
	TenantID         string
	GroupID          string
	PrincipalID      string
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type ACLSubjectType string

const (
	ACLSubjectPrincipal ACLSubjectType = "principal"
	ACLSubjectGroup     ACLSubjectType = "group"
)

func ValidACLSubjectType(subjectType ACLSubjectType) bool {
	return subjectType == ACLSubjectPrincipal || subjectType == ACLSubjectGroup
}

type ACLRole string

const (
	ACLReader      ACLRole = "reader"
	ACLContributor ACLRole = "contributor"
	ACLManager     ACLRole = "manager"
)

func ValidACLRole(role ACLRole) bool {
	return role == ACLReader || role == ACLContributor || role == ACLManager
}

type NodeCapability string

const (
	NodeCapabilityRead      NodeCapability = "read"
	NodeCapabilityWrite     NodeCapability = "write"
	NodeCapabilityDelete    NodeCapability = "delete"
	NodeCapabilityManageACL NodeCapability = "manage_acl"
)

func ValidNodeCapability(capability NodeCapability) bool {
	switch capability {
	case NodeCapabilityRead, NodeCapabilityWrite, NodeCapabilityDelete, NodeCapabilityManageACL:
		return true
	default:
		return false
	}
}

type NodeACLEntry struct {
	ID          string
	TenantID    string
	NodeID      string
	SubjectType ACLSubjectType
	SubjectID   string
	Role        ACLRole
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SetNodeACLCommand struct {
	ID               string
	TenantID         string
	NodeID           string
	SubjectType      ACLSubjectType
	SubjectID        string
	Role             ACLRole
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type DeleteNodeACLCommand struct {
	TenantID         string
	NodeID           string
	EntryID          string
	ActorPrincipalID string
	ActorRole        AccessRole
	Now              time.Time
}

type AuditEvent struct {
	Sequence         int64
	ID               string
	TenantID         string
	ActorPrincipalID string
	Action           string
	TargetType       string
	TargetID         string
	RequestID        string
	Metadata         map[string]string
	OccurredAt       time.Time
}

type AuditFilter struct {
	TenantID      string
	AfterSequence int64
	From          time.Time
	Until         time.Time
	Limit         int
}

type IdempotencyScope string

const (
	IdempotencyCreateDirectory IdempotencyScope = "create_directory"
	IdempotencyCreateUpload    IdempotencyScope = "create_upload"
)

func ValidIdempotencyScope(scope IdempotencyScope) bool {
	return scope == IdempotencyCreateDirectory || scope == IdempotencyCreateUpload
}

type IdempotencyState string

const (
	IdempotencyPending   IdempotencyState = "pending"
	IdempotencyCompleted IdempotencyState = "completed"
)

type IdempotencyRequest struct {
	Identity      Identity
	Scope         IdempotencyScope
	KeyHash       string
	RequestDigest string
	ClaimToken    string
	LockedUntil   time.Time
	ExpiresAt     time.Time
	Now           time.Time
}

type IdempotencyRecord struct {
	Identity      Identity
	Scope         IdempotencyScope
	KeyHash       string
	RequestDigest string
	State         IdempotencyState
	ClaimToken    string
	ResourceID    string
	LockedUntil   time.Time
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type UploadMaintenanceClaim struct {
	Upload         UploadSession
	Owner          string
	CleanupPending bool
	Attempts       int
}

type RecycleMaintenanceClaim struct {
	Identity Identity
	RootID   string
	Revision int64
	Owner    string
}

type Tenant struct {
	ID          string
	DisplayName string
	RootNodeID  string
	CreatedAt   time.Time
}

type NodeKind string

const (
	NodeDirectory NodeKind = "directory"
	NodeFile      NodeKind = "file"
)

type NodeStatus string

const (
	NodeActive  NodeStatus = "active"
	NodeTrashed NodeStatus = "trashed"
	NodePurging NodeStatus = "purging"
)

type Node struct {
	ID               string
	TenantID         string
	ParentID         string
	Kind             NodeKind
	DisplayName      string
	NormalizedName   string
	CurrentVersionID string
	Size             int64
	MimeType         string
	Status           NodeStatus
	TrashedRootID    string
	OriginalParentID string
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

type ChecksumStatus string

const (
	ChecksumVerified    ChecksumStatus = "verified"
	ChecksumDeclared    ChecksumStatus = "declared"
	ChecksumUnavailable ChecksumStatus = "unavailable"
)

type BlobStatus string

const (
	BlobAvailable     BlobStatus = "available"
	BlobPendingDelete BlobStatus = "pending_delete"
	BlobDeleted       BlobStatus = "deleted"
)

type Blob struct {
	ID             string
	TenantID       string
	Bucket         string
	ObjectKey      string
	Size           int64
	MimeType       string
	Checksum       Checksum
	ChecksumStatus ChecksumStatus
	Status         BlobStatus
	ReferenceCount int64
	CreatedAt      time.Time
	DeletedAt      *time.Time
}

type FileVersion struct {
	ID        string
	TenantID  string
	NodeID    string
	BlobID    string
	Size      int64
	MimeType  string
	Checksum  Checksum
	CreatedBy string
	CreatedAt time.Time
}

type UploadStatus string

// MaxUploadParts is the S3 multipart hard limit. It bounds both the completion payload
// and a part listing so a resumed client can never be told to download more parts than
// storage will accept in a single completion request.
const MaxUploadParts = 10000

const (
	UploadCreated         UploadStatus = "created"
	UploadUploading       UploadStatus = "uploading"
	UploadCompleting      UploadStatus = "completing"
	UploadObjectCompleted UploadStatus = "object_completed"
	UploadCommitted       UploadStatus = "committed"
	UploadAborted         UploadStatus = "aborted"
	UploadExpired         UploadStatus = "expired"
	UploadFailed          UploadStatus = "failed"
)

const (
	UploadFailureStorageRejected   = "storage_rejected"
	UploadFailureSizeMismatch      = "size_mismatch"
	UploadFailureChecksumMismatch  = "checksum_mismatch"
	UploadFailureParentUnavailable = "parent_unavailable"
	UploadFailureNameConflict      = "name_conflict"
)

func (s UploadStatus) Terminal() bool {
	return s == UploadCommitted || s == UploadAborted || s == UploadExpired || s == UploadFailed
}

type UploadSession struct {
	ID               string
	TenantID         string
	PrincipalID      string
	ParentID         string
	DisplayName      string
	NormalizedName   string
	ExpectedSize     int64
	MimeType         string
	DeclaredChecksum Checksum
	Bucket           string
	ObjectKey        string
	StorageUploadID  string
	Status           UploadStatus
	CompletionDigest string
	CommittedNodeID  string
	FailureCode      string
	PartSize         int64
	ExpiresAt        time.Time
	Revision         int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type CompletedPart struct {
	PartNumber int      `json:"part_number"`
	ETag       string   `json:"etag"`
	Checksum   Checksum `json:"checksum,omitempty"`
	Size       int64    `json:"size,omitempty"`
}

// PartSignature is the content identity a client declared when it asked for a part's
// signature. It is an audit record of what the client promised, not proof of what storage
// accepted: a signature can be issued and the PUT that was supposed to follow it can be
// interrupted. Resume therefore never reads it back (ADR-0028).
type PartSignature struct {
	PartNumber int
	Size       int64
	SHA256     string
}

// StoredPart is one part that object storage has durably accepted for a multipart
// upload. It is the read-only source for cross-reload resume (ADR-0027): it carries no
// credential and no signed URL, only the identity of a part that can be re-signed.
type StoredPart struct {
	PartNumber int
	ETag       string
	Size       int64
	// StorageChecksum is the checksum object storage itself reports for the bytes it
	// accepted, when the backend supports per-part checksums. It is the only evidence a
	// resumed client may use to skip a part: it comes from the stored object, not from a
	// client request that may never have been followed by a successful PUT (ADR-0028).
	StorageChecksum Checksum
	// Reusable reports whether this entry carries enough storage-attested evidence for a
	// resuming client to skip re-sending it. It is false for a part whose backend reports
	// no checksum, for a part number object storage returned more than once with different
	// identities, and for a part this service cannot describe exactly. False always means
	// "re-upload", which costs bandwidth but cannot splice another file's bytes.
	Reusable bool
}

// UploadPartListing is the merged view of the parts a resumed client already uploaded.
// The control plane and object storage are both consulted because they cover different
// windows: storage knows every accepted part, while the control plane only records parts
// after completion has been requested.
type UploadPartListing struct {
	UploadID string
	Status   UploadStatus
	PartSize int64
	Parts    []StoredPart
	// Complete reports whether the listing is the full set of parts for the declared
	// size. It is false when more than MaxUploadParts have been observed, in which case
	// the caller must not treat the listing as authoritative.
	Complete bool
}

type ObjectInfo struct {
	Bucket         string
	ObjectKey      string
	Size           int64
	ETag           string
	Checksum       Checksum
	ChecksumStatus ChecksumStatus
}

type CompleteResult struct {
	Upload  UploadSession
	Node    Node
	Blob    Blob
	Version FileVersion
}

type Page[T any] struct {
	Items      []T
	NextCursor string
}

type CursorPosition struct {
	Name string
	ID   string
}

type RecycleEntry struct {
	Node             Node
	OriginalParentID string
	DeletedAt        time.Time
}

type PurgePlan struct {
	RootID string
	Blobs  []Blob
}

type SignedPart struct {
	PartNumber      int
	Method          string
	URL             string
	RequiredHeaders map[string]string
	ExpiresAt       time.Time
}

type SignedDownload struct {
	Method    string
	URL       string
	ExpiresAt time.Time
}
