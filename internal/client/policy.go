package client

import (
	"context"
	"fmt"

	"github.com/quonaro/acp2api/internal/acp"
)

// Policy decides which permission option to select for an agent request.
//
// ACP agents ask before doing anything consequential; a headless gateway has no
// human to ask, so the decision is a configured policy. This is the gateway's
// security boundary and it is deliberately explicit rather than defaulted.
type Policy interface {
	// Decide returns the option id to select. An empty id cancels the request.
	Decide(ctx context.Context, req acp.RequestPermissionRequest) (string, error)
}

// PolicyFunc adapts a function to a Policy.
type PolicyFunc func(ctx context.Context, req acp.RequestPermissionRequest) (string, error)

// Decide implements Policy.
func (f PolicyFunc) Decide(ctx context.Context, req acp.RequestPermissionRequest) (string, error) {
	return f(ctx, req)
}

// AllowAll approves every request, preferring a one-shot allow option so a
// single approval does not silently become a standing grant.
func AllowAll() Policy {
	return PolicyFunc(func(_ context.Context, req acp.RequestPermissionRequest) (string, error) {
		return pickOption(req.Options, true), nil
	})
}

// DenyAll rejects every request. When the agent offers no reject option the
// request is cancelled, which is the ACP equivalent of "no".
func DenyAll() Policy {
	return PolicyFunc(func(_ context.Context, req acp.RequestPermissionRequest) (string, error) {
		return pickOption(req.Options, false), nil
	})
}

// ParsePolicy maps a config value to a Policy.
func ParsePolicy(name string) (Policy, error) {
	switch name {
	case "", "allow":
		return AllowAll(), nil
	case "deny":
		return DenyAll(), nil
	default:
		return nil, fmt.Errorf("client: unknown permission policy %q (want \"allow\" or \"deny\")", name)
	}
}

// pickOption chooses an option matching the requested outcome: an exact
// one-shot kind first, then any option of the same disposition. For a deny with
// no reject option it returns "" so the caller cancels.
func pickOption(options []acp.PermissionOption, allow bool) string {
	exact := acp.PermRejectOnce
	if allow {
		exact = acp.PermAllowOnce
	}
	for _, o := range options {
		if o.Kind == exact {
			return o.OptionID
		}
	}
	for _, o := range options {
		isAllow := o.Kind == acp.PermAllowOnce || o.Kind == acp.PermAllowAlways
		if isAllow == allow {
			return o.OptionID
		}
	}
	if allow && len(options) > 0 {
		return options[0].OptionID
	}
	return ""
}
