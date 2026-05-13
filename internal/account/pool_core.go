package account

import (
	"math/rand"
	"sort"
	"sync"
	"time"

	"ds2api/internal/config"
)

type Pool struct {
	store                  *config.Store
	mu                     sync.Mutex
	queue                  []string
	inUse                  map[string]int
	waiters                []chan struct{}
	maxInflightPerAccount  int
	recommendedConcurrency int
	maxQueueSize           int
	globalMaxInflight      int

	// Health tracking — per-account weight + cooldown state. Mutated only
	// under p.mu. Entries persist across Reset() so admin edits to the
	// account list (which trigger Reset) do not wipe a fresh 429 cooldown
	// and re-expose a still-throttled account.
	healthCfg healthConfig
	health    map[string]*accountHealth
	rng       *rand.Rand
	now       func() time.Time
}

func NewPool(store *config.Store) *Pool {
	maxPer := 2
	if store != nil {
		maxPer = store.RuntimeAccountMaxInflight()
	}
	p := &Pool{
		store:                 store,
		inUse:                 map[string]int{},
		maxInflightPerAccount: maxPer,
		health:                map[string]*accountHealth{},
		healthCfg:             DefaultHealthConfig().toInternal(),
		// Per-pool RNG seeded from time.Now keeps the P2C tie-break
		// non-deterministic without burdening callers with a test seam;
		// tests that need determinism set p.rng directly.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
		now: time.Now,
	}
	if store != nil {
		p.healthCfg = LoadHealthConfigFromStore(store).toInternal()
	}
	p.Reset()
	return p
}

func (p *Pool) Reset() {
	accounts := p.store.Accounts()
	sort.SliceStable(accounts, func(i, j int) bool {
		iHas := accounts[i].Token != ""
		jHas := accounts[j].Token != ""
		if iHas == jHas {
			return i < j
		}
		return iHas
	})
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		id := a.Identifier()
		if id != "" {
			ids = append(ids, id)
		}
	}
	if p.store != nil {
		p.maxInflightPerAccount = p.store.RuntimeAccountMaxInflight()
	} else {
		p.maxInflightPerAccount = maxInflightFromEnv()
	}
	recommended := defaultRecommendedConcurrency(len(ids), p.maxInflightPerAccount)
	queueLimit := maxQueueFromEnv(recommended)
	globalLimit := recommended
	if p.store != nil {
		queueLimit = p.store.RuntimeAccountMaxQueue(recommended)
		globalLimit = p.store.RuntimeGlobalMaxInflight(recommended)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drainWaitersLocked()
	p.queue = ids
	p.inUse = map[string]int{}
	p.recommendedConcurrency = recommended
	p.maxQueueSize = queueLimit
	p.globalMaxInflight = globalLimit
	if p.store != nil {
		p.healthCfg = LoadHealthConfigFromStore(p.store).toInternal()
	}
	p.pruneHealthLocked(ids)
	config.Logger.Info(
		"[init_account_queue] initialized",
		"total", len(ids),
		"max_inflight_per_account", p.maxInflightPerAccount,
		"global_max_inflight", p.globalMaxInflight,
		"recommended_concurrency", p.recommendedConcurrency,
		"max_queue_size", p.maxQueueSize,
		"health_enabled", p.healthCfg.enabled,
	)
}

func (p *Pool) Release(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	count := p.inUse[accountID]
	if count <= 0 {
		return
	}
	if count == 1 {
		delete(p.inUse, accountID)
		p.notifyWaiterLocked()
		return
	}
	p.inUse[accountID] = count - 1
	p.notifyWaiterLocked()
}

// Penalize records a failure against the given account and applies the
// configured cooldown / weight penalty. Calling with an empty accountID or
// PenaltyUnknown is a no-op (the latter so SwitchAccount can be invoked
// from legacy paths without recording a cooldown). When health is disabled
// via config, this is also a no-op so operators can opt out cleanly.
func (p *Pool) Penalize(accountID string, kind PenaltyKind) {
	if accountID == "" || kind == PenaltyUnknown {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.healthCfg.enabled {
		return
	}
	h := p.healthLocked(accountID)
	h.applyPenalty(p.healthCfg, kind, p.now())
}

// RecordSuccess clears the cooldown and resets the failure streak for the
// given account. It is the explicit "this account just produced a healthy
// response" signal called from Resolver.Release when no penalty was
// recorded earlier in the request lifecycle.
func (p *Pool) RecordSuccess(accountID string) {
	if accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.healthCfg.enabled {
		return
	}
	h, ok := p.health[accountID]
	if !ok {
		// Lazily seed so subsequent Status() reflects last_success_at.
		h = newAccountHealth()
		p.health[accountID] = h
	}
	h.recordSuccess(p.now())
	// Releasing a successful account should immediately wake a waiter — a
	// previously-cooled-down account becoming visible again is exactly the
	// signal the wait queue is interested in.
	p.notifyWaiterLocked()
}

// ApplyHealthConfig hot-swaps the health tunables without resetting any
// per-account state. Used by the admin settings handler.
func (p *Pool) ApplyHealthConfig(cfg HealthConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthCfg = cfg.toInternal()
	// Re-enabling health from a disabled state should not retroactively
	// punish accounts that "should" have cooldowns; existing entries simply
	// remain at weight=1.0 unless / until a fresh failure is recorded.
	p.notifyWaiterLocked()
}

// HealthEnabled reports whether the live config has the health layer
// switched on. Useful for tests and for admin UIs that surface a banner.
func (p *Pool) HealthEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.healthCfg.enabled
}

// healthLocked returns (and lazily creates) the health entry for the given
// account ID. Must be called with p.mu held.
func (p *Pool) healthLocked(accountID string) *accountHealth {
	if p.health == nil {
		p.health = map[string]*accountHealth{}
	}
	h, ok := p.health[accountID]
	if !ok {
		h = newAccountHealth()
		p.health[accountID] = h
	}
	return h
}

// pruneHealthLocked drops entries whose accounts no longer exist in the
// store. Called from Reset() so a deleted account does not leak its
// cooldown state forever.
func (p *Pool) pruneHealthLocked(currentIDs []string) {
	if len(p.health) == 0 {
		return
	}
	keep := make(map[string]struct{}, len(currentIDs))
	for _, id := range currentIDs {
		keep[id] = struct{}{}
	}
	for id := range p.health {
		if _, ok := keep[id]; !ok {
			delete(p.health, id)
		}
	}
}

// healthSnapshotLocked builds the per-account health view used by Status().
// Must be called with p.mu held. Output is sorted by ID for stable JSON.
func (p *Pool) healthSnapshotLocked(now time.Time) []map[string]any {
	if len(p.queue) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(p.queue))
	ids := append([]string(nil), p.queue...)
	sort.Strings(ids)
	for _, id := range ids {
		h := p.health[id]
		entry := map[string]any{
			"id":                 id,
			"in_use":             p.inUse[id],
			"weight":             1.0,
			"failure_count":      0,
			"cooldown_remaining": 0,
			"last_failure_kind":  "",
			"last_success_at":    int64(0),
			"last_failure_at":    int64(0),
		}
		if h != nil {
			entry["weight"] = roundToTwo(h.effectiveWeight(p.healthCfg, now))
			entry["failure_count"] = h.failureCount
			entry["cooldown_remaining"] = int(h.cooldownRemaining(now).Seconds())
			entry["last_failure_kind"] = string(h.lastFailureKind)
			if !h.lastSuccessAt.IsZero() {
				entry["last_success_at"] = h.lastSuccessAt.Unix()
			}
			if !h.lastFailureAt.IsZero() {
				entry["last_failure_at"] = h.lastFailureAt.Unix()
			}
		}
		out = append(out, entry)
	}
	return out
}

func roundToTwo(v float64) float64 {
	// Rounding to 2 decimals keeps the JSON readable in the admin UI
	// without leaking floating-point noise like 0.6000000000000001.
	return float64(int(v*100+0.5)) / 100
}

func (p *Pool) Status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	available := make([]string, 0, len(p.queue))
	inUseAccounts := make([]string, 0, len(p.inUse))
	inUseSlots := 0
	for _, id := range p.queue {
		if p.inUse[id] >= p.maxInflightPerAccount {
			continue
		}
		// Hide cooldown-suspended accounts from the "available" list so the
		// status JSON matches what the scheduler will actually pick.
		if p.healthCfg.enabled {
			if h := p.health[id]; h != nil && h.cooldownRemaining(now) > 0 {
				continue
			}
		}
		available = append(available, id)
	}
	for id, count := range p.inUse {
		if count > 0 {
			inUseAccounts = append(inUseAccounts, id)
			inUseSlots += count
		}
	}
	sort.Strings(inUseAccounts)
	return map[string]any{
		"available":                len(available),
		"in_use":                   inUseSlots,
		"total":                    len(p.store.Accounts()),
		"available_accounts":       available,
		"in_use_accounts":          inUseAccounts,
		"max_inflight_per_account": p.maxInflightPerAccount,
		"global_max_inflight":      p.globalMaxInflight,
		"recommended_concurrency":  p.recommendedConcurrency,
		"waiting":                  len(p.waiters),
		"max_queue_size":           p.maxQueueSize,
		"health_enabled":           p.healthCfg.enabled,
		"accounts":                 p.healthSnapshotLocked(now),
	}
}
