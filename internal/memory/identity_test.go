package memory

import (
	"context"
	"testing"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

func TestOIDCMemberBootstrapIsIdempotentAndTenantScoped(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	tenantA := "11111111-1111-4111-8111-111111111111"
	tenantB := "22222222-2222-4222-8222-222222222222"
	principalID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := repository.EnsureTenant(ctx, drive.TenantSeed{TenantID: tenantA, DisplayName: "A", RootNodeID: "root-a", Now: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnsureTenant(ctx, drive.TenantSeed{TenantID: tenantB, DisplayName: "B", RootNodeID: "root-b", Now: now}); err != nil {
		t.Fatal(err)
	}
	seed := drive.OIDCMemberSeed{
		PrincipalID: principalID, TenantID: tenantA, Issuer: "https://issuer.example.test", Subject: "subject-1",
		DisplayName: "User", Role: drive.RoleViewer, Now: now,
	}
	first, err := repository.EnsureOIDCMember(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.EnsureOIDCMember(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	if first.Role != drive.RoleViewer || second.Role != drive.RoleViewer || second.Status != drive.MemberStatusActive {
		t.Fatalf("unexpected idempotent member: first=%+v second=%+v", first, second)
	}
	seed.TenantID = tenantB
	seed.Role = drive.RoleEditor
	memberB, err := repository.EnsureOIDCMember(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	if memberB.Identity.TenantID != tenantB || memberB.TenantDisplayName != "B" || memberB.Role != drive.RoleEditor {
		t.Fatalf("unexpected second-tenant member: %+v", memberB)
	}
	resolvedA, err := repository.ResolveOIDCPrincipal(ctx, seed.Issuer, seed.Subject, tenantA)
	if err != nil || resolvedA.Role != drive.RoleViewer || resolvedA.Identity.TenantID != tenantA {
		t.Fatalf("resolve tenant A: record=%+v err=%v", resolvedA, err)
	}
	resolvedB, err := repository.ResolveOIDCPrincipal(ctx, seed.Issuer, seed.Subject, tenantB)
	if err != nil || resolvedB.Role != drive.RoleEditor || resolvedB.Identity.TenantID != tenantB {
		t.Fatalf("resolve tenant B: record=%+v err=%v", resolvedB, err)
	}
	if err := repository.SetOIDCMemberStatus(ctx, tenantB, principalID, drive.MemberStatusSuspended); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveOIDCPrincipal(ctx, seed.Issuer, seed.Subject, tenantB); drive.CodeOf(err) != drive.CodeForbidden {
		t.Fatalf("suspended member should be forbidden, got %v", err)
	}
	if err := repository.SetOIDCMemberStatus(ctx, tenantB, principalID, drive.MemberStatusActive); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveOIDCPrincipal(ctx, seed.Issuer, seed.Subject, tenantB); err != nil {
		t.Fatal(err)
	}
	conflicting := seed
	conflicting.TenantID = tenantA
	conflicting.PrincipalID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err := repository.EnsureOIDCMember(ctx, conflicting); drive.CodeOf(err) != drive.CodeNameConflict {
		t.Fatalf("same external identity with another principal should conflict, got %v", err)
	}
	conflicting = seed
	conflicting.Subject = "subject-2"
	if _, err := repository.EnsureOIDCMember(ctx, conflicting); drive.CodeOf(err) != drive.CodeNameConflict {
		t.Fatalf("same principal with another external identity should conflict, got %v", err)
	}
}
