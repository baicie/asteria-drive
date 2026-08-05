package drive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Clock interface {
	Now() time.Time
}

type InvitationOutput struct {
	Invitation TenantInvitation
	Token      string
}

func (s *Service) CreateInvitation(ctx context.Context, actor MemberActor, issuer, subject, displayName string, role AccessRole, expiresAt time.Time) (InvitationOutput, error) {
	if err := validateMemberActor(actor); err != nil {
		return InvitationOutput{}, err
	}
	if actor.Role != RoleOwner && actor.Role != RoleAdmin {
		return InvitationOutput{}, E(CodeForbidden, "only owners and admins may invite members")
	}
	if !validInvitationIdentity(issuer, subject) || len(displayName) > 256 || !ValidAccessRole(role) || !expiresAt.After(s.clock.Now()) {
		return InvitationOutput{}, E(CodeInvalidRequest, "invitation is invalid")
	}
	id, err := NewID()
	if err != nil {
		return InvitationOutput{}, E(CodeInternal, "could not generate invitation id", err)
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return InvitationOutput{}, E(CodeInternal, "could not generate invitation token", err)
	}
	token := hex.EncodeToString(b)
	digest := sha256.Sum256([]byte(token))
	v, err := s.repository.CreateInvitation(ctx, CreateInvitationCommand{ID: id, TenantID: actor.Identity.TenantID, ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Issuer: issuer, Subject: subject, DisplayName: displayName, Role: role, TokenHash: hex.EncodeToString(digest[:]), ExpiresAt: expiresAt, Now: s.clock.Now()})
	if err != nil {
		return InvitationOutput{}, err
	}
	return InvitationOutput{Invitation: v, Token: token}, nil
}
func (s *Service) ListInvitations(ctx context.Context, actor MemberActor, status InvitationStatus, limit int) ([]TenantInvitation, error) {
	if actor.Role != RoleOwner && actor.Role != RoleAdmin {
		return nil, E(CodeForbidden, "only owners and admins may list invitations")
	}
	if status != "" && !ValidInvitationStatus(status) {
		return nil, E(CodeInvalidRequest, "invitation status is invalid")
	}
	return s.repository.ListInvitations(ctx, actor.Identity.TenantID, status, limit)
}
func (s *Service) AcceptInvitation(ctx context.Context, token, issuer, subject string) (TenantInvitation, PrincipalRecord, error) {
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 || !validInvitationIdentity(issuer, subject) {
		return TenantInvitation{}, PrincipalRecord{}, E(CodeInvalidRequest, "invitation token and authenticated identity are required")
	}
	principalID, err := NewID()
	if err != nil {
		return TenantInvitation{}, PrincipalRecord{}, E(CodeInternal, "could not generate principal id", err)
	}
	d := sha256.Sum256([]byte(token))
	return s.repository.AcceptInvitation(ctx, AcceptInvitationCommand{
		TokenHash: hex.EncodeToString(d[:]), CandidatePrincipalID: principalID,
		Issuer: issuer, Subject: subject, Now: s.clock.Now(),
	})
}
func (s *Service) RevokeInvitation(ctx context.Context, actor MemberActor, id string) (TenantInvitation, error) {
	if err := validateMemberActor(actor); err != nil {
		return TenantInvitation{}, err
	}
	if err := validateID(id); err != nil {
		return TenantInvitation{}, err
	}
	return s.repository.RevokeInvitation(ctx, RevokeInvitationCommand{TenantID: actor.Identity.TenantID, InvitationID: id, ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now()})
}

func validInvitationIdentity(issuer, subject string) bool {
	return issuer != "" && subject != "" && strings.TrimSpace(issuer) != "" && strings.TrimSpace(subject) != "" && len(issuer) <= 512 && len(subject) <= 512
}
func (s *Service) DeleteMember(ctx context.Context, actor MemberActor, id string) error {
	if err := validateMemberActor(actor); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	return s.repository.DeleteMember(ctx, DeleteMemberCommand{TenantID: actor.Identity.TenantID, PrincipalID: id, ActorPrincipalID: actor.Identity.PrincipalID, ActorRole: actor.Role, Now: s.clock.Now()})
}

func validateMemberActor(actor MemberActor) error {
	if err := validateID(actor.Identity.TenantID); err != nil {
		return err
	}
	if err := validateID(actor.Identity.PrincipalID); err != nil {
		return err
	}
	if !ValidAccessRole(actor.Role) {
		return E(CodeInvalidRequest, "actor role is invalid")
	}
	return nil
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type ServiceOptions struct {
	Repository           Repository
	Storage              StorageProvider
	Cursor               *CursorCodec
	Clock                Clock
	MaxFileSize          int64
	PartSize             int64
	UploadTTL            time.Duration
	UploadSignTTL        time.Duration
	DownloadSignTTL      time.Duration
	IdempotencyClaimTTL  time.Duration
	IdempotencyRetention time.Duration
}

type Service struct {
	repository           Repository
	storage              StorageProvider
	cursor               *CursorCodec
	clock                Clock
	maxFileSize          int64
	partSize             int64
	uploadTTL            time.Duration
	uploadSignTTL        time.Duration
	downloadSignTTL      time.Duration
	idempotencyClaimTTL  time.Duration
	idempotencyRetention time.Duration
}

func NewService(options ServiceOptions) (*Service, error) {
	if options.Repository == nil || options.Storage == nil || options.Cursor == nil {
		return nil, E(CodeInvalidRequest, "repository, storage and cursor are required")
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.MaxFileSize <= 0 {
		options.MaxFileSize = 50 * 1024 * 1024 * 1024
	}
	if options.PartSize <= 0 {
		options.PartSize = 8 * 1024 * 1024
	}
	if options.UploadTTL <= 0 {
		options.UploadTTL = 24 * time.Hour
	}
	if options.UploadSignTTL <= 0 {
		options.UploadSignTTL = 15 * time.Minute
	}
	if options.DownloadSignTTL <= 0 {
		options.DownloadSignTTL = 15 * time.Minute
	}
	if options.IdempotencyClaimTTL <= 0 {
		options.IdempotencyClaimTTL = time.Minute
	}
	if options.IdempotencyRetention <= 0 {
		options.IdempotencyRetention = 24 * time.Hour
	}
	if options.IdempotencyRetention <= options.IdempotencyClaimTTL {
		return nil, E(CodeInvalidRequest, "idempotency retention must exceed the claim lease")
	}
	return &Service{
		repository: options.Repository, storage: options.Storage, cursor: options.Cursor,
		clock: options.Clock, maxFileSize: options.MaxFileSize, partSize: options.PartSize,
		uploadTTL: options.UploadTTL, uploadSignTTL: options.UploadSignTTL, downloadSignTTL: options.DownloadSignTTL,
		idempotencyClaimTTL: options.IdempotencyClaimTTL, idempotencyRetention: options.IdempotencyRetention,
	}, nil
}

func (s *Service) Ready(ctx context.Context) error {
	if err := s.repository.Ready(ctx); err != nil {
		return Retryable(CodeDependencyUnavailable, "metadata repository is unavailable", err)
	}
	if err := s.storage.Ready(ctx); err != nil {
		return Retryable(CodeDependencyUnavailable, "object storage is unavailable", err)
	}
	return nil
}

func (s *Service) EnsureTenant(ctx context.Context, tenantID, displayName string) (Tenant, error) {
	if err := validateID(tenantID); err != nil {
		return Tenant{}, err
	}
	rootID, err := NewID()
	if err != nil {
		return Tenant{}, E(CodeInternal, "could not generate root id", err)
	}
	return s.repository.EnsureTenant(ctx, TenantSeed{TenantID: tenantID, DisplayName: displayName, RootNodeID: rootID, Now: s.clock.Now()})
}

func (s *Service) Tenant(ctx context.Context, tenantID string) (Tenant, error) {
	if err := validateID(tenantID); err != nil {
		return Tenant{}, err
	}
	return s.repository.Tenant(ctx, tenantID)
}

type MemberActor struct {
	Identity Identity
	Role     AccessRole
}

func (s *Service) ListMembers(ctx context.Context, identity Identity, cursor string, limit int) (Page[PrincipalRecord], error) {
	if err := validateID(identity.TenantID); err != nil {
		return Page[PrincipalRecord]{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return Page[PrincipalRecord]{}, E(CodeInvalidRequest, "limit must be between 1 and 200")
	}
	position, err := s.cursor.Decode(cursor, identity.TenantID, "members")
	if err != nil {
		return Page[PrincipalRecord]{}, err
	}
	items, more, err := s.repository.ListMembers(ctx, identity.TenantID, position, limit)
	if err != nil {
		return Page[PrincipalRecord]{}, err
	}
	page := Page[PrincipalRecord]{Items: items}
	if more && len(items) > 0 {
		last := items[len(items)-1]
		page.NextCursor, err = s.cursor.Encode(identity.TenantID, "members", CursorPosition{Name: last.Identity.PrincipalID, ID: last.Identity.PrincipalID})
	}
	return page, err
}

func (s *Service) UpdateMember(ctx context.Context, actor MemberActor, principalID string, role *AccessRole, status *MemberStatus) (PrincipalRecord, error) {
	if err := validateID(actor.Identity.TenantID); err != nil {
		return PrincipalRecord{}, err
	}
	if err := validateID(actor.Identity.PrincipalID); err != nil {
		return PrincipalRecord{}, err
	}
	if err := validateID(principalID); err != nil {
		return PrincipalRecord{}, err
	}
	if actor.Role != RoleOwner && actor.Role != RoleAdmin {
		return PrincipalRecord{}, E(CodeForbidden, "only owners and admins may manage members")
	}
	if role == nil && status == nil {
		return PrincipalRecord{}, E(CodeInvalidRequest, "at least one member field is required")
	}
	if role != nil && !ValidAccessRole(*role) {
		return PrincipalRecord{}, E(CodeInvalidRequest, "member role is invalid")
	}
	if status != nil && !ValidMemberStatus(*status) {
		return PrincipalRecord{}, E(CodeInvalidRequest, "member status is invalid")
	}
	if actor.Role == RoleAdmin && role != nil && *role == RoleOwner {
		return PrincipalRecord{}, E(CodeForbidden, "admins cannot grant the owner role")
	}
	return s.repository.UpdateMember(ctx, UpdateMemberCommand{
		TenantID: actor.Identity.TenantID, PrincipalID: principalID, ActorPrincipalID: actor.Identity.PrincipalID,
		ActorRole: actor.Role, Role: role, Status: status, Now: s.clock.Now(),
	})
}

func (s *Service) CreateDirectory(ctx context.Context, identity Identity, parentID, name string) (Node, error) {
	output, err := s.CreateDirectoryWithIdempotency(ctx, identity, parentID, name, "")
	return output.Node, err
}

type CreateDirectoryOutput struct {
	Node     Node
	Replayed bool
}

func (s *Service) CreateDirectoryWithIdempotency(ctx context.Context, identity Identity, parentID, name, key string) (CreateDirectoryOutput, error) {
	if err := validateID(parentID); err != nil {
		return CreateDirectoryOutput{}, err
	}
	display, normalized, err := NormalizeName(name)
	if err != nil {
		return CreateDirectoryOutput{}, err
	}
	id, err := NewID()
	if err != nil {
		return CreateDirectoryOutput{}, E(CodeInternal, "could not generate node id", err)
	}
	requestDigest, err := canonicalRequestDigest(struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}{ParentID: parentID, Name: display})
	if err != nil {
		return CreateDirectoryOutput{}, err
	}
	claim, record, err := s.claimCreation(ctx, identity, IdempotencyCreateDirectory, key, requestDigest)
	if err != nil {
		return CreateDirectoryOutput{}, err
	}
	if record.State == IdempotencyCompleted {
		node, replayErr := s.repository.Node(ctx, identity, record.ResourceID, true)
		if replayErr != nil || node.Kind != NodeDirectory {
			return CreateDirectoryOutput{}, E(CodeInternal, "completed idempotency claim references a missing directory", replayErr)
		}
		return CreateDirectoryOutput{Node: node, Replayed: true}, nil
	}
	node, err := s.repository.CreateDirectory(ctx, CreateDirectoryCommand{
		Identity: identity, ID: id, ParentID: parentID, DisplayName: display, NormalizedName: normalized, Now: s.clock.Now(),
		Idempotency: claim,
	})
	if err != nil {
		s.releaseKnownFailedClaim(ctx, claim, err)
		return CreateDirectoryOutput{}, err
	}
	return CreateDirectoryOutput{Node: node}, nil
}

func (s *Service) Node(ctx context.Context, identity Identity, id string, kind NodeKind) (Node, error) {
	if err := validateID(id); err != nil {
		return Node{}, err
	}
	node, err := s.repository.Node(ctx, identity, id, false)
	if err != nil {
		return Node{}, err
	}
	if node.Kind != kind || node.Status != NodeActive || node.TrashedRootID != "" {
		return Node{}, E(CodeNotFound, "resource was not found")
	}
	return node, nil
}

func (s *Service) ListChildren(ctx context.Context, identity Identity, parentID, cursor string, limit int) (Page[Node], error) {
	if err := validateID(parentID); err != nil {
		return Page[Node]{}, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return Page[Node]{}, E(CodeInvalidRequest, "limit must be between 1 and 200")
	}
	position, err := s.cursor.Decode(cursor, identity.TenantID, "children:"+parentID)
	if err != nil {
		return Page[Node]{}, err
	}
	items, more, err := s.repository.ListChildren(ctx, identity, parentID, position, limit)
	if err != nil {
		return Page[Node]{}, err
	}
	page := Page[Node]{Items: items}
	if more && len(items) > 0 {
		last := items[len(items)-1]
		page.NextCursor, err = s.cursor.Encode(identity.TenantID, "children:"+parentID, CursorPosition{Name: last.NormalizedName, ID: last.ID})
	}
	return page, err
}

func (s *Service) UpdateNode(ctx context.Context, identity Identity, nodeID string, name, parentID *string, revision int64) (Node, error) {
	if revision <= 0 || (name == nil && parentID == nil) {
		return Node{}, E(CodeInvalidRequest, "revision and at least one update are required")
	}
	if err := validateID(nodeID); err != nil {
		return Node{}, err
	}
	if parentID != nil {
		if err := validateID(*parentID); err != nil {
			return Node{}, err
		}
	}
	var display, normalized *string
	if name != nil {
		d, n, err := NormalizeName(*name)
		if err != nil {
			return Node{}, err
		}
		display, normalized = &d, &n
	}
	return s.repository.UpdateNode(ctx, UpdateNodeCommand{
		Identity: identity, NodeID: nodeID, DisplayName: display, NormalizedName: normalized,
		ParentID: parentID, ExpectedRevision: revision, Now: s.clock.Now(),
	})
}

type CreateUploadInput struct {
	ParentID string
	Name     string
	Size     int64
	MimeType string
	Checksum Checksum
}

func (s *Service) CreateUpload(ctx context.Context, identity Identity, input CreateUploadInput) (UploadSession, error) {
	output, err := s.CreateUploadWithIdempotency(ctx, identity, input, "")
	return output.Upload, err
}

type CreateUploadOutput struct {
	Upload   UploadSession
	Replayed bool
}

func (s *Service) CreateUploadWithIdempotency(ctx context.Context, identity Identity, input CreateUploadInput, key string) (CreateUploadOutput, error) {
	if err := validateID(input.ParentID); err != nil {
		return CreateUploadOutput{}, err
	}
	display, normalized, err := NormalizeName(input.Name)
	if err != nil {
		return CreateUploadOutput{}, err
	}
	if input.Size <= 0 || input.Size > s.maxFileSize {
		return CreateUploadOutput{}, E(CodeInvalidRequest, "file size is outside the allowed range")
	}
	if input.MimeType == "" || len(input.MimeType) > 255 || !ValidMediaType(input.MimeType) || !ValidChecksum(input.Checksum) {
		return CreateUploadOutput{}, E(CodeInvalidRequest, "mime type or checksum is invalid")
	}
	parent, err := s.repository.Node(ctx, identity, input.ParentID, false)
	if err != nil || parent.Kind != NodeDirectory || parent.Status != NodeActive || parent.TrashedRootID != "" {
		return CreateUploadOutput{}, E(CodeNotFound, "parent directory was not found")
	}
	sessionID, err := NewID()
	if err != nil {
		return CreateUploadOutput{}, E(CodeInternal, "could not generate upload id", err)
	}
	blobID, err := NewID()
	if err != nil {
		return CreateUploadOutput{}, E(CodeInternal, "could not generate blob id", err)
	}
	requestDigest, err := canonicalRequestDigest(struct {
		ParentID string   `json:"parent_id"`
		Name     string   `json:"name"`
		Size     int64    `json:"size"`
		MimeType string   `json:"mime_type"`
		Checksum Checksum `json:"checksum"`
	}{ParentID: input.ParentID, Name: display, Size: input.Size, MimeType: input.MimeType, Checksum: input.Checksum})
	if err != nil {
		return CreateUploadOutput{}, err
	}
	claim, record, err := s.claimCreation(ctx, identity, IdempotencyCreateUpload, key, requestDigest)
	if err != nil {
		return CreateUploadOutput{}, err
	}
	if record.State == IdempotencyCompleted {
		upload, replayErr := s.repository.Upload(ctx, identity, record.ResourceID)
		if replayErr != nil {
			return CreateUploadOutput{}, E(CodeInternal, "completed idempotency claim references a missing upload", replayErr)
		}
		return CreateUploadOutput{Upload: upload, Replayed: true}, nil
	}
	objectKey := fmt.Sprintf("blobs/%s/%s", identity.TenantID, blobID)
	storageUploadID, err := s.storage.CreateMultipart(ctx, objectKey, input.MimeType, input.Checksum)
	if err != nil {
		mapped := mapStorageError(err)
		s.releaseKnownFailedClaim(ctx, claim, mapped)
		return CreateUploadOutput{}, mapped
	}
	now := s.clock.Now()
	session := UploadSession{
		ID: sessionID, TenantID: identity.TenantID, PrincipalID: identity.PrincipalID, ParentID: input.ParentID,
		DisplayName: display, NormalizedName: normalized, ExpectedSize: input.Size, MimeType: input.MimeType,
		DeclaredChecksum: input.Checksum, Bucket: s.storage.Bucket(), ObjectKey: objectKey,
		StorageUploadID: storageUploadID, Status: UploadCreated, PartSize: s.partSize,
		ExpiresAt: now.Add(s.uploadTTL), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, err := s.repository.CreateUpload(ctx, CreateUploadCommand{Session: session, Idempotency: claim})
	if err != nil {
		if claim == nil || knownFailedCreation(err) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = s.storage.AbortMultipart(cleanupCtx, objectKey, storageUploadID)
			s.releaseKnownFailedClaim(ctx, claim, err)
		}
		return CreateUploadOutput{}, err
	}
	return CreateUploadOutput{Upload: created}, nil
}

func (s *Service) Upload(ctx context.Context, identity Identity, uploadID string) (UploadSession, error) {
	if err := validateID(uploadID); err != nil {
		return UploadSession{}, err
	}
	return s.repository.Upload(ctx, identity, uploadID)
}

func (s *Service) SignUploadPart(ctx context.Context, identity Identity, uploadID string, partNumber int, checksum Checksum) (SignedPart, error) {
	if err := validateID(uploadID); err != nil {
		return SignedPart{}, err
	}
	if partNumber < 1 || partNumber > 10000 || !ValidChecksum(checksum) {
		return SignedPart{}, E(CodeInvalidRequest, "part number or checksum is invalid")
	}
	session, err := s.repository.Upload(ctx, identity, uploadID)
	if err != nil {
		return SignedPart{}, err
	}
	if !s.clock.Now().Before(session.ExpiresAt) {
		if _, err := s.repository.AbortUpload(ctx, identity, uploadID, UploadExpired, s.clock.Now()); err != nil {
			return SignedPart{}, err
		}
		if err := s.abortMultipart(ctx, session); err != nil {
			return SignedPart{}, err
		}
		return SignedPart{}, E(CodeInvalidState, "upload session has expired")
	}
	if session.Status != UploadCreated && session.Status != UploadUploading {
		return SignedPart{}, E(CodeInvalidState, "upload session cannot sign parts")
	}
	if session.Status == UploadCreated {
		if session, err = s.repository.MarkUploading(ctx, identity, uploadID, s.clock.Now()); err != nil {
			return SignedPart{}, err
		}
	}
	signed, err := s.storage.SignUploadPart(ctx, session.ObjectKey, session.StorageUploadID, partNumber, checksum, s.uploadSignTTL)
	if err != nil {
		return SignedPart{}, mapStorageError(err)
	}
	return signed, nil
}

type CompleteOutput struct {
	Upload UploadSession
	Node   Node
	First  bool
}

func (s *Service) CompleteUpload(ctx context.Context, identity Identity, uploadID string, parts []CompletedPart) (CompleteOutput, error) {
	if err := validateID(uploadID); err != nil {
		return CompleteOutput{}, err
	}
	if err := validateParts(parts); err != nil {
		return CompleteOutput{}, err
	}
	digest := partsDigest(parts)
	session, err := s.repository.BeginComplete(ctx, identity, uploadID, digest, parts, s.clock.Now())
	if err != nil {
		return CompleteOutput{}, err
	}
	if session.Status == UploadCommitted {
		result, _, err := s.repository.CommitUpload(ctx, CommitUploadCommand{
			Identity: identity, SessionID: uploadID, Digest: digest,
		})
		return CompleteOutput{Upload: result.Upload, Node: result.Node, First: false}, err
	}

	var object ObjectInfo
	if session.Status == UploadObjectCompleted {
		object, err = s.storage.StatObject(ctx, session.ObjectKey)
	} else {
		_, completeErr := s.storage.CompleteMultipart(ctx, session.ObjectKey, session.StorageUploadID, parts)
		var statErr error
		object, statErr = s.storage.StatObject(ctx, session.ObjectKey)
		if statErr != nil {
			mappedStat := mapStorageError(statErr)
			if completeErr == nil {
				return CompleteOutput{}, Retryable(CodeDependencyUnavailable, "completed object is not yet readable", statErr)
			}
			mappedComplete := mapStorageError(completeErr)
			if CodeOf(mappedComplete) == CodeInvalidRequest && CodeOf(mappedStat) == CodeNotFound {
				if failErr := s.failUploadCompletion(ctx, identity, session, digest, "storage_rejected", false); failErr != nil {
					return CompleteOutput{}, failErr
				}
				return CompleteOutput{}, mappedComplete
			}
			if CodeOf(mappedStat) == CodeDependencyUnavailable {
				return CompleteOutput{}, mappedStat
			}
			if CodeOf(mappedComplete) == CodeNotFound && CodeOf(mappedStat) == CodeNotFound {
				return CompleteOutput{}, Retryable(CodeDependencyUnavailable, "multipart completion result is not yet observable", completeErr)
			}
			if CodeOf(mappedComplete) == CodeDependencyUnavailable {
				return CompleteOutput{}, mappedComplete
			}
			return CompleteOutput{}, Retryable(CodeDependencyUnavailable, "multipart completion result is unknown", completeErr)
		}
	}
	if err != nil {
		return CompleteOutput{}, mapStorageError(err)
	}
	if err := normalizeObjectChecksum(&object, session.DeclaredChecksum); err != nil {
		if CodeOf(err) == CodeInvalidRequest {
			if failErr := s.failUploadCompletion(ctx, identity, session, digest, UploadFailureChecksumMismatch, true); failErr != nil {
				return CompleteOutput{}, failErr
			}
		}
		return CompleteOutput{}, err
	}
	if object.Size != session.ExpectedSize {
		if failErr := s.failUploadCompletion(ctx, identity, session, digest, UploadFailureSizeMismatch, true); failErr != nil {
			return CompleteOutput{}, failErr
		}
		return CompleteOutput{}, E(CodeInvalidRequest, "completed object size does not match the declared size")
	}
	if session.Status != UploadObjectCompleted {
		if session, err = s.repository.MarkObjectCompleted(ctx, identity, uploadID, object, s.clock.Now()); err != nil {
			return CompleteOutput{}, err
		}
	}
	blobID, err := NewID()
	if err != nil {
		return CompleteOutput{}, E(CodeInternal, "could not generate blob id", err)
	}
	nodeID, err := NewID()
	if err != nil {
		return CompleteOutput{}, E(CodeInternal, "could not generate node id", err)
	}
	versionID, err := NewID()
	if err != nil {
		return CompleteOutput{}, E(CodeInternal, "could not generate version id", err)
	}
	now := s.clock.Now()
	blob := Blob{
		ID: blobID, TenantID: identity.TenantID, Bucket: object.Bucket, ObjectKey: object.ObjectKey,
		Size: object.Size, MimeType: session.MimeType, Checksum: object.Checksum,
		ChecksumStatus: object.ChecksumStatus, Status: BlobAvailable, ReferenceCount: 1, CreatedAt: now,
	}
	version := FileVersion{
		ID: versionID, TenantID: identity.TenantID, NodeID: nodeID, BlobID: blobID,
		Size: object.Size, MimeType: session.MimeType, Checksum: object.Checksum,
		CreatedBy: identity.PrincipalID, CreatedAt: now,
	}
	node := Node{
		ID: nodeID, TenantID: identity.TenantID, ParentID: session.ParentID, Kind: NodeFile,
		DisplayName: session.DisplayName, NormalizedName: session.NormalizedName, CurrentVersionID: versionID,
		Size: object.Size, MimeType: session.MimeType, Status: NodeActive, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	result, first, err := s.repository.CommitUpload(ctx, CommitUploadCommand{
		Identity: identity, SessionID: uploadID, Digest: digest, Blob: blob, Version: version, Node: node, Parts: parts, Now: now,
	})
	if err != nil {
		switch CodeOf(err) {
		case CodeNameConflict, CodeNotFound:
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cleanupErr := s.storage.DeleteObject(cleanupCtx, session.ObjectKey); cleanupErr == nil {
				_ = s.repository.MarkUploadCleanupComplete(cleanupCtx, identity, session.ID, s.clock.Now())
			}
		}
		return CompleteOutput{}, err
	}
	return CompleteOutput{Upload: result.Upload, Node: result.Node, First: first}, nil
}

func (s *Service) failUploadCompletion(ctx context.Context, identity Identity, session UploadSession, digest, failureCode string, objectExists bool) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.repository.FailUploadCompletion(cleanupCtx, identity, session.ID, digest, failureCode, s.clock.Now()); err != nil {
		return err
	}
	if objectExists {
		if err := s.storage.DeleteObject(cleanupCtx, session.ObjectKey); err == nil {
			_ = s.repository.MarkUploadCleanupComplete(cleanupCtx, identity, session.ID, s.clock.Now())
		}
	} else {
		if err := s.storage.AbortMultipart(cleanupCtx, session.ObjectKey, session.StorageUploadID); err == nil {
			_ = s.repository.MarkUploadCleanupComplete(cleanupCtx, identity, session.ID, s.clock.Now())
		}
	}
	return nil
}

func (s *Service) AbortUpload(ctx context.Context, identity Identity, uploadID string) error {
	if err := validateID(uploadID); err != nil {
		return err
	}
	session, err := s.repository.AbortUpload(ctx, identity, uploadID, UploadAborted, s.clock.Now())
	if err != nil {
		return err
	}
	if session.Status == UploadFailed && failedUploadHasObject(session.FailureCode) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.storage.DeleteObject(cleanupCtx, session.ObjectKey); err != nil {
			return mapStorageError(err)
		}
		_ = s.repository.MarkUploadCleanupComplete(cleanupCtx, identity, session.ID, s.clock.Now())
		return nil
	}
	if err := s.abortMultipart(ctx, session); err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = s.repository.MarkUploadCleanupComplete(cleanupCtx, identity, session.ID, s.clock.Now())
	return nil
}

func (s *Service) abortMultipart(ctx context.Context, session UploadSession) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.storage.AbortMultipart(cleanupCtx, session.ObjectKey, session.StorageUploadID); err != nil {
		return mapStorageError(err)
	}
	return nil
}

func (s *Service) Download(ctx context.Context, identity Identity, nodeID string) (SignedDownload, error) {
	if err := validateID(nodeID); err != nil {
		return SignedDownload{}, err
	}
	node, blob, err := s.repository.DownloadBlob(ctx, identity, nodeID)
	if err != nil {
		return SignedDownload{}, err
	}
	signed, err := s.storage.SignDownload(ctx, blob.ObjectKey, node.DisplayName, s.downloadSignTTL)
	if err != nil {
		return SignedDownload{}, mapStorageError(err)
	}
	return signed, nil
}

func (s *Service) Recycle(ctx context.Context, identity Identity, nodeID string, revision int64) error {
	if revision <= 0 {
		return E(CodeInvalidRequest, "revision is required")
	}
	if err := validateID(nodeID); err != nil {
		return err
	}
	return s.repository.Recycle(ctx, identity, nodeID, revision, s.clock.Now())
}

func (s *Service) ListRecycle(ctx context.Context, identity Identity, cursor string, limit int) (Page[RecycleEntry], error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return Page[RecycleEntry]{}, E(CodeInvalidRequest, "limit must be between 1 and 200")
	}
	position, err := s.cursor.Decode(cursor, identity.TenantID, "recycle")
	if err != nil {
		return Page[RecycleEntry]{}, err
	}
	items, more, err := s.repository.ListRecycle(ctx, identity, position, limit)
	if err != nil {
		return Page[RecycleEntry]{}, err
	}
	page := Page[RecycleEntry]{Items: items}
	if more && len(items) > 0 {
		last := items[len(items)-1].Node
		page.NextCursor, err = s.cursor.Encode(identity.TenantID, "recycle", CursorPosition{Name: last.NormalizedName, ID: last.ID})
	}
	return page, err
}

func (s *Service) Restore(ctx context.Context, identity Identity, nodeID string, revision int64) (Node, error) {
	if revision <= 0 {
		return Node{}, E(CodeInvalidRequest, "revision is required")
	}
	if err := validateID(nodeID); err != nil {
		return Node{}, err
	}
	return s.repository.Restore(ctx, identity, nodeID, revision, s.clock.Now())
}

func (s *Service) Purge(ctx context.Context, identity Identity, nodeID string, revision int64) error {
	if revision <= 0 {
		return E(CodeInvalidRequest, "revision is required")
	}
	if err := validateID(nodeID); err != nil {
		return err
	}
	plan, err := s.repository.PreparePurge(ctx, identity, nodeID, revision, s.clock.Now())
	if err != nil {
		return err
	}
	for _, blob := range plan.Blobs {
		if err := s.storage.DeleteObject(ctx, blob.ObjectKey); err != nil {
			return mapStorageError(err)
		}
	}
	if err := s.repository.FinishPurge(ctx, identity, plan, s.clock.Now()); err != nil {
		return err
	}
	return nil
}

func validateParts(parts []CompletedPart) error {
	if len(parts) == 0 || len(parts) > 10000 {
		return E(CodeInvalidRequest, "parts must contain between 1 and 10000 items")
	}
	previous := 0
	for _, part := range parts {
		if part.PartNumber <= previous || part.PartNumber > 10000 || part.ETag == "" || len(part.ETag) > 1024 || !ValidChecksum(part.Checksum) {
			return E(CodeInvalidRequest, "parts must be strictly ordered and valid")
		}
		previous = part.PartNumber
	}
	return nil
}

func validateID(value string) error {
	if !ValidID(value) {
		return E(CodeInvalidRequest, "identifier must be a valid UUID")
	}
	return nil
}

func failedUploadHasObject(failureCode string) bool {
	switch failureCode {
	case UploadFailureSizeMismatch, UploadFailureChecksumMismatch,
		UploadFailureParentUnavailable, UploadFailureNameConflict:
		return true
	default:
		return false
	}
}

func (s *Service) claimCreation(ctx context.Context, identity Identity, scope IdempotencyScope, key, requestDigest string) (*IdempotencyRequest, IdempotencyRecord, error) {
	if key == "" {
		return nil, IdempotencyRecord{}, nil
	}
	if err := ValidateIdempotencyKey(key); err != nil {
		return nil, IdempotencyRecord{}, err
	}
	claimToken, err := NewID()
	if err != nil {
		return nil, IdempotencyRecord{}, E(CodeInternal, "could not generate idempotency claim token", err)
	}
	now := s.clock.Now()
	request := &IdempotencyRequest{
		Identity: identity, Scope: scope, KeyHash: sha256Hex([]byte(key)), RequestDigest: requestDigest,
		ClaimToken: claimToken, LockedUntil: now.Add(s.idempotencyClaimTTL),
		ExpiresAt: now.Add(s.idempotencyRetention), Now: now,
	}
	record, err := s.repository.ClaimIdempotency(ctx, *request)
	if err != nil {
		return nil, IdempotencyRecord{}, err
	}
	if record.State == IdempotencyPending && record.ClaimToken != request.ClaimToken {
		return nil, IdempotencyRecord{}, E(CodeInternal, "repository returned an idempotency claim owned by another request")
	}
	return request, record, nil
}

func (s *Service) releaseKnownFailedClaim(ctx context.Context, claim *IdempotencyRequest, err error) {
	if claim == nil || !knownFailedCreation(err) {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = s.repository.ReleaseIdempotency(cleanupCtx, *claim)
}

func knownFailedCreation(err error) bool {
	switch CodeOf(err) {
	case CodeInvalidRequest, CodeForbidden, CodeNotFound, CodeNameConflict,
		CodeIdempotencyConflict, CodeInvalidState, CodeRestoreConflict,
		CodeRevisionMismatch, CodeRequestTooLarge, CodeUnsupportedMediaType:
		return true
	default:
		return false
	}
}

func canonicalRequestDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", E(CodeInternal, "could not encode canonical idempotency request", err)
	}
	return sha256Hex(encoded), nil
}

func sha256Hex(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func partsDigest(parts []CompletedPart) string {
	copyParts := append([]CompletedPart(nil), parts...)
	sort.Slice(copyParts, func(i, j int) bool { return copyParts[i].PartNumber < copyParts[j].PartNumber })
	encoded, _ := json.Marshal(copyParts)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func mapStorageError(err error) error {
	if err == nil {
		return nil
	}
	if CodeOf(err) != CodeInternal {
		return err
	}
	return Retryable(CodeDependencyUnavailable, "object storage operation failed", err)
}

func normalizeObjectChecksum(object *ObjectInfo, declared Checksum) error {
	switch object.ChecksumStatus {
	case ChecksumVerified:
		if object.Checksum.Algorithm == "" || !ValidChecksum(object.Checksum) {
			return Retryable(CodeDependencyUnavailable, "object storage returned an invalid verified checksum", nil)
		}
		if declared.Algorithm != "" && object.Checksum != declared {
			return E(CodeInvalidRequest, "completed object checksum does not match the declared checksum")
		}
	case ChecksumDeclared:
		if object.Checksum.Algorithm == "" || !ValidChecksum(object.Checksum) {
			return Retryable(CodeDependencyUnavailable, "object storage returned an invalid declared checksum", nil)
		}
		if declared.Algorithm != "" && object.Checksum != declared {
			return E(CodeInvalidRequest, "completed object checksum does not match the declared checksum")
		}
	case ChecksumUnavailable, "":
		if declared.Algorithm != "" {
			object.Checksum = declared
			object.ChecksumStatus = ChecksumDeclared
		} else {
			object.Checksum = Checksum{}
			object.ChecksumStatus = ChecksumUnavailable
		}
	default:
		return Retryable(CodeDependencyUnavailable, "object storage returned an unknown checksum status", nil)
	}
	return nil
}
