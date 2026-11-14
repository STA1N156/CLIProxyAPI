package misc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, cliManifestURL string) func() {
	t.Helper()

	oldCLIManifest := antigravityCLILatestManifestURL
	antigravityCLILatestManifestURL = cliManifestURL

	return func() {
		antigravityCLILatestManifestURL = oldCLIManifest
	}
}

func overrideAntigravityVersionCacheForTest(t *testing.T, version string, expiry time.Time) func() {
	t.Helper()

	antigravityVersionMu.Lock()
	oldVersion := cachedAntigravityVersion
	oldExpiry := antigravityVersionExpiry
	cachedAntigravityVersion = version
	antigravityVersionExpiry = expiry
	antigravityVersionMu.Unlock()

	return func() {
		antigravityVersionMu.Lock()
		cachedAntigravityVersion = oldVersion
		antigravityVersionExpiry = oldExpiry
		antigravityVersionMu.Unlock()
	}
}

func TestAntigravityLatestVersionUsesCurrentCLIFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, antigravityFallbackVersion)
	}
}

func TestAntigravityUserAgentUsesCLIFamily(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "1.5.9", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/cli/1.5.9 (aidev_client; os_type=windows; arch=amd64; cl=1021519450; auth_method=consumer; datacollection_opt_in=false; host_path=unknown/unknown)"
	if got := AntigravityUserAgent(); got != want {
		t.Fatalf("AntigravityUserAgent() = %q, want %q", got, want)
	}
	if got := AntigravityVersionFromUserAgent(want); got != "1.5.9" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want 1.5.9", got)
	}
}

func TestAntigravityVersionFromUserAgentParsesHubFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/hub/2.2.1 darwin/arm64"); got != "2.2.1" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "2.2.1")
	}
}

func TestAntigravityVersionFromUserAgentParsesLegacyFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/1.23.2 windows/amd64"); got != "1.23.2" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "1.23.2")
	}
}

func TestAntigravityLoadCodeAssistUserAgentUsesShortUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/cli/2.2.1 (aidev_client; os_type=windows; arch=amd64; auth_method=consumer; datacollection_opt_in=false; host_path=unknown/unknown)"
	if got := AntigravityLoadCodeAssistUserAgent(""); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent() = %q, want %q", got, want)
	}
	if got := AntigravityLoadCodeAssistUserAgent(want); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent(configured) = %q, want %q", got, want)
	}
}

func TestAntigravityOnboardUserUserAgentUsesCLIUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/cli/2.2.1 (aidev_client; os_type=windows; arch=amd64; auth_method=consumer; datacollection_opt_in=false; host_path=unknown/unknown)"
	if got := AntigravityOnboardUserUserAgent(""); got != want {
		t.Fatalf("AntigravityOnboardUserUserAgent() = %q, want %q", got, want)
	}
}

func TestFetchAntigravityLatestVersionUsesCLIManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/darwin_arm64.json":
			if got := r.Header.Get("Accept"); got != "application/json" {
				t.Errorf("cli manifest Accept = %q, want %q", got, "application/json")
			}
			if got := r.Header.Get("Cache-Control"); got != "no-cache" {
				t.Errorf("cli manifest Cache-Control = %q, want %q", got, "no-cache")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"1.5.9"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/manifests/darwin_arm64.json")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "1.5.9" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "1.5.9")
	}
}

func TestFetchAntigravityLatestVersionReturnsCLIManifestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary outage", http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/manifests/darwin_arm64.json")
	defer restore()

	_, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch == nil {
		t.Fatal("fetchAntigravityLatestVersion() error = nil, want error")
	}
}
