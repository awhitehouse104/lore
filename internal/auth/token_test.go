package auth

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeBearerTokenAndAuthenticate(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	encoded := base64.RawURLEncoding.EncodeToString(secret)
	decoded, err := DecodeBearerToken(encoded)
	if err != nil || string(decoded) != string(secret) {
		t.Fatalf("DecodeBearerToken = %q, %v", decoded, err)
	}
	principal, err := NewPrincipal("remote_reader", TransportHTTP, []Permission{PermissionQuery}, []string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	authenticator := NewBearerAuthenticator([]BearerPrincipal{{
		Principal: principal,
		Digest:    DigestBearerToken(secret),
	}})
	got, ok := authenticator.Authenticate([]string{"bEaReR " + encoded})
	if !ok || got.ID != principal.ID {
		t.Fatalf("Authenticate = %+v, %v", got, ok)
	}
	got.Permissions[PermissionCapture] = struct{}{}
	again, ok := authenticator.Authenticate([]string{"Bearer " + encoded})
	if !ok || again.Has(PermissionCapture) {
		t.Fatalf("authenticator principal was mutable: %+v, %v", again, ok)
	}
}

func TestBearerAuthenticationRejectsMalformedHeaders(t *testing.T) {
	secret := []byte(strings.Repeat("a", MinimumBearerTokenBytes))
	principal, _ := NewPrincipal("remote_reader", TransportHTTP, []Permission{PermissionQuery}, []string{"normal"})
	authenticator := NewBearerAuthenticator([]BearerPrincipal{{
		Principal: principal,
		Digest:    DigestBearerToken(secret),
	}})
	valid := base64.RawURLEncoding.EncodeToString(secret)
	tests := [][]string{
		nil,
		{},
		{"Bearer"},
		{"Basic " + valid},
		{"Bearer  " + valid},
		{"Bearer " + valid + " "},
		{"Bearer not-a-valid-secret"},
		{"Bearer " + valid, "Bearer " + valid},
		{strings.Repeat("x", MaximumAuthorizationHeaderBytes+1)},
	}
	for _, headers := range tests {
		if principal, ok := authenticator.Authenticate(headers); ok {
			t.Errorf("Authenticate(%q) = %+v, true", headers, principal)
		}
	}
}

func TestBearerTokenBoundsAndPrincipalContext(t *testing.T) {
	for _, value := range []string{
		"",
		base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", MinimumBearerTokenBytes-1))),
		strings.Repeat("!", 64),
	} {
		if _, err := DecodeBearerToken(value); err == nil {
			t.Errorf("DecodeBearerToken(%q) succeeded", value)
		}
	}
	principal, _ := NewPrincipal("remote_reader", TransportHTTP, []Permission{PermissionQuery}, []string{"normal"})
	ctx := WithPrincipal(context.Background(), principal)
	got, ok := PrincipalFromContext(ctx)
	if !ok || got.ID != principal.ID {
		t.Fatalf("PrincipalFromContext = %+v, %v", got, ok)
	}
	got.AllowedSensitivities["sensitive"] = struct{}{}
	again, _ := PrincipalFromContext(ctx)
	if again.AllowsSensitivity("sensitive") {
		t.Fatal("context principal was mutable")
	}
}
