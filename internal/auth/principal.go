package auth

import (
	"fmt"
	"regexp"
	"sort"

	"lore/internal/docs"
	"lore/internal/search"
)

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

type Permission string

const (
	PermissionQuery   Permission = "query"
	PermissionCapture Permission = "capture"
	PermissionCurate  Permission = "curate"
	PermissionInspect Permission = "inspect"
	PermissionHistory Permission = "history"
)

var principalPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type PermissionSet map[Permission]struct{}

type Principal struct {
	ID                   string
	Transport            Transport
	Permissions          PermissionSet
	AllowedSensitivities map[string]struct{}
	IsLocalStdio         bool
}

func NewPrincipal(id string, transport Transport, permissions []Permission, sensitivities []string) (Principal, error) {
	if !principalPattern.MatchString(id) {
		return Principal{}, fmt.Errorf("principal ID must match ^[a-z][a-z0-9_-]{0,63}$")
	}
	switch transport {
	case TransportStdio, TransportHTTP:
	default:
		return Principal{}, fmt.Errorf("principal transport must be stdio or http")
	}
	permissionSet := make(PermissionSet, len(permissions))
	for _, permission := range permissions {
		if !ValidPermission(permission) {
			return Principal{}, fmt.Errorf("unknown permission %q", permission)
		}
		permissionSet[permission] = struct{}{}
	}
	allowed := make(map[string]struct{}, len(sensitivities))
	for _, sensitivity := range sensitivities {
		if !docs.ValidSensitivity(sensitivity) {
			return Principal{}, fmt.Errorf("unknown sensitivity %q", sensitivity)
		}
		if transport == TransportHTTP && sensitivity == "local-only" {
			return Principal{}, fmt.Errorf("HTTP principals cannot receive local-only sensitivity access")
		}
		allowed[sensitivity] = struct{}{}
	}
	return Principal{
		ID:                   id,
		Transport:            transport,
		Permissions:          permissionSet,
		AllowedSensitivities: allowed,
		IsLocalStdio:         transport == TransportStdio,
	}, nil
}

func ValidPermission(permission Permission) bool {
	switch permission {
	case PermissionQuery, PermissionCapture, PermissionCurate, PermissionInspect, PermissionHistory:
		return true
	default:
		return false
	}
}

func (p Principal) Has(permission Permission) bool {
	_, allowed := p.Permissions[permission]
	return allowed
}

func (p Principal) AllowsSensitivity(sensitivity string) bool {
	if p.Transport == TransportHTTP && sensitivity == "local-only" {
		return false
	}
	_, allowed := p.AllowedSensitivities[sensitivity]
	return allowed
}

func (p Principal) AccessPolicy() search.AccessPolicy {
	values := make([]string, 0, len(p.AllowedSensitivities))
	for sensitivity := range p.AllowedSensitivities {
		if p.Transport == TransportHTTP && sensitivity == "local-only" {
			continue
		}
		values = append(values, sensitivity)
	}
	sort.Strings(values)
	policy, err := search.NewAccessPolicy(values)
	if err != nil {
		return search.AccessPolicy{AllowedSensitivities: map[string]struct{}{}}
	}
	return policy
}

func (p Principal) PermissionsList() []Permission {
	values := make([]Permission, 0, len(p.Permissions))
	for permission := range p.Permissions {
		values = append(values, permission)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func (p Principal) SensitivitiesList() []string {
	return p.AccessPolicy().Values()
}

func (p Principal) Clone() Principal {
	permissions := make(PermissionSet, len(p.Permissions))
	for permission := range p.Permissions {
		permissions[permission] = struct{}{}
	}
	sensitivities := make(map[string]struct{}, len(p.AllowedSensitivities))
	for sensitivity := range p.AllowedSensitivities {
		sensitivities[sensitivity] = struct{}{}
	}
	p.Permissions = permissions
	p.AllowedSensitivities = sensitivities
	return p
}
