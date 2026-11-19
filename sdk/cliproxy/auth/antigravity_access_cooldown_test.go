package auth

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestAntigravityAccessDeniedCooldown(t *testing.T) {
	now := time.Now()
	for _, reason := range []string{"USER_PROJECT_DENIED", "SUBSCRIPTION_REQUIRED", "TOS_VIOLATION"} {
		auth := &Auth{Provider: "antigravity"}
		err := &Error{HTTPStatus: http.StatusForbidden, Message: fmt.Sprintf(`{"error":{"details":[{"reason":%q}]}}`, reason)}
		applyAuthFailureState(auth, err, nil, now, false)
		want := time.Minute
		if reason == "TOS_VIOLATION" {
			want = 30 * time.Minute
		}
		if got := auth.NextRetryAfter.Sub(now); got != want {
			t.Errorf("%s cooldown = %v, want %v", reason, got, want)
		}
	}
}
