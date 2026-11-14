// Package misc provides miscellaneous utility functions for the CLI Proxy API server.
package misc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// Used until the CLI manifest has been fetched, or when its cache expires.
	antigravityFallbackVersion = "1.5.9"
	antigravityCLIPlatform     = "os_type=windows; arch=amd64"
	antigravityVersionCacheTTL = 6 * time.Hour
	antigravityFetchTimeout    = 10 * time.Second
)

var (
	antigravityCLILatestManifestURL = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/windows_amd64.json"
)

type antigravityCLIUpdaterManifest struct {
	Version string `json:"version"`
}

var (
	cachedAntigravityVersion = antigravityFallbackVersion
	antigravityVersionMu     sync.RWMutex
	antigravityVersionExpiry time.Time
	antigravityUpdaterOnce   sync.Once
)

// StartAntigravityVersionUpdater starts a background goroutine that periodically refreshes the cached antigravity version.
// This is intentionally decoupled from request execution to avoid blocking executors on version lookups.
func StartAntigravityVersionUpdater(ctx context.Context) {
	antigravityUpdaterOnce.Do(func() {
		go runAntigravityVersionUpdater(ctx)
	})
}

func runAntigravityVersionUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(antigravityVersionCacheTTL / 2)
	defer ticker.Stop()

	log.Infof("periodic antigravity version refresh started (interval=%s)", antigravityVersionCacheTTL/2)

	refreshAntigravityVersion(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshAntigravityVersion(ctx)
		}
	}
}

func refreshAntigravityVersion(ctx context.Context) {
	version, errFetch := fetchAntigravityLatestVersion(ctx)

	antigravityVersionMu.Lock()
	defer antigravityVersionMu.Unlock()

	now := time.Now()

	if errFetch == nil {
		cachedAntigravityVersion = version
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		log.WithField("version", version).Info("fetched latest antigravity version")
		return
	}

	if cachedAntigravityVersion == "" || now.After(antigravityVersionExpiry) {
		cachedAntigravityVersion = antigravityFallbackVersion
		antigravityVersionExpiry = now.Add(antigravityVersionCacheTTL)
		log.WithError(errFetch).Warn("failed to refresh antigravity version, using fallback version")
		return
	}

	log.WithError(errFetch).Debug("failed to refresh antigravity version, keeping cached value")
}

// AntigravityLatestVersion returns the cached antigravity version refreshed by StartAntigravityVersionUpdater.
// It falls back to antigravityFallbackVersion if the cache is empty or stale.
func AntigravityLatestVersion() string {
	antigravityVersionMu.RLock()
	if cachedAntigravityVersion != "" && time.Now().Before(antigravityVersionExpiry) {
		v := cachedAntigravityVersion
		antigravityVersionMu.RUnlock()
		return v
	}
	antigravityVersionMu.RUnlock()

	return antigravityFallbackVersion
}

// AntigravityUserAgent returns the current Antigravity CLI identity.
func AntigravityUserAgent() string {
	version := AntigravityLatestVersion()
	metadata := "aidev_client; " + antigravityCLIPlatform
	if version == "1.5.9" {
		metadata += "; cl=1021519450"
	}
	return fmt.Sprintf("antigravity/cli/%s (%s; auth_method=consumer; datacollection_opt_in=false; host_path=unknown/unknown)", version, metadata)
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

func fetchAntigravityLatestVersion(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	client := &http.Client{Timeout: antigravityFetchTimeout, Transport: CloneAntigravityTransport(nil)}
	return fetchAntigravityCLILatestManifestVersion(ctx, client)
}

func fetchAntigravityCLILatestManifestVersion(ctx context.Context, client *http.Client) (string, error) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, antigravityCLILatestManifestURL, nil)
	if errReq != nil {
		return "", fmt.Errorf("build antigravity CLI updater manifest request: %w", errReq)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Cache-Control", "no-cache")

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return "", fmt.Errorf("fetch antigravity CLI updater manifest: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Warn("antigravity CLI updater manifest response body close error")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("antigravity CLI updater manifest returned status %d", resp.StatusCode)
	}

	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if errRead != nil {
		return "", fmt.Errorf("read antigravity CLI updater manifest: %w", errRead)
	}

	var manifest antigravityCLIUpdaterManifest
	if errDecode := json.Unmarshal(raw, &manifest); errDecode != nil {
		return "", fmt.Errorf("decode antigravity CLI updater manifest: %w", errDecode)
	}

	version := strings.TrimSpace(manifest.Version)
	if version == "" {
		return "", errors.New("antigravity CLI updater manifest returned empty version")
	}
	if !isValidAntigravitySemVersion(version) {
		return "", fmt.Errorf("antigravity CLI updater manifest returned invalid version %q", version)
	}
	return version, nil
}

func isValidAntigravitySemVersion(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}

	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return false
			}
		}
	}

	return true
}
