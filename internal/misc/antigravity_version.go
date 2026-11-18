// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"context"
	"strings"
)

const antigravityHubVersion = "2.28.0"

// StartAntigravityVersionUpdater is retained for SDK compatibility. The Hub
// version and build number are a fixed pair, so CLI manifests are not consulted.
func StartAntigravityVersionUpdater(context.Context) {}

func AntigravityLatestVersion() string { return antigravityHubVersion }

// AntigravityUserAgent returns the pinned Hub identity.
func AntigravityUserAgent() string {
	return "antigravity/hub/2.28.0 (aidev_client; os_type=windows; arch=amd64; cl=1008300195)"
}

func isAntigravityFamilyUserAgent(lower string) bool {
	return strings.HasPrefix(lower, "antigravity/cli/") || strings.HasPrefix(lower, "antigravity/hub/") || strings.HasPrefix(lower, "antigravity/")
}

func antigravityBaseUserAgent(userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return AntigravityUserAgent()
	}
	lower := strings.ToLower(userAgent)
	if isAntigravityFamilyUserAgent(lower) {
		if idx := strings.Index(lower, " google-api-nodejs-client/"); idx >= 0 {
			trimmed := strings.TrimSpace(userAgent[:idx])
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return userAgent
}

// AntigravityRequestUserAgent returns the short Antigravity runtime UA used by
// generate/stream/model-list requests.
func AntigravityRequestUserAgent(userAgent string) string {
	return antigravityBaseUserAgent(userAgent)
}

// AntigravityLoadCodeAssistUserAgent returns the short Antigravity UA used by
// loadCodeAssist requests.
func AntigravityLoadCodeAssistUserAgent(userAgent string) string {
	return AntigravityRequestUserAgent(userAgent)
}

// AntigravityOnboardUserUserAgent returns the CLI identity used for onboarding.
func AntigravityOnboardUserUserAgent(userAgent string) string {
	return antigravityBaseUserAgent(userAgent)
}

// AntigravityVersionFromUserAgent extracts the Antigravity version prefix from
// either the short or long Antigravity UA forms.
func AntigravityVersionFromUserAgent(userAgent string) string {
	base := antigravityBaseUserAgent(userAgent)
	lower := strings.ToLower(base)
	for _, prefix := range []string{"antigravity/cli/", "antigravity/hub/", "antigravity/"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		if fields := strings.Fields(base[len(prefix):]); len(fields) > 0 {
			return fields[0]
		}
		break
	}
	return AntigravityLatestVersion()
}
