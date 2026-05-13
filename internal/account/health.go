package account

import (
	"math"
	"strings"
	"time"
)

// PenaltyKind classifies why an account is being demoted so the cooldown
// scheduler can pick the right base duration and weight delta. The zero
// value (PenaltyUnknown) is treated as "rotate but do not record cooldown",
// which keeps the legacy SwitchAccount semantics intact for callers that
// have not been updated to classify failures.
type PenaltyKind string

const (
	// PenaltyUnknown carries no cooldown impact. Use for legacy paths.
	PenaltyUnknown PenaltyKind = ""
	// PenaltyHTTP429 — upstream rate limited the account.
	PenaltyHTTP429 PenaltyKind = "http_429"
	// PenaltyHTTP403 — captcha / risk-control / forbidden response.
	PenaltyHTTP403 PenaltyKind = "http_403"
	// PenaltyAuthFailed — re-login or token refresh definitively failed.
	PenaltyAuthFailed PenaltyKind = "auth_failed"
	// PenaltyHTTP5xx — upstream server error attributable to this account.
	PenaltyHTTP5xx PenaltyKind = "http_5xx"
	// PenaltyNetwork — network or transport-level failure.
	PenaltyNetwork PenaltyKind = "network"
	// PenaltyEmpty — model produced empty output across allowed retries.
	// No cooldown by default — relies on weight-bias only so we do not
	// shadow-ban an account on a single fluky empty response.
	PenaltyEmpty PenaltyKind = "empty_output"
)

// HealthConfig is the public, serialisation-friendly configuration surface
// that Pool consumes via ApplyHealthConfig. Durations are in seconds so the
// settings layer (which lives in JSON) can pass them through unchanged.
type HealthConfig struct {
	Enabled                bool
	RecoveryWindowSeconds  int
	MaxCooldownSeconds     int
	Cooldown429Seconds     int
	Cooldown403Seconds     int
	CooldownAuthSeconds    int
	Cooldown5xxSeconds     int
	CooldownNetworkSeconds int
	CooldownEmptySeconds   int
}

// DefaultHealthConfig returns the recommended starting tunables. They favour
// a conservative cooldown that protects accounts against rapid re-acquire
// after a 429 while still letting healthy accounts soak up the queue.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Enabled:                true,
		RecoveryWindowSeconds:  300,  // 5 min from min weight back to 1.0
		MaxCooldownSeconds:     1800, // 30 min upper bound on any single cooldown
		Cooldown429Seconds:     30,
		Cooldown403Seconds:     60,
		CooldownAuthSeconds:    120,
		Cooldown5xxSeconds:     10,
		CooldownNetworkSeconds: 5,
		CooldownEmptySeconds:   0, // weight-only; no hard skip
	}
}

// HealthConfigReader is the slice of *config.Store the account package needs
// to build a HealthConfig. Defining the interface here avoids an import
// cycle (config -> account -> config).
type HealthConfigReader interface {
	AccountHealthEnabled() bool
	AccountHealthRecoveryWindowSeconds() int
	AccountHealthMaxCooldownSeconds() int
	AccountHealthCooldown429Seconds() int
	AccountHealthCooldown403Seconds() int
	AccountHealthCooldownAuthSeconds() int
	AccountHealthCooldown5xxSeconds() int
	AccountHealthCooldownNetworkSeconds() int
	AccountHealthCooldownEmptySeconds() int
}

// LoadHealthConfigFromStore translates a config.Store (or any compatible
// reader) into the public HealthConfig. Missing fields fall back to the
// defaults provided by DefaultHealthConfig so partial JSON does not produce
// a zeroed-out, dysfunctional configuration.
func LoadHealthConfigFromStore(r HealthConfigReader) HealthConfig {
	if r == nil {
		return DefaultHealthConfig()
	}
	def := DefaultHealthConfig()
	cfg := HealthConfig{Enabled: r.AccountHealthEnabled()}
	cfg.RecoveryWindowSeconds = pickPositive(r.AccountHealthRecoveryWindowSeconds(), def.RecoveryWindowSeconds)
	cfg.MaxCooldownSeconds = pickPositive(r.AccountHealthMaxCooldownSeconds(), def.MaxCooldownSeconds)
	cfg.Cooldown429Seconds = pickPositive(r.AccountHealthCooldown429Seconds(), def.Cooldown429Seconds)
	cfg.Cooldown403Seconds = pickPositive(r.AccountHealthCooldown403Seconds(), def.Cooldown403Seconds)
	cfg.CooldownAuthSeconds = pickPositive(r.AccountHealthCooldownAuthSeconds(), def.CooldownAuthSeconds)
	cfg.Cooldown5xxSeconds = pickPositive(r.AccountHealthCooldown5xxSeconds(), def.Cooldown5xxSeconds)
	cfg.CooldownNetworkSeconds = pickPositive(r.AccountHealthCooldownNetworkSeconds(), def.CooldownNetworkSeconds)
	if r.AccountHealthCooldownEmptySeconds() >= 0 {
		// Empty is the only kind allowed to be zero by design.
		cfg.CooldownEmptySeconds = r.AccountHealthCooldownEmptySeconds()
	} else {
		cfg.CooldownEmptySeconds = def.CooldownEmptySeconds
	}
	return cfg
}

func pickPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// healthConfig is the internal representation used by Pool. It mirrors
// HealthConfig but stores time.Duration so we never need to repeatedly
// multiply seconds at the hot path.
type healthConfig struct {
	enabled             bool
	recoveryWindow      time.Duration
	maxCooldown         time.Duration
	baseCooldown429     time.Duration
	baseCooldown403     time.Duration
	baseCooldownAuth    time.Duration
	baseCooldown5xx     time.Duration
	baseCooldownNetwork time.Duration
	baseCooldownEmpty   time.Duration
}

func (c HealthConfig) toInternal() healthConfig {
	return healthConfig{
		enabled:             c.Enabled,
		recoveryWindow:      time.Duration(c.RecoveryWindowSeconds) * time.Second,
		maxCooldown:         time.Duration(c.MaxCooldownSeconds) * time.Second,
		baseCooldown429:     time.Duration(c.Cooldown429Seconds) * time.Second,
		baseCooldown403:     time.Duration(c.Cooldown403Seconds) * time.Second,
		baseCooldownAuth:    time.Duration(c.CooldownAuthSeconds) * time.Second,
		baseCooldown5xx:     time.Duration(c.Cooldown5xxSeconds) * time.Second,
		baseCooldownNetwork: time.Duration(c.CooldownNetworkSeconds) * time.Second,
		baseCooldownEmpty:   time.Duration(c.CooldownEmptySeconds) * time.Second,
	}
}

// minWeight prevents a heavily-penalised account from becoming permanently
// invisible. Even at the floor, the account is still selectable when it is
// the only candidate left after cooldown filtering.
const minWeight = 0.05

// weightTieEpsilon defines "indistinguishable" for the P2C tie-break path.
// When all eligible accounts score within this band, we fall back to queue
// order, which preserves the legacy round-robin behaviour for healthy
// pools and keeps the existing test expectations intact.
const weightTieEpsilon = 0.05

// weightDeltaByKind maps each penalty kind to how much we deduct from the
// stored weight. Heavier deductions take longer to bleed off via
// time-based recovery and dominate the P2C tie-break for longer.
func weightDeltaByKind(kind PenaltyKind) float64 {
	switch kind {
	case PenaltyHTTP429:
		return 0.40
	case PenaltyHTTP403:
		return 0.60
	case PenaltyAuthFailed:
		return 0.70
	case PenaltyHTTP5xx:
		return 0.20
	case PenaltyNetwork:
		return 0.10
	case PenaltyEmpty:
		return 0.10
	default:
		return 0.20
	}
}

func baseCooldownByKind(cfg healthConfig, kind PenaltyKind) time.Duration {
	switch kind {
	case PenaltyHTTP429:
		return cfg.baseCooldown429
	case PenaltyHTTP403:
		return cfg.baseCooldown403
	case PenaltyAuthFailed:
		return cfg.baseCooldownAuth
	case PenaltyHTTP5xx:
		return cfg.baseCooldown5xx
	case PenaltyNetwork:
		return cfg.baseCooldownNetwork
	case PenaltyEmpty:
		return cfg.baseCooldownEmpty
	default:
		return cfg.baseCooldown5xx
	}
}

// accountHealth tracks per-account scheduling state. It is mutated only
// under Pool.mu. Methods are pointer-receiver but tolerate nil receivers
// so that callers can lazily probe entries that have never recorded a
// failure.
type accountHealth struct {
	weight          float64
	failureCount    int
	lastFailureAt   time.Time
	lastFailureKind PenaltyKind
	lastSuccessAt   time.Time
	cooldownUntil   time.Time
}

func newAccountHealth() *accountHealth {
	return &accountHealth{weight: 1.0}
}

// effectiveWeight returns the recovery-adjusted score at the given moment.
// Past failures bleed off linearly toward 1.0 over RecoveryWindow once the
// cooldown has elapsed; the floor at minWeight ensures every account
// remains selectable as a last resort.
func (h *accountHealth) effectiveWeight(cfg healthConfig, now time.Time) float64 {
	if h == nil {
		return 1.0
	}
	w := h.weight
	if w < minWeight {
		w = minWeight
	}
	if w >= 1.0 {
		return 1.0
	}
	if h.lastFailureAt.IsZero() || cfg.recoveryWindow <= 0 {
		return w
	}
	elapsed := now.Sub(h.lastFailureAt)
	if elapsed <= 0 {
		return w
	}
	recovered := w + elapsed.Seconds()/cfg.recoveryWindow.Seconds()
	if recovered > 1.0 {
		return 1.0
	}
	return recovered
}

// cooldownRemaining reports how much longer the account is shadow-banned
// from the regular selection path. Returns 0 when the cooldown has
// elapsed or was never set.
func (h *accountHealth) cooldownRemaining(now time.Time) time.Duration {
	if h == nil || h.cooldownUntil.IsZero() {
		return 0
	}
	if !h.cooldownUntil.After(now) {
		return 0
	}
	return h.cooldownUntil.Sub(now)
}

// applyPenalty mutates the entry to reflect a fresh failure. Cooldown
// follows exponential backoff (2^failureCount-1) bounded by MaxCooldown;
// weight is dropped by the kind-specific delta but never below minWeight.
func (h *accountHealth) applyPenalty(cfg healthConfig, kind PenaltyKind, now time.Time) {
	h.failureCount++
	h.lastFailureAt = now
	h.lastFailureKind = kind
	h.weight -= weightDeltaByKind(kind)
	if h.weight < minWeight {
		h.weight = minWeight
	}
	base := baseCooldownByKind(cfg, kind)
	if base <= 0 {
		// kinds with zero base cooldown (e.g. empty output) bump the weight
		// but do not produce a hard skip; they rely on weight bias only.
		return
	}
	exp := math.Pow(2, float64(h.failureCount-1))
	if exp > 256 {
		exp = 256
	}
	cooldown := time.Duration(float64(base) * exp)
	if cfg.maxCooldown > 0 && cooldown > cfg.maxCooldown {
		cooldown = cfg.maxCooldown
	}
	h.cooldownUntil = now.Add(cooldown)
}

// recordSuccess clears active penalties and resets the failure streak.
// We deliberately leave `weight` alone — the time-based recovery in
// effectiveWeight() heals it back to 1.0 over RecoveryWindow, which keeps
// a previously-unhealthy account at a slight tie-break disadvantage even
// after one good response.
func (h *accountHealth) recordSuccess(now time.Time) {
	if h == nil {
		return
	}
	h.failureCount = 0
	h.lastSuccessAt = now
	h.cooldownUntil = time.Time{}
	h.lastFailureKind = ""
}

// ParsePenaltyKind translates a free-form input from configs/admin
// payloads into a known PenaltyKind. Unrecognised input maps to
// PenaltyUnknown so callers cannot accidentally inject ad-hoc kinds.
func ParsePenaltyKind(raw string) PenaltyKind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(PenaltyHTTP429):
		return PenaltyHTTP429
	case string(PenaltyHTTP403):
		return PenaltyHTTP403
	case string(PenaltyAuthFailed):
		return PenaltyAuthFailed
	case string(PenaltyHTTP5xx):
		return PenaltyHTTP5xx
	case string(PenaltyNetwork):
		return PenaltyNetwork
	case string(PenaltyEmpty):
		return PenaltyEmpty
	}
	return PenaltyUnknown
}
