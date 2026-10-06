package config

import (
	"fmt"
	"net/url"
)

// ProxyConfig is the proxy the agent CLIs should use.
//
// It is applied as environment, not by interception: every agent CLI reaches
// its own API over HTTP, and the standard proxy variables are how that is
// routed — for a corporate egress, or to reach an API that is not served in the
// host's region.
type ProxyConfig struct {
	// URL applies to every protocol. A scheme such as socks5:// is honoured by
	// most CLIs.
	URL string `json:"url" yaml:"url"`
	// HTTP and HTTPS override URL for a single protocol.
	HTTP  string `json:"http" yaml:"http"`
	HTTPS string `json:"https" yaml:"https"`
	// NoProxy lists hosts that bypass the proxy, comma separated.
	NoProxy string `json:"no_proxy" yaml:"no_proxy"`
}

// validate checks the URLs in a proxy block. prefix names the block in the
// error, so a bad per-agent value is not mistaken for a bad global one.
func (p ProxyConfig) validate(prefix string) error {
	for name, raw := range map[string]string{
		"url": p.URL, "http": p.HTTP, "https": p.HTTPS,
	} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("config: %s.%s is not a valid url: %w", prefix, name, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("config: %s.%s must include a scheme and host, got %q", prefix, name, raw)
		}
	}
	return nil
}

// Env renders the proxy as the environment an agent CLI expects.
//
// Both cases of each name are set: tools disagree about which they read, and
// setting only one is a silent failure. The values may carry credentials, so
// this is never logged verbatim.
func (p ProxyConfig) Env() map[string]string {
	env := map[string]string{}

	all := p.URL
	httpURL := firstNonEmpty(p.HTTP, p.URL)
	httpsURL := firstNonEmpty(p.HTTPS, p.URL)

	for _, pair := range []struct{ name, value string }{
		{"HTTP_PROXY", httpURL}, {"http_proxy", httpURL},
		{"HTTPS_PROXY", httpsURL}, {"https_proxy", httpsURL},
		{"ALL_PROXY", all}, {"all_proxy", all},
		{"NO_PROXY", p.NoProxy}, {"no_proxy", p.NoProxy},
	} {
		if pair.value != "" {
			env[pair.name] = pair.value
		}
	}
	return env
}

// Configured reports whether any proxy is set.
func (p ProxyConfig) Configured() bool {
	return p.URL != "" || p.HTTP != "" || p.HTTPS != ""
}

// Redacted renders the proxy for a log line, with any credentials removed.
func (p ProxyConfig) Redacted() string {
	if !p.Configured() {
		return ""
	}
	return redactURL(firstNonEmpty(p.HTTPS, p.HTTP, p.URL))
}

// redactURL strips userinfo credentials from a URL.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.User("***")
	return parsed.String()
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mergeEnv layers environment maps, later ones winning.
func mergeEnv(layers ...map[string]string) map[string]string {
	total := 0
	for _, layer := range layers {
		total += len(layer)
	}
	if total == 0 {
		return nil
	}
	out := make(map[string]string, total)
	for _, layer := range layers {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}
