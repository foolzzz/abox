package claude

import (
	"fmt"
	"strings"
)

type PermissionPolicy struct {
	Mode string
}

func StaticPermissionPolicy(mode string) (PermissionPolicy, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	normalized = strings.NewReplacer("-", "", "_", "").Replace(normalized)
	switch normalized {
	case "", "default", "deny", "denyall", "dontask":
		return PermissionPolicy{Mode: "dontAsk"}, nil
	case "acceptedits":
		return PermissionPolicy{Mode: "acceptEdits"}, nil
	case "auto":
		return PermissionPolicy{Mode: "auto"}, nil
	case "plan":
		return PermissionPolicy{Mode: "plan"}, nil
	case "bypasspermissions", "bypass":
		return PermissionPolicy{Mode: "bypassPermissions"}, nil
	case "manual", "interactive", "host":
		return PermissionPolicy{}, &OperationError{
			Operation: "permission policy",
			Cause:     ErrInteractiveApprovalUnsupported,
			Detail:    fmt.Sprintf("mode %q requires an interactive approval bridge", mode),
		}
	default:
		return PermissionPolicy{}, fmt.Errorf("claude permission policy: unknown static mode %q", mode)
	}
}

func (p PermissionPolicy) commandArgs() ([]string, error) {
	resolved, err := StaticPermissionPolicy(p.Mode)
	if err != nil {
		return nil, err
	}

	args := []string{"--permission-mode", resolved.Mode, "--permission-prompts", "none"}
	if resolved.Mode == "bypassPermissions" {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	return args, nil
}
