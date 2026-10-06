package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// envReference matches ${NAME} and ${NAME:-default}.
var envReference = regexp.MustCompile(`\$\{([^}]+)\}`)

// expandEnv substitutes environment references in the raw configuration text,
// before it is parsed.
//
// Substitution runs on the text rather than on the decoded values, so it works
// anywhere a string appears: a proxy url, an agent's environment, a workspace
// path. That is what lets one file serve as a template for several
// environments.
//
// A line whose first non-space character is `#` is a comment and is left alone,
// so a commented-out example such as `# token: ${ACP2API_TOKEN}` does not
// demand that the variable be set.
//
// An unset variable with no default is an error, not an empty string. A typo
// in ${ACP2API_TOKEN} would otherwise leave the gateway running without
// authentication, and the failure would be silent — which is the one behaviour
// this project does not allow. Write ${NAME:-} to say that empty is acceptable.
func expandEnv(input string) (string, error) {
	var failure error

	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines[i] = envReference.ReplaceAllStringFunc(line, func(match string) string {
			inner := match[2 : len(match)-1]

			name, fallback, hasFallback := strings.Cut(inner, ":-")
			name = strings.TrimSpace(name)
			if name == "" {
				failure = fmt.Errorf("empty variable name in %q", match)
				return match
			}

			if value, ok := os.LookupEnv(name); ok {
				return value
			}
			if hasFallback {
				return fallback
			}

			failure = fmt.Errorf(
				"%s is not set and no default was given; write ${%s:-} if empty is intended",
				name, name)
			return match
		})
	}

	if failure != nil {
		return "", fmt.Errorf("config: %w", failure)
	}
	return strings.Join(lines, "\n"), nil
}
