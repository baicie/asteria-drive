package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestInvitationAcceptanceIsIdentityBoundAndReplaySafe(t *testing.T) {
	repository, tenantID, ownerID, now := governanceFixture(t)
	invitationID := "00000000-0000-4000-8000-000000000010"
	created, err := repository.CreateInvitation(context.Background(), drive.CreateInvitationCommand{
		ID: invitationID, TenantID: tenantID, ActorPrincipalID: ownerID, ActorRole: drive.RoleOwner,
		Issuer: "https://issuer.example.test", Subject: "invitee", DisplayName: "Invitee", Role: drive.RoleViewer,
		TokenHash: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Hour), Now: now,
	})
	if err != nil || created.Status != drive.InvitationPending {
		t.Fatalf("create invitation: invitation=%+v err=%v", created, err)
	}
	if _, _, err := repository.AcceptInvitation(context.Background(), drive.AcceptInvitationCommand{
		TokenHash: strings.Repeat("a", 64), CandidatePrincipalID: "00000000-0000-4000-8000-000000000011",
		Issuer: "https://issuer.example.test", Subject: "another", Now: now.Add(time.Minute),
	}); drive.CodeOf(err) != drive.CodeForbidden {
		t.Fatalf("mismatched identity code=%s err=%v", drive.CodeOf(err), err)
	}
	accepted, member, err := repository.AcceptInvitation(context.Background(), drive.AcceptInvitationCommand{
		TokenHash: strings.Repeat("a", 64), CandidatePrincipalID: "00000000-0000-4000-8000-000000000011",
		Issuer: "https://issuer.example.test", Subject: "invitee", Now: now.Add(time.Minute),
	})
	if err != nil || accepted.Status != drive.InvitationAccepted || member.Identity.PrincipalID != accepted.AcceptedPrincipalID {
		t.Fatalf("accept invitation: invitation=%+v member=%+v err=%v", accepted, member, err)
	}
	replay, replayMember, err := repository.AcceptInvitation(context.Background(), drive.AcceptInvitationCommand{
		TokenHash: strings.Repeat("a", 64), CandidatePrincipalID: "00000000-0000-4000-8000-000000000012",
		Issuer: "https://issuer.example.test", Subject: "invitee", Now: now.Add(2 * time.Minute),
	})
	if err != nil || replay.ID != accepted.ID || replayMember.Identity.PrincipalID != member.Identity.PrincipalID {
		t.Fatalf("accept replay: invitation=%+v member=%+v err=%v", replay, replayMember, err)
	}
	events, err := repository.ListAudit(context.Background(), drive.AuditFilter{TenantID: tenantID, Limit: 10})
	if err != nil || len(events) != 2 || events[1].Action != "tenant.invitation.accepted" {
		t.Fatalf("invitation audit events=%+v err=%v", events, err)
	}
}

func TestManagerACLInheritsAndCrossTenantNodeIsNotFound(t *testing.T) {
	repository, tenantID, ownerID, now := governanceFixture(t)
	viewerID := "00000000-0000-4000-8000-000000000021"
	if _, err := repository.EnsureOIDCMember(context.Background(), drive.OIDCMemberSeed{PrincipalID: viewerID, TenantID: tenantID, Issuer: "https://issuer.example.test", Subject: "viewer", Role: drive.RoleViewer, Now: now}); err != nil {
		t.Fatal(err)
	}
	parent, err := repository.CreateDirectory(context.Background(), drive.CreateDirectoryCommand{Identity: drive.Identity{TenantID: tenantID, PrincipalID: ownerID}, ID: "00000000-0000-4000-8000-000000000022", ParentID: "root-governance", DisplayName: "parent", NormalizedName: "parent", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	child, err := repository.CreateDirectory(context.Background(), drive.CreateDirectoryCommand{Identity: drive.Identity{TenantID: tenantID, PrincipalID: ownerID}, ID: "00000000-0000-4000-8000-000000000023", ParentID: parent.ID, DisplayName: "child", NormalizedName: "child", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetNodeACL(context.Background(), drive.SetNodeACLCommand{ID: "00000000-0000-4000-8000-000000000024", TenantID: tenantID, NodeID: parent.ID, SubjectType: drive.ACLSubjectPrincipal, SubjectID: viewerID, Role: drive.ACLManager, ActorPrincipalID: ownerID, ActorRole: drive.RoleOwner, Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetNodeACL(context.Background(), drive.SetNodeACLCommand{ID: "00000000-0000-4000-8000-000000000025", TenantID: tenantID, NodeID: child.ID, SubjectType: drive.ACLSubjectPrincipal, SubjectID: ownerID, Role: drive.ACLReader, ActorPrincipalID: viewerID, ActorRole: drive.RoleViewer, Now: now}); err != nil {
		t.Fatalf("inherited manager should manage child ACL: %v", err)
	}
	if err := repository.AuthorizeNode(context.Background(), drive.Identity{TenantID: tenantID, PrincipalID: viewerID}, "00000000-0000-4000-8000-000000000026", drive.NodeCapabilityRead); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("missing node code=%s err=%v", drive.CodeOf(err), err)
	}
	if err := repository.AuthorizeNode(context.Background(), drive.Identity{TenantID: "00000000-0000-4000-8000-000000000027", PrincipalID: viewerID}, child.ID, drive.NodeCapabilityRead); drive.CodeOf(err) != drive.CodeNotFound {
		t.Fatalf("cross-tenant node code=%s err=%v", drive.CodeOf(err), err)
	}
}

func TestAuditMetadataIsNonNilBoundedAndPaginates(t *testing.T) {
	repository, tenantID, _, now := governanceFixture(t)
	for i := 0; i < 3; i++ {
		if err := repository.AppendAudit(context.Background(), drive.AuditEvent{ID: governanceID(40 + i), TenantID: tenantID, Action: "test.event", TargetType: "test", OccurredAt: now.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.ListAudit(context.Background(), drive.AuditFilter{TenantID: tenantID, Limit: 2})
	if err != nil || len(page) != 2 || page[0].Metadata == nil || page[0].Sequence >= page[1].Sequence {
		t.Fatalf("first audit page=%+v err=%v", page, err)
	}
	next, err := repository.ListAudit(context.Background(), drive.AuditFilter{TenantID: tenantID, AfterSequence: page[1].Sequence, Limit: 2})
	if err != nil || len(next) != 1 || next[0].Sequence <= page[1].Sequence {
		t.Fatalf("next audit page=%+v err=%v", next, err)
	}
	if err := repository.AppendAudit(context.Background(), drive.AuditEvent{ID: governanceID(50), TenantID: tenantID, Action: "test.event", TargetType: "test", Metadata: map[string]string{"x": strings.Repeat("x", 4097)}, OccurredAt: now}); drive.CodeOf(err) != drive.CodeInvalidRequest {
		t.Fatalf("oversized metadata code=%s err=%v", drive.CodeOf(err), err)
	}
}

func governanceFixture(t *testing.T) (*Repository, string, string, time.Time) {
	t.Helper()
	repository := NewRepository()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tenantID, ownerID := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
	if _, err := repository.EnsureTenant(context.Background(), drive.TenantSeed{TenantID: tenantID, DisplayName: "Governance", RootNodeID: "root-governance", Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureOIDCMember(context.Background(), drive.OIDCMemberSeed{PrincipalID: ownerID, TenantID: tenantID, Issuer: "https://issuer.example.test", Subject: "owner", Role: drive.RoleOwner, Now: now}); err != nil {
		t.Fatal(err)
	}
	return repository, tenantID, ownerID, now
}

func governanceID(number int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", number)
}
