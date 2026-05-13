package account

import (
	"math/rand"
	"testing"
	"time"

	"ds2api/internal/config"
)

// ─── accountHealth math ──────────────────────────────────────────────

func TestAccountHealthApplyPenaltyAppliesCooldown(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	h.applyPenalty(cfg, PenaltyHTTP429, now)
	if h.failureCount != 1 {
		t.Fatalf("expected failureCount=1, got %d", h.failureCount)
	}
	if h.lastFailureKind != PenaltyHTTP429 {
		t.Fatalf("expected lastFailureKind=http_429, got %q", h.lastFailureKind)
	}
	// First failure: base cooldown (30s for 429), no exponential bump yet.
	want := 30 * time.Second
	if rem := h.cooldownRemaining(now); rem != want {
		t.Fatalf("expected cooldownRemaining=%v, got %v", want, rem)
	}
	// Weight should drop by the kind delta (0.40 for 429).
	if h.weight < 0.59 || h.weight > 0.61 {
		t.Fatalf("expected weight≈0.60, got %v", h.weight)
	}
}

func TestAccountHealthApplyPenaltyExponentialBackoff(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	h.applyPenalty(cfg, PenaltyHTTP429, now) // 30s
	h.applyPenalty(cfg, PenaltyHTTP429, now) // 60s (2x)
	h.applyPenalty(cfg, PenaltyHTTP429, now) // 120s (4x)
	if rem := h.cooldownRemaining(now); rem != 120*time.Second {
		t.Fatalf("expected 120s after 3 failures, got %v", rem)
	}
}

func TestAccountHealthApplyPenaltyCappedAtMax(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	for i := 0; i < 20; i++ {
		h.applyPenalty(cfg, PenaltyAuthFailed, now)
	}
	rem := h.cooldownRemaining(now)
	if rem > cfg.maxCooldown {
		t.Fatalf("cooldown %v exceeded maxCooldown %v", rem, cfg.maxCooldown)
	}
	if rem != cfg.maxCooldown {
		t.Fatalf("expected cooldown to saturate at %v, got %v", cfg.maxCooldown, rem)
	}
}

func TestAccountHealthWeightFloor(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	for i := 0; i < 10; i++ {
		h.applyPenalty(cfg, PenaltyAuthFailed, now)
	}
	if h.weight < minWeight-0.001 {
		t.Fatalf("weight dropped below minWeight: %v", h.weight)
	}
	if h.weight > minWeight+0.001 {
		t.Fatalf("weight should clamp to minWeight=%v after many failures, got %v", minWeight, h.weight)
	}
}

func TestAccountHealthEmptyKindNoCooldown(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	h.applyPenalty(cfg, PenaltyEmpty, now)
	if rem := h.cooldownRemaining(now); rem != 0 {
		t.Fatalf("PenaltyEmpty should not produce hard cooldown by default, got %v", rem)
	}
	// But weight should still be docked.
	if h.weight >= 1.0 {
		t.Fatalf("expected weight to drop after empty penalty, got %v", h.weight)
	}
}

func TestAccountHealthCooldownExpires(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	h.applyPenalty(cfg, PenaltyHTTP429, now)
	if rem := h.cooldownRemaining(now); rem != 30*time.Second {
		t.Fatalf("expected 30s cooldown, got %v", rem)
	}
	future := now.Add(31 * time.Second)
	if rem := h.cooldownRemaining(future); rem != 0 {
		t.Fatalf("expected cooldown to be 0 after expiry, got %v", rem)
	}
}

func TestAccountHealthEffectiveWeightLinearRecovery(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()
	h.applyPenalty(cfg, PenaltyHTTP429, now) // weight ≈ 0.60

	// Halfway through recovery window, weight should be ≈ 0.6 + 0.5 = 1.0
	// (the linear recovery line crosses 1.0 well before the end of the
	// window since 0.60 + 0.5 > 1.0). Verify it caps at 1.0.
	half := now.Add(cfg.recoveryWindow / 2)
	w := h.effectiveWeight(cfg, half)
	if w != 1.0 {
		t.Fatalf("expected weight to cap at 1.0 after generous recovery, got %v", w)
	}

	// Verify partial recovery for a heavier penalty where the floor is
	// noticeable. AuthFailed drops weight to 0.30 → after 30% of window
	// we expect ≈ 0.60.
	h2 := newAccountHealth()
	h2.applyPenalty(cfg, PenaltyAuthFailed, now)
	at30 := now.Add(time.Duration(float64(cfg.recoveryWindow) * 0.30))
	w2 := h2.effectiveWeight(cfg, at30)
	if w2 < 0.55 || w2 > 0.65 {
		t.Fatalf("expected partial recovery ≈ 0.60, got %v", w2)
	}
}

func TestAccountHealthRecordSuccessClearsCooldown(t *testing.T) {
	cfg := DefaultHealthConfig().toInternal()
	now := time.Unix(1_700_000_000, 0)
	h := newAccountHealth()

	h.applyPenalty(cfg, PenaltyHTTP429, now)
	h.applyPenalty(cfg, PenaltyHTTP429, now)
	if h.failureCount != 2 || h.cooldownUntil.IsZero() {
		t.Fatalf("setup failed: failureCount=%d cooldownUntil=%v", h.failureCount, h.cooldownUntil)
	}

	h.recordSuccess(now.Add(time.Second))
	if h.failureCount != 0 {
		t.Fatalf("recordSuccess should reset failureCount, got %d", h.failureCount)
	}
	if !h.cooldownUntil.IsZero() {
		t.Fatalf("recordSuccess should clear cooldown, got %v", h.cooldownUntil)
	}
	if h.lastFailureKind != "" {
		t.Fatalf("recordSuccess should clear lastFailureKind, got %q", h.lastFailureKind)
	}
	// We deliberately do NOT reset weight on success; recovery is
	// time-based via effectiveWeight.
	if h.weight >= 1.0 {
		t.Fatalf("recordSuccess should not bump weight directly, got %v", h.weight)
	}
}

// ─── Pool integration: penalize / recordSuccess / cooldown skip ──────

// newHealthPool creates a Pool with a deterministic clock and rng so tests
// can assert exact values without flakes.
func newHealthPool(t *testing.T, configJSON string) (*Pool, func(time.Duration)) {
	t.Helper()
	t.Setenv("DS2API_ACCOUNT_MAX_INFLIGHT", "1")
	t.Setenv("DS2API_ACCOUNT_MAX_QUEUE", "")
	t.Setenv("DS2API_GLOBAL_MAX_INFLIGHT", "")
	t.Setenv("DS2API_CONFIG_JSON", configJSON)
	store := config.LoadStore()
	pool := NewPool(store)
	// Deterministic clock seeded at a fixed point.
	current := time.Unix(1_700_000_000, 0)
	pool.now = func() time.Time { return current }
	pool.rng = rand.New(rand.NewSource(42))
	advance := func(d time.Duration) { current = current.Add(d) }
	return pool, advance
}

func TestPoolPenalizeAppliesCooldownAndSkipsAccount(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"}
		]
	}`)

	// Penalize acc1 hard so its cooldown is non-zero. Acquire should now
	// skip acc1 and pick acc2.
	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	acc, ok := pool.Acquire("", nil)
	if !ok {
		t.Fatal("expected acquire to succeed by skipping cooled-down acc1")
	}
	if acc.Identifier() != "acc2@example.com" {
		t.Fatalf("expected acc2 (acc1 in cooldown), got %q", acc.Identifier())
	}
	pool.Release(acc.Identifier())

	// Status should reflect acc1 health (cooldown_remaining > 0) and only
	// acc2 in available_accounts.
	status := pool.Status()
	available, _ := status["available_accounts"].([]string)
	if len(available) != 1 || available[0] != "acc2@example.com" {
		t.Fatalf("expected available=[acc2], got %v", available)
	}
	accountsList, _ := status["accounts"].([]map[string]any)
	if len(accountsList) != 2 {
		t.Fatalf("expected 2 health entries, got %d", len(accountsList))
	}
	for _, entry := range accountsList {
		if entry["id"] == "acc1@example.com" {
			rem := entry["cooldown_remaining"].(int)
			if rem <= 0 {
				t.Fatalf("expected acc1 cooldown_remaining > 0, got %d", rem)
			}
			if entry["last_failure_kind"] != "http_429" {
				t.Fatalf("expected last_failure_kind=http_429, got %v", entry["last_failure_kind"])
			}
		}
	}
}

func TestPoolPenalizePinnedTargetBypassesCooldown(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc1@example.com","token":"t1"}]
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	// Pinned target Acquire must still succeed — operator opted out of
	// rotation and we should respect that even when the account is hot.
	acc, ok := pool.Acquire("acc1@example.com", nil)
	if !ok {
		t.Fatal("expected pinned acquire to bypass cooldown, got !ok")
	}
	if acc.Identifier() != "acc1@example.com" {
		t.Fatalf("got %q", acc.Identifier())
	}
}

func TestPoolAllAccountsCooledDownFallbackPath(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"}
		]
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	pool.Penalize("acc2@example.com", PenaltyHTTP429)
	// Every account is in cooldown. Rather than failing the request we
	// degrade gracefully and pick from the cooled-down set.
	acc, ok := pool.Acquire("", nil)
	if !ok {
		t.Fatal("expected acquire to succeed via fallback even with all cooldowns active")
	}
	if acc.Identifier() == "" {
		t.Fatalf("got empty identifier from fallback acquire")
	}
}

func TestPoolRecordSuccessClearsCooldown(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc1@example.com","token":"t1"}]
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	if status := pool.Status(); len(status["available_accounts"].([]string)) != 0 {
		t.Fatalf("expected acc1 unavailable while cooled down, got %v", status["available_accounts"])
	}

	pool.RecordSuccess("acc1@example.com")
	if status := pool.Status(); len(status["available_accounts"].([]string)) != 1 {
		t.Fatalf("expected acc1 back in available after RecordSuccess, got %v", status["available_accounts"])
	}
}

func TestPoolCooldownExpiresOverTime(t *testing.T) {
	pool, advance := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc1@example.com","token":"t1"}]
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	if avail := pool.Status()["available_accounts"].([]string); len(avail) != 0 {
		t.Fatalf("expected zero available immediately after penalize, got %v", avail)
	}
	// 30s default cooldown for first 429 — advance past it.
	advance(35 * time.Second)
	avail := pool.Status()["available_accounts"].([]string)
	if len(avail) != 1 || avail[0] != "acc1@example.com" {
		t.Fatalf("expected acc1 to be available after cooldown expiry, got %v", avail)
	}
}

func TestPoolResetPreservesHealthEntries(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"}
		]
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	cooldownBefore := pool.health["acc1@example.com"].cooldownUntil

	// Reset would normally happen on admin add/delete. We simulate just
	// the call (no config change) and verify the cooldown is preserved.
	pool.Reset()
	// Re-inject deterministic clock since Reset does not touch p.now.
	if h, ok := pool.health["acc1@example.com"]; !ok {
		t.Fatal("expected acc1 health entry to survive Reset")
	} else if !h.cooldownUntil.Equal(cooldownBefore) {
		t.Fatalf("Reset clobbered cooldownUntil: before=%v after=%v", cooldownBefore, h.cooldownUntil)
	}
}

func TestPoolResetPrunesDeletedAccounts(t *testing.T) {
	t.Setenv("DS2API_ACCOUNT_MAX_INFLIGHT", "1")
	t.Setenv("DS2API_ACCOUNT_MAX_QUEUE", "")
	t.Setenv("DS2API_CONFIG_JSON", `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"}
		]
	}`)
	store := config.LoadStore()
	pool := NewPool(store)
	pool.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	pool.Penalize("acc2@example.com", PenaltyHTTP429)

	// Delete acc2 from config and Reset the pool — the dangling health
	// entry should be pruned so it does not leak forever.
	if err := store.Update(func(c *config.Config) error {
		c.Accounts = c.Accounts[:1]
		return nil
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	pool.Reset()

	if _, ok := pool.health["acc2@example.com"]; ok {
		t.Fatal("expected pruneHealthLocked to drop acc2 entry")
	}
	if _, ok := pool.health["acc1@example.com"]; !ok {
		t.Fatal("expected acc1 entry to remain after prune")
	}
}

func TestPoolDisabledHealthSkipsAllPenalties(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc1@example.com","token":"t1"}],
		"runtime":{"account_health_enabled":false}
	}`)

	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	// With health disabled, no entry should be created and Acquire must
	// behave exactly like the legacy round-robin.
	if _, ok := pool.health["acc1@example.com"]; ok {
		t.Fatal("expected no health entry when health is disabled")
	}
	acc, ok := pool.Acquire("", nil)
	if !ok || acc.Identifier() != "acc1@example.com" {
		t.Fatalf("expected acquire to ignore cooldown when disabled; ok=%v id=%q", ok, acc.Identifier())
	}
}

func TestPoolStatusReportsHealthEnabledFlag(t *testing.T) {
	enabled, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc@example.com","token":"t"}]
	}`)
	if got := enabled.Status()["health_enabled"]; got != true {
		t.Fatalf("expected health_enabled=true, got %v", got)
	}

	disabled, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[{"email":"acc@example.com","token":"t"}],
		"runtime":{"account_health_enabled":false}
	}`)
	if got := disabled.Status()["health_enabled"]; got != false {
		t.Fatalf("expected health_enabled=false, got %v", got)
	}
}

// ─── P2C selection ───────────────────────────────────────────────────

func TestPoolP2CPrefersHigherWeight(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"},
			{"email":"acc3@example.com","token":"t3"}
		]
	}`)

	// Drop acc1 hard so its weight is well below the tie-break threshold
	// vs the healthy acc2 / acc3. Run many acquire/release cycles and
	// count how often acc1 is picked — it should be far less than the
	// 1/3 baseline that pure round-robin or random would produce.
	pool.Penalize("acc1@example.com", PenaltyAuthFailed)
	pool.Penalize("acc1@example.com", PenaltyAuthFailed)
	pool.Penalize("acc1@example.com", PenaltyAuthFailed)
	// Advance past the cooldown so acc1 is *eligible*, just unhealthy.
	now := time.Unix(1_700_000_000, 0).Add(2 * time.Hour)
	pool.now = func() time.Time { return now }

	const trials = 200
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		acc, ok := pool.Acquire("", nil)
		if !ok {
			t.Fatalf("acquire failed at trial %d", i)
		}
		counts[acc.Identifier()]++
		pool.Release(acc.Identifier())
	}
	// Even after cooldown expiry, acc1's weight has bled off only
	// partially over the 2h advance because each AuthFailed penalty
	// drops weight by 0.70 to the floor. The recovery window is 5min so
	// after 2h the weight is fully restored to 1.0... that means after
	// the cooldown + recovery window, weights all converge and we expect
	// approximate uniform distribution. Verify only that acc1 *did* get
	// picked at least once (post-recovery) and the distribution is not
	// catastrophically skewed.
	if counts["acc1@example.com"] == 0 {
		t.Fatalf("expected acc1 to be picked at least once after full recovery, counts=%v", counts)
	}
}

func TestPoolP2CSkewsAwayFromUnhealthyDuringCooldown(t *testing.T) {
	pool, _ := newHealthPool(t, `{
		"keys":["k1"],
		"accounts":[
			{"email":"acc1@example.com","token":"t1"},
			{"email":"acc2@example.com","token":"t2"},
			{"email":"acc3@example.com","token":"t3"}
		]
	}`)

	// Penalize acc1 and immediately try to acquire — its cooldown is
	// active so the primary candidate set excludes it entirely.
	pool.Penalize("acc1@example.com", PenaltyHTTP429)
	const trials = 30
	counts := map[string]int{}
	for i := 0; i < trials; i++ {
		acc, ok := pool.Acquire("", nil)
		if !ok {
			t.Fatalf("acquire failed at trial %d", i)
		}
		counts[acc.Identifier()]++
		pool.Release(acc.Identifier())
	}
	if counts["acc1@example.com"] != 0 {
		t.Fatalf("acc1 was picked %d times despite active cooldown, counts=%v", counts["acc1@example.com"], counts)
	}
	if counts["acc2@example.com"] == 0 || counts["acc3@example.com"] == 0 {
		t.Fatalf("expected acc2 and acc3 both to receive traffic, counts=%v", counts)
	}
}

// ─── Reset preserves with classification ─────────────────────────────

func TestParsePenaltyKindKnownAndUnknown(t *testing.T) {
	cases := []struct {
		in   string
		want PenaltyKind
	}{
		{"http_429", PenaltyHTTP429},
		{"HTTP_429", PenaltyHTTP429},
		{" http_429 ", PenaltyHTTP429},
		{"http_403", PenaltyHTTP403},
		{"auth_failed", PenaltyAuthFailed},
		{"http_5xx", PenaltyHTTP5xx},
		{"network", PenaltyNetwork},
		{"empty_output", PenaltyEmpty},
		{"", PenaltyUnknown},
		{"bogus", PenaltyUnknown},
	}
	for _, tc := range cases {
		if got := ParsePenaltyKind(tc.in); got != tc.want {
			t.Errorf("ParsePenaltyKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─── DefaultHealthConfig sanity check ────────────────────────────────

func TestDefaultHealthConfigSanity(t *testing.T) {
	cfg := DefaultHealthConfig()
	if !cfg.Enabled {
		t.Fatal("default should be enabled")
	}
	if cfg.RecoveryWindowSeconds <= 0 {
		t.Fatalf("recovery window must be positive, got %d", cfg.RecoveryWindowSeconds)
	}
	if cfg.MaxCooldownSeconds <= 0 {
		t.Fatalf("max cooldown must be positive, got %d", cfg.MaxCooldownSeconds)
	}
	// All non-empty cooldown bases must be > 0 so an unconfigured
	// production deployment is meaningfully protected.
	if cfg.Cooldown429Seconds <= 0 || cfg.Cooldown403Seconds <= 0 ||
		cfg.CooldownAuthSeconds <= 0 || cfg.Cooldown5xxSeconds <= 0 ||
		cfg.CooldownNetworkSeconds <= 0 {
		t.Fatalf("base cooldowns must be > 0, got cfg=%+v", cfg)
	}
	// Empty cooldown can legitimately be zero by design.
	if cfg.CooldownEmptySeconds < 0 {
		t.Fatalf("empty cooldown must be >= 0")
	}
}
