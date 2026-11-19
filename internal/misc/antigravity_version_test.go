package misc

import "testing"

func TestAntigravityWireIdentity(t *testing.T) {
	want := "antigravity/cli/1.5.11 (aidev_client; os_type=linux; arch=amd64; cl=1024248524; auth_method=consumer; datacollection_opt_in=false; host_path=acp-client/1.0.0)"
	for _, got := range []string{AntigravityUserAgent(), AntigravityRequestUserAgent(""), AntigravityLoadCodeAssistUserAgent(""), AntigravityOnboardUserUserAgent("")} {
		if got != want {
			t.Fatalf("User-Agent = %q, want %q", got, want)
		}
	}
	if got := AntigravityVersionFromUserAgent(want); got != "1.5.11" {
		t.Fatalf("version = %q", got)
	}
	if got := AntigravityVersionFromUserAgent("antigravity/cli/1.5.9 windows/amd64"); got != "1.5.9" {
		t.Fatalf("legacy version = %q", got)
	}
}
