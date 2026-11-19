package executor

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var antigravityPresenceOnce sync.Once
var antigravityClientManager atomic.Pointer[cliproxyauth.Manager]

// StartAntigravityPresence runs one account sweep per CPA process, not per request.
func StartAntigravityPresence(ctx context.Context, cfg *config.Config, manager *cliproxyauth.Manager) {
	if manager == nil {
		return
	}
	antigravityClientManager.Store(manager)
	helps.StartAntigravityBackground(ctx)
	interval := 60 * time.Second
	if value, err := strconv.ParseFloat(os.Getenv("ANTIGRAVITY_PRESENCE_INTERVAL"), 64); err == nil {
		interval = time.Duration(value * float64(time.Second))
	}
	if interval <= 0 {
		return
	}
	workers := 8
	if value, err := strconv.Atoi(os.Getenv("ANTIGRAVITY_PRESENCE_CONCURRENCY")); err == nil && value > 0 {
		workers = min(value, 64)
	}
	antigravityPresenceOnce.Do(func() {
		go func() {
			initial := time.NewTimer(time.Duration(3+rand.Float64()*5) * time.Second)
			select {
			case <-initial.C:
			case <-ctx.Done():
				initial.Stop()
				return
			}
			seen := make(map[string]bool)
			for cycle := 0; ctx.Err() == nil; cycle++ {
				start := time.Now()
				client := antigravityBaseHTTPClient(ctx, cfg, nil, 0)
				probe, errReq := http.NewRequestWithContext(ctx, http.MethodGet, antigravityBaseURLDaily+"/v1internal/", nil)
				if errReq == nil {
					probe.Header.Set("User-Agent", resolveUserAgent(nil))
					if resp, errDo := client.Do(probe); errDo == nil {
						_, _ = io.Copy(io.Discard, resp.Body)
						if errClose := resp.Body.Close(); errClose != nil {
							log.Debugf("antigravity: presence probe close: %v", errClose)
						}
					}
				}
				jobs := make(chan *cliproxyauth.Auth)
				var wg sync.WaitGroup
				var seenMu sync.Mutex
				for range workers {
					wg.Go(func() {
						for auth := range jobs {
							seenMu.Lock()
							first := !seen[auth.ID]
							seen[auth.ID] = true
							seenMu.Unlock()
							NewAntigravityExecutor(cfg).visitPresence(ctx, manager, auth, first, cycle)
						}
					})
				}
				for _, auth := range manager.List() {
					if auth.Disabled || !strings.EqualFold(auth.Provider, "antigravity") {
						continue
					}
					select {
					case jobs <- auth:
					case <-ctx.Done():
					}
				}
				close(jobs)
				wg.Wait()
				delay := max(time.Second, time.Duration(float64(interval)*(0.85+rand.Float64()*0.3))-time.Since(start))
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
		}()
	})
}

func (e *AntigravityExecutor) visitPresence(ctx context.Context, manager *cliproxyauth.Manager, auth *cliproxyauth.Auth, first bool, cycle int) {
	token := metaStringValue(auth.Metadata, "access_token")
	if token == "" || !tokenExpiry(auth.Metadata).After(time.Now()) {
		updated, errRefresh := e.Refresh(ctx, auth)
		if errRefresh != nil || updated == nil {
			return
		}
		auth = updated
		token = metaStringValue(auth.Metadata, "access_token")
		if _, errUpdate := manager.Update(ctx, auth); errUpdate != nil {
			log.Debugf("antigravity: presence token save failed: %v", errUpdate)
		}
	}
	project := antigravityProjectIDFromAuth(auth)
	if project == "" {
		return
	}
	client := antigravityBaseHTTPClient(ctx, e.cfg, auth, 0)
	base := buildBaseURL(auth)
	headers := make(http.Header)
	headers.Set("User-Agent", resolveUserAgent(auth))
	headers.Set("Authorization", "Bearer "+token)
	if first {
		helps.AntigravityBackgroundPost(ctx, client.Transport, base, "fetchUserInfo", headers, map[string]any{"project": project})
	}
	status := helps.AntigravityBackgroundPost(ctx, client.Transport, base, "listExperiments", headers,
		map[string]any{"project": project, "metadata": map[string]any{"updateChannel": "stable"}})
	if status == http.StatusUnauthorized {
		updated, errRefresh := e.Refresh(ctx, auth)
		if errRefresh == nil && updated != nil {
			if _, errUpdate := manager.Update(ctx, updated); errUpdate != nil {
				log.Debugf("antigravity: save refreshed presence auth: %v", errUpdate)
			}
			headers.Set("Authorization", "Bearer "+metaStringValue(updated.Metadata, "access_token"))
			helps.AntigravityBackgroundPost(ctx, client.Transport, base, "listExperiments", headers,
				map[string]any{"project": project, "metadata": map[string]any{"updateChannel": "stable"}})
		}
	}
	if cycle%5 == 0 {
		helps.AntigravityBackgroundPost(ctx, client.Transport, base, "fetchAvailableModels", headers, map[string]any{"project": project})
	}
}
