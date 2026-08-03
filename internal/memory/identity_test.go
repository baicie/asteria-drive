package memory

import (
	"context"
	"sync"
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

func TestMemberLifecycleProtectsLastOwnerAndPaginates(t *testing.T) {
	repository := NewRepository()
	ctx := context.Background()
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	tenantID := "33333333-3333-4333-8333-333333333333"
	if _, err := repository.EnsureTenant(ctx, drive.TenantSeed{TenantID: tenantID, DisplayName: "Lifecycle", RootNodeID: "root-lifecycle", Now: now}); err != nil {
		t.Fatal(err)
	}
	ownerID := "aaaaaaaa-1111-4111-8111-aaaaaaaaaaaa"
	secondOwnerID := "bbbbbbbb-2222-4222-8222-bbbbbbbbbbbb"
	adminID := "cccccccc-3333-4333-8333-cccccccccccc"
	viewerID := "dddddddd-4444-4444-8444-dddddddddddd"
	for _, member := range []struct {
		id   string
		role drive.AccessRole
	}{
		{ownerID, drive.RoleOwner}, {secondOwnerID, drive.RoleOwner}, {adminID, drive.RoleAdmin}, {viewerID, drive.RoleViewer},
	} {
		if _, err := repository.EnsureOIDCMember(ctx, drive.OIDCMemberSeed{
			PrincipalID: member.id, TenantID: tenantID, Issuer: "https://issuer.lifecycle.test", Subject: member.id,
			DisplayName: member.id[:8], Role: member.role, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, more, err := repository.ListMembers(ctx, tenantID, drive.CursorPosition{}, 2)
	if err != nil || len(items) != 2 || !more {
		t.Fatalf("first member page: items=%d more=%v err=%v", len(items), more, err)
	}
	items, more, err = repository.ListMembers(ctx, tenantID, drive.CursorPosition{Name: items[1].Identity.PrincipalID, ID: items[1].Identity.PrincipalID}, 2)
	if err != nil || len(items) != 2 || more {
		t.Fatalf("second member page: items=%d more=%v err=%v", len(items), more, err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: viewerID, ActorPrincipalID: ownerID, ActorRole: drive.RoleOwner,
		Role: ptrRole(drive.RoleEditor), Status: ptrStatus(drive.MemberStatusSuspended), Now: now,
	}); err != nil {
		t.Fatalf("owner update: %v", err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: ownerID, ActorPrincipalID: adminID, ActorRole: drive.RoleAdmin,
		Role: ptrRole(drive.RoleViewer), Now: now,
	}); drive.CodeOf(err) != drive.CodeForbidden {
		t.Fatalf("admin should not modify owner, got %v", err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: adminID, ActorPrincipalID: adminID, ActorRole: drive.RoleAdmin,
		Role: ptrRole(drive.RoleOwner), Now: now,
	}); drive.CodeOf(err) != drive.CodeForbidden {
		t.Fatalf("admin should not grant owner, got %v", err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: ownerID, ActorPrincipalID: ownerID, ActorRole: drive.RoleOwner,
		Status: ptrStatus(drive.MemberStatusSuspended), Now: now,
	}); err != nil {
		t.Fatalf("owner should be able to suspend itself while another owner remains: %v", err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: secondOwnerID, ActorPrincipalID: secondOwnerID, ActorRole: drive.RoleOwner,
		Status: ptrStatus(drive.MemberStatusSuspended), Now: now,
	}); drive.CodeOf(err) != drive.CodeInvalidState {
		t.Fatalf("last active owner should be protected, got %v", err)
	}
	if _, err := repository.UpdateMember(ctx, drive.UpdateMemberCommand{
		TenantID: tenantID, PrincipalID: secondOwnerID, ActorPrincipalID: adminID, ActorRole: drive.RoleAdmin,
		Status: ptrStatus(drive.MemberStatusSuspended), Now: now,
	}); drive.CodeOf(err) != drive.CodeForbidden {
		t.Fatalf("admin should not modify owner, got %v", err)
	}

	// Two concurrent owners may race to demote one another, but the lock-protected
	// invariant must leave at least one active owner.
	concurrentRepository := NewRepository()
	concurrentTenant := "44444444-4444-4444-8444-444444444444"
	if _, err := concurrentRepository.EnsureTenant(ctx, drive.TenantSeed{TenantID: concurrentTenant, DisplayName: "Concurrent", RootNodeID: "root-concurrent", Now: now}); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{ownerID, secondOwnerID} {
		if _, err := concurrentRepository.EnsureOIDCMember(ctx, drive.OIDCMemberSeed{
			PrincipalID: member, TenantID: concurrentTenant, Issuer: "https://issuer.concurrent.test", Subject: member,
			Role: drive.RoleOwner, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for _, pair := range [][2]string{{ownerID, secondOwnerID}, {secondOwnerID, ownerID}} {
		wg.Add(1)
		go func(actor, target string) {
			defer wg.Done()
			_, _ = concurrentRepository.UpdateMember(ctx, drive.UpdateMemberCommand{
				TenantID: concurrentTenant, PrincipalID: target, ActorPrincipalID: actor, ActorRole: drive.RoleOwner,
				Role: ptrRole(drive.RoleViewer), Now: now,
			})
		}(pair[0], pair[1])
	}
	wg.Wait()
	final, _, err := concurrentRepository.ListMembers(ctx, concurrentTenant, drive.CursorPosition{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	activeOwners := 0
	for _, member := range final {
		if member.Role == drive.RoleOwner && member.Status == drive.MemberStatusActive {
			activeOwners++
		}
	}
	if activeOwners < 1 {
		t.Fatalf("owner invariant was violated: %+v", final)
	}
}

func ptrRole(value drive.AccessRole) *drive.AccessRole { return &value }

func ptrStatus(value drive.MemberStatus) *drive.MemberStatus { return &value }
