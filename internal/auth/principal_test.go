package auth

import (
	"reflect"
	"testing"
)

func TestPrincipalPermissionsAndSensitivity(t *testing.T) {
	principal, err := NewPrincipal(
		"reader_one",
		TransportStdio,
		[]Permission{PermissionQuery, PermissionInspect, PermissionQuery},
		[]string{"sensitive", "normal", "sensitive"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.Has(PermissionQuery) || principal.Has(PermissionCapture) {
		t.Fatalf("permissions = %v", principal.PermissionsList())
	}
	if !principal.AllowsSensitivity("normal") || principal.AllowsSensitivity("local-only") {
		t.Fatalf("sensitivities = %v", principal.SensitivitiesList())
	}
	if got, want := principal.SensitivitiesList(), []string{"normal", "sensitive"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sensitivities = %v, want %v", got, want)
	}
}

func TestHTTPPrincipalRejectsAndDefensivelyRemovesLocalOnly(t *testing.T) {
	if _, err := NewPrincipal("remote", TransportHTTP, []Permission{PermissionQuery}, []string{"normal", "local-only"}); err == nil {
		t.Fatal("HTTP local-only grant unexpectedly succeeded")
	}
	principal, err := NewPrincipal("remote", TransportHTTP, []Permission{PermissionQuery}, []string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	principal.AllowedSensitivities["local-only"] = struct{}{}
	if principal.AllowsSensitivity("local-only") || principal.AccessPolicy().Allows("local-only") {
		t.Fatal("defensive HTTP local-only boundary failed")
	}
}

func TestPrincipalRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		name          string
		id            string
		transport     Transport
		permission    Permission
		sensitivities []string
	}{
		{name: "id", id: "../owner", transport: TransportStdio, permission: PermissionQuery, sensitivities: []string{"normal"}},
		{name: "transport", id: "owner", transport: "pipe", permission: PermissionQuery, sensitivities: []string{"normal"}},
		{name: "permission", id: "owner", transport: TransportStdio, permission: "admin", sensitivities: []string{"normal"}},
		{name: "sensitivity", id: "owner", transport: TransportStdio, permission: PermissionQuery, sensitivities: []string{"secret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewPrincipal(test.id, test.transport, []Permission{test.permission}, test.sensitivities); err == nil {
				t.Fatal("invalid principal unexpectedly succeeded")
			}
		})
	}
}

func TestLocalProfilesAreFixed(t *testing.T) {
	full, err := LocalProfile(DefaultLocalProfile)
	if err != nil {
		t.Fatal(err)
	}
	if full.ID != "local-codex" || !full.Has(PermissionCurate) || !full.AllowsSensitivity("local-only") {
		t.Fatalf("local-full = %+v", full)
	}
	query, err := LocalProfile("local-query")
	if err != nil {
		t.Fatal(err)
	}
	if query.ID != "local-reader" || !query.Has(PermissionQuery) || query.Has(PermissionInspect) {
		t.Fatalf("local-query = %+v", query)
	}
	if _, err := LocalProfile("arbitrary"); err == nil {
		t.Fatal("arbitrary local profile unexpectedly succeeded")
	}
}

func TestPrincipalCloneDoesNotSharePolicyMaps(t *testing.T) {
	principal, err := NewPrincipal("owner", TransportStdio, []Permission{PermissionQuery}, []string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	clone := principal.Clone()
	delete(principal.Permissions, PermissionQuery)
	delete(principal.AllowedSensitivities, "normal")
	if !clone.Has(PermissionQuery) || !clone.AllowsSensitivity("normal") {
		t.Fatalf("clone changed with source: %+v", clone)
	}
}
