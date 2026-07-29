package auth

import "fmt"

const DefaultLocalProfile = "local-full"

func LocalProfile(name string) (Principal, error) {
	switch name {
	case "local-full":
		return NewPrincipal(
			"local-codex",
			TransportStdio,
			[]Permission{
				PermissionQuery,
				PermissionCapture,
				PermissionCurate,
				PermissionInspect,
				PermissionHistory,
			},
			[]string{"normal", "sensitive", "local-only"},
		)
	case "local-query":
		return NewPrincipal(
			"local-reader",
			TransportStdio,
			[]Permission{PermissionQuery},
			[]string{"normal", "sensitive", "local-only"},
		)
	default:
		return Principal{}, fmt.Errorf("unknown local MCP profile %q", name)
	}
}
