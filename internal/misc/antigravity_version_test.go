package misc

import "testing"

func TestAntigravityHubIdentity(t *testing.T) {
	want := "antigravity/hub/2.31.0 (aidev_client; os_type=windows; arch=amd64; cl=1022991777)"
	for _, got := range []string{AntigravityUserAgent(), AntigravityRequestUserAgent(""), AntigravityLoadCodeAssistUserAgent(""), AntigravityOnboardUserUserAgent("")} {
		if got != want {
			t.Fatalf("User-Agent = %q, want %q", got, want)
		}
	}
	if got := AntigravityVersionFromUserAgent(want); got != "2.31.0" {
		t.Fatalf("version = %q", got)
	}
	if got := AntigravityVersionFromUserAgent("antigravity/cli/1.5.9 windows/amd64"); got != "1.5.9" {
		t.Fatalf("legacy version = %q", got)
	}
}
