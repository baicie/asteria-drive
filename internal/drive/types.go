package drive

import "time"

type Identity struct {
	TenantID    string
	PrincipalID string
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
