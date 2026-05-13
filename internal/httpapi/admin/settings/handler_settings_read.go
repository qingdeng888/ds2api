package settings

import (
	"net/http"
	"strings"

	authn "ds2api/internal/auth"
	"ds2api/internal/config"
	"ds2api/internal/promptcompat"
)

func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	snap := h.Store.Snapshot()
	recommended := defaultRuntimeRecommended(len(snap.Accounts), h.Store.RuntimeAccountMaxInflight())
	needsSync := config.IsVercel() && snap.VercelSyncHash != "" && snap.VercelSyncHash != h.computeSyncHash()
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"admin": map[string]any{
			"has_password_hash":        strings.TrimSpace(snap.Admin.PasswordHash) != "",
			"jwt_expire_hours":         h.Store.AdminJWTExpireHours(),
			"jwt_valid_after_unix":     snap.Admin.JWTValidAfterUnix,
			"default_password_warning": authn.UsingDefaultAdminKey(h.Store),
		},
		"runtime": map[string]any{
			"account_max_inflight":                    h.Store.RuntimeAccountMaxInflight(),
			"account_max_queue":                       h.Store.RuntimeAccountMaxQueue(recommended),
			"global_max_inflight":                     h.Store.RuntimeGlobalMaxInflight(recommended),
			"token_refresh_interval_hours":            h.Store.RuntimeTokenRefreshIntervalHours(),
			"account_health_enabled":                  h.Store.AccountHealthEnabled(),
			"account_health_recovery_window_seconds":  effectiveHealthInt(h.Store.AccountHealthRecoveryWindowSeconds(), 300),
			"account_health_max_cooldown_seconds":     effectiveHealthInt(h.Store.AccountHealthMaxCooldownSeconds(), 1800),
			"account_health_cooldown_429_seconds":     effectiveHealthInt(h.Store.AccountHealthCooldown429Seconds(), 30),
			"account_health_cooldown_403_seconds":     effectiveHealthInt(h.Store.AccountHealthCooldown403Seconds(), 60),
			"account_health_cooldown_auth_seconds":    effectiveHealthInt(h.Store.AccountHealthCooldownAuthSeconds(), 120),
			"account_health_cooldown_5xx_seconds":     effectiveHealthInt(h.Store.AccountHealthCooldown5xxSeconds(), 10),
			"account_health_cooldown_network_seconds": effectiveHealthInt(h.Store.AccountHealthCooldownNetworkSeconds(), 5),
			"account_health_cooldown_empty_seconds":   effectiveHealthEmpty(h.Store.AccountHealthCooldownEmptySeconds()),
		},
		"responses":   snap.Responses,
		"embeddings":  snap.Embeddings,
		"auto_delete": snap.AutoDelete,
		"current_input_file": map[string]any{
			"enabled":   h.Store.CurrentInputFileEnabled(),
			"min_chars": h.Store.CurrentInputFileMinChars(),
		},
		"thinking_injection": map[string]any{
			"enabled":        h.Store.ThinkingInjectionEnabled(),
			"prompt":         h.Store.ThinkingInjectionPrompt(),
			"default_prompt": promptcompat.DefaultThinkingInjectionPrompt,
		},
		"model_aliases":     snap.ModelAliases,
		"env_backed":        h.Store.IsEnvBacked(),
		"needs_vercel_sync": needsSync,
	})
}

// effectiveHealthInt mirrors the fallback logic in
// account.LoadHealthConfigFromStore so the GET /admin/settings response
// reflects the value the scheduler will actually use, rather than the
// raw zero stored when the operator has never set the field.
func effectiveHealthInt(stored, fallback int) int {
	if stored > 0 {
		return stored
	}
	return fallback
}

// effectiveHealthEmpty handles the one knob (empty cooldown) where 0 is a
// legitimate value meaning "no hard cooldown for empty output" and only
// negatives fall back to the default.
func effectiveHealthEmpty(stored int) int {
	if stored < 0 {
		return 0
	}
	return stored
}
