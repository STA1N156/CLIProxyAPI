package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func withQuotaCooldownEnabled(t *testing.T) {
	t.Helper()
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })
}

func quotaResult(authID, model string) Result {
	return Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "quota",
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	}
}

func TestMarkResultQuotaDefaultCooldownIsFixed(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-quota-fixed",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), quotaResult(auth.ID, "gpt-5"))
	first, ok := manager.GetByID(auth.ID)
	if !ok || first == nil || first.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after first failure")
	}
	firstState := first.ModelStates["gpt-5"]
	if firstState.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel 0 after first failure, got %d", firstState.Quota.BackoffLevel)
	}
	diff := time.Until(firstState.Quota.NextRecoverAt)
	if diff < 4*time.Second || diff > 6*time.Second {
		t.Fatalf("expected fallback quota cooldown to be ~5 seconds, got %v", diff)
	}

	manager.MarkResult(context.Background(), quotaResult(auth.ID, "gpt-5"))
	second, ok := manager.GetByID(auth.ID)
	if !ok || second == nil || second.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after second failure")
	}
	secondState := second.ModelStates["gpt-5"]
	if secondState.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel to stay 0, got %d", secondState.Quota.BackoffLevel)
	}
	diff = time.Until(secondState.Quota.NextRecoverAt)
	if diff < 4*time.Second || diff > 6*time.Second {
		t.Fatalf("expected fallback quota cooldown to stay ~5 seconds, got %v", diff)
	}
}

func TestMarkResultQuotaDefaultCooldownClearsPreviousBackoff(t *testing.T) {
	withQuotaCooldownEnabled(t)

	expired := time.Now().Add(-time.Second)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-quota-expired",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			"gpt-5": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired, BackoffLevel: 3},
			},
		},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), quotaResult(auth.ID, "gpt-5"))
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after failure")
	}
	state := updated.ModelStates["gpt-5"]
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel 0 after fallback quota failure, got %d", state.Quota.BackoffLevel)
	}
	diff := time.Until(state.Quota.NextRecoverAt)
	if diff < 4*time.Second || diff > 6*time.Second {
		t.Fatalf("expected fallback quota cooldown to be ~5 seconds, got %v", diff)
	}
}

func TestApplyAuthFailureStateQuotaFallbackAndRetryHint(t *testing.T) {
	now := time.Now()
	quotaErr := &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests}
	auth := &Auth{ID: "auth-level-quota"}

	applyAuthFailureState(auth, quotaErr, nil, now, false)
	if auth.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel 0 after first failure, got %d", auth.Quota.BackoffLevel)
	}
	firstRecover := auth.Quota.NextRecoverAt
	if !firstRecover.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("expected first window to close at %v, got %v", now.Add(5*time.Second), firstRecover)
	}

	applyAuthFailureState(auth, quotaErr, nil, now.Add(100*time.Millisecond), false)
	if auth.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel to stay 0, got %d", auth.Quota.BackoffLevel)
	}
	if !auth.Quota.NextRecoverAt.Equal(now.Add(5100 * time.Millisecond)) {
		t.Fatalf("expected fallback window to close at %v, got %v", now.Add(5100*time.Millisecond), auth.Quota.NextRecoverAt)
	}

	retryAfter := 10 * time.Second
	applyAuthFailureState(auth, quotaErr, &retryAfter, now.Add(3*time.Second), false)
	if auth.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel to stay 0 with retry hint, got %d", auth.Quota.BackoffLevel)
	}
	if !auth.Quota.NextRecoverAt.Equal(now.Add(13 * time.Second)) {
		t.Fatalf("expected retry hint window to close at %v, got %v", now.Add(13*time.Second), auth.Quota.NextRecoverAt)
	}
}

func TestQuotaRetryAfterParsesGoogleQuotaResetHints(t *testing.T) {
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	jsonErr := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message: `{
			"error": {
				"code": 429,
				"message": "Individual quota reached. Resets in 48h28m30s.",
				"details": [
					{"metadata": {
						"quotaResetDelay": "48h28m30.56899813s",
						"quotaResetTimeStamp": "2026-07-10T10:32:17Z"
					}},
					{"retryDelay": "174510.568998130s"}
				]
			}
		}`,
	}
	got := quotaRetryAfterFromResult(jsonErr, nil, now)
	if got == nil || *got != 174510568998130*time.Nanosecond {
		t.Fatalf("quotaRetryAfterFromResult(json) = %v, want 174510.568998130s", got)
	}

	textErr := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    "Individual quota reached. Resets in 75h35m33s.",
	}
	got = quotaRetryAfterFromResult(textErr, nil, now)
	if got == nil || *got != 75*time.Hour+35*time.Minute+33*time.Second {
		t.Fatalf("quotaRetryAfterFromResult(text) = %v, want 75h35m33s", got)
	}
}

func TestMarkResultQuotaUsesResetsInCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-quota-reset-text", Provider: "antigravity"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gemini-3.1-pro-low",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "Individual quota reached. Resets in 2m3s.",
		},
	})
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates["gemini-3.1-pro-low"] == nil {
		t.Fatal("expected model state after quota failure")
	}
	diff := time.Until(updated.ModelStates["gemini-3.1-pro-low"].NextRetryAfter)
	if diff < 122*time.Second || diff > 124*time.Second {
		t.Fatalf("expected quota reset text cooldown to be ~2m3s, got %v", diff)
	}
}

func TestRecoverableUnknownFailuresHaveFiniteCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	testCases := []struct {
		name      string
		model     string
		resultErr *Error
	}{
		{name: "model failure without error details", model: "gpt-5"},
		{name: "auth transport failure without status", resultErr: &Error{Message: "connection reset"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-unknown-" + testCase.name, Provider: "codex"}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
				t.Fatalf("Register returned error: %v", errRegister)
			}

			manager.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    testCase.model,
				Success:  false,
				Error:    testCase.resultErr,
			})

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("expected auth after failure")
			}
			var nextRetryAfter time.Time
			if testCase.model == "" {
				nextRetryAfter = updated.NextRetryAfter
			} else {
				state := updated.ModelStates[testCase.model]
				if state == nil {
					t.Fatalf("expected model state for %q", testCase.model)
				}
				nextRetryAfter = state.NextRetryAfter
			}
			if nextRetryAfter.IsZero() {
				t.Fatal("recoverable failure has no retry deadline")
			}
			if blocked, _, _ := isAuthBlockedForModel(updated, testCase.model, time.Now()); !blocked {
				t.Fatal("auth was not blocked during recoverable failure cooldown")
			}
			if blocked, _, _ := isAuthBlockedForModel(updated, testCase.model, nextRetryAfter.Add(time.Nanosecond)); blocked {
				t.Fatal("auth did not automatically recover after retry deadline")
			}
		})
	}
}

func TestSchedulerPromotesUnknownFailureAfterRetryDeadline(t *testing.T) {
	withQuotaCooldownEnabled(t)
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	const (
		provider = "gemini"
		model    = "scheduler-unknown-recovery-model"
		authID   = "scheduler-unknown-recovery-auth"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: authID, Provider: provider}); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	if _, errPick := manager.scheduler.pickSingle(context.Background(), provider, model, cliproxyexecutor.Options{}, nil); errPick != nil {
		t.Fatalf("initial scheduler pick returned error: %v", errPick)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Message: "transport closed"},
	})

	manager.scheduler.mu.Lock()
	defer manager.scheduler.mu.Unlock()
	providerScheduler := manager.scheduler.providers[provider]
	if providerScheduler == nil {
		t.Fatalf("scheduler provider %q is missing", provider)
	}
	shard := providerScheduler.modelShards[model]
	if shard == nil {
		t.Fatalf("scheduler model shard %q is missing", model)
	}
	entry := shard.entries[authID]
	if entry == nil {
		t.Fatalf("scheduler auth %q is missing", authID)
	}
	if entry.state != scheduledStateBlocked || entry.nextRetryAt.IsZero() {
		t.Fatalf("scheduler entry state = %v, retry = %v; want finite blocked state", entry.state, entry.nextRetryAt)
	}

	shard.promoteExpiredLocked(entry.nextRetryAt.Add(time.Nanosecond))
	if entry.state != scheduledStateReady {
		t.Fatalf("scheduler entry state after deadline = %v, want ready", entry.state)
	}
}

func TestJitteredCooldownWaitBounds(t *testing.T) {
	cases := []struct {
		wait      time.Duration
		maxWait   time.Duration
		maxJitter time.Duration
	}{
		{time.Second, 0, 250 * time.Millisecond},
		{8 * time.Second, 0, 2 * time.Second},
		{30 * time.Second, 0, 2 * time.Second},
		{time.Second, 30 * time.Second, 250 * time.Millisecond},
		{29 * time.Second, 30 * time.Second, time.Second},
	}
	for _, tc := range cases {
		for i := 0; i < 200; i++ {
			got := jitteredCooldownWait(tc.wait, tc.maxWait)
			if got < tc.wait || got >= tc.wait+tc.maxJitter {
				t.Fatalf("jitteredCooldownWait(%v, %v) = %v, want in [%v, %v)", tc.wait, tc.maxWait, got, tc.wait, tc.wait+tc.maxJitter)
			}
			if tc.maxWait > 0 && got > tc.maxWait {
				t.Fatalf("jitteredCooldownWait(%v, %v) = %v exceeds maxWait", tc.wait, tc.maxWait, got)
			}
		}
	}

	// maxWait is a hard ceiling: zero headroom disables jitter entirely.
	for i := 0; i < 50; i++ {
		if got := jitteredCooldownWait(30*time.Second, 30*time.Second); got != 30*time.Second {
			t.Fatalf("expected wait at maxWait to stay unjittered, got %v", got)
		}
	}

	if got := jitteredCooldownWait(0, time.Minute); got != 0 {
		t.Fatalf("expected zero wait to stay zero, got %v", got)
	}
	if got := jitteredCooldownWait(-time.Second, time.Minute); got != -time.Second {
		t.Fatalf("expected negative wait to pass through, got %v", got)
	}
	if got := jitteredCooldownWait(3, 0); got != 3 {
		t.Fatalf("expected sub-4ns wait to stay unchanged, got %v", got)
	}
}
