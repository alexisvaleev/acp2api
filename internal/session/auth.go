package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/quonaro/acp2api/internal/acp"
	"github.com/quonaro/acp2api/internal/agent"
)

// authenticate selects an auth method when the agent advertises any. An agent
// that advertises none needs no call, which is the common case.
//
// The Devin CLI is the reason this is careful. Under ACP it refuses to use its
// own on-disk login — "ACP host is the sole source of credentials" — and its
// only advertised method, devin-browser, starts a browser PKCE flow. Calling
// that from a daemon opens a login window on every agent spawn, so the default
// is to send a key when one is configured and otherwise not to call at all:
// the agent's own error then says exactly what is missing.
func (c *connection) authenticate(ctx context.Context, cl *acp.Client, a agent.Agent, initRaw json.RawMessage) error {
	var init acp.InitializeResponse
	if err := json.Unmarshal(initRaw, &init); err != nil || len(init.AuthMethods) == 0 {
		return nil //nolint:nilerr // an unreadable result advertises no auth method
	}

	request := acp.AuthenticateRequest{MethodID: authMethodID(a, init)}

	switch {
	case a.HasKey():
		// Resolved once at startup, by the agent's module or from APIKeyEnv.
		request.Meta = map[string]any{"api_key": a.APIKey}
	case a.WantsInteractiveAuth():
		// No key by design: call the method as advertised. For the Devin CLI
		// that is a browser PKCE flow, which is why it has to be asked for.
	case a.CredentialMode() == agent.CredentialNone:
		return nil
	case a.APIKeyEnv != "":
		return fmt.Errorf(
			"session: agent %q is configured with api_key_env %q but no value was resolved at startup",
			a.ID, a.APIKeyEnv)
	default:
		slog.With("module", "session").Warn("skipping authenticate because no key was resolved",
			"agent", a.ID,
			"advertised_method", request.MethodID,
			"hint", "set api_key_env to the variable holding the agent's key, "+
				"or credential_source \"interactive\" if a browser prompt is acceptable")
		return nil
	}

	if _, err := cl.Request(ctx, acp.MethodAuthenticate, request); err != nil {
		return fmt.Errorf("session: authenticate agent %q with method %q: %w",
			a.ID, request.MethodID, err)
	}
	slog.With("module", "session").Debug("authenticated",
		"agent", a.ID, "method", request.MethodID, "with_key", request.Meta != nil)
	return nil
}

// authMethodID picks the advertised method to use: the configured override, or
// the first one advertised.
func authMethodID(a agent.Agent, init acp.InitializeResponse) string {
	if a.AuthMethod != "" {
		return a.AuthMethod
	}
	return init.AuthMethods[0].ID
}

// parseCapabilities reads the agent's prompt capabilities. An agent that says
// nothing is treated as text-only, which is the safe default: sending an image
// to an agent that cannot read it wastes a turn and answers blind.
func parseCapabilities(raw json.RawMessage) connectionCapabilities {
	var init acp.InitializeResponse
	if err := json.Unmarshal(raw, &init); err != nil {
		return connectionCapabilities{}
	}
	supported, _ := init.AgentCapabilities.PromptCapabilities["image"].(bool)
	return connectionCapabilities{Images: supported}
}
