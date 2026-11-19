package auth

import (
	"testing"
	"time"
)

func TestRestoreCooldownSkipsOldWireConfigurationErrors(t *testing.T) {
	for _, tc := range []struct {
		name, provider, message string
		restore                 bool
	}{
		{"monitor", "antigravity", "unknown model: gemini-4-hydrogen-low(目录里的安全监控配置未核对 sha256=65f17f96d6858c47)", false},
		{"template", "antigravity", "unknown model: test(目录里的系统提示模板未核对)", false},
		{"upstream", "antigravity", "unknown model: removed-model", true},
		{"other-provider", "gemini", "unknown model: test(目录里的安全监控配置未核对)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.auths["test"] = &Auth{ID: "test", Provider: tc.provider, Status: StatusActive}
			now := time.Now()
			record := CooldownStateRecord{AuthID: "test", Model: "test-model", NextRetryAfter: now.Add(time.Hour), LastError: &Error{HTTPStatus: 404, Message: tc.message}}
			if got := manager.restoreCooldownRecordLocked(record, now); got != tc.restore {
				t.Fatalf("restored = %v, want %v", got, tc.restore)
			}
		})
	}
}
