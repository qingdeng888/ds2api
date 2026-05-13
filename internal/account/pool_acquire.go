package account

import (
	"context"
	"math"
	"time"

	"ds2api/internal/config"
)

func (p *Pool) Acquire(target string, exclude map[string]bool) (config.Account, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acquireLocked(target, normalizeExclude(exclude))
}

func (p *Pool) AcquireWait(ctx context.Context, target string, exclude map[string]bool) (config.Account, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	exclude = normalizeExclude(exclude)
	for {
		if ctx.Err() != nil {
			return config.Account{}, false
		}

		p.mu.Lock()
		if acc, ok := p.acquireLocked(target, exclude); ok {
			p.mu.Unlock()
			return acc, true
		}
		if !p.canQueueLocked(target, exclude) {
			p.mu.Unlock()
			return config.Account{}, false
		}
		waiter := make(chan struct{})
		p.waiters = append(p.waiters, waiter)
		p.mu.Unlock()

		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.removeWaiterLocked(waiter)
			p.mu.Unlock()
			return config.Account{}, false
		case <-waiter:
		}
	}
}

func (p *Pool) acquireLocked(target string, exclude map[string]bool) (config.Account, bool) {
	if target != "" {
		// Pinned target: cooldown is intentionally bypassed because the
		// caller has explicitly opted out of automatic rotation. Slot caps
		// (per-account inflight + global) are still enforced.
		if exclude[target] || !p.canAcquireIDLocked(target) {
			return config.Account{}, false
		}
		acc, ok := p.store.FindAccount(target)
		if !ok {
			return config.Account{}, false
		}
		p.inUse[target]++
		p.bumpQueue(target)
		return acc, true
	}

	return p.tryAcquire(exclude)
}

// tryAcquire picks the next account to serve a non-pinned request.
//
// Selection strategy:
//  1. Build the eligible candidate set: respect exclude list and slot caps.
//  2. If the health layer is enabled, drop any candidate whose cooldown is
//     still active. If everything is cooled down we *unwind* and fall back
//     to the cooldown-included set so the service degrades gracefully —
//     better to serve from a partially-throttled account than 503.
//  3. If candidates are effectively equal in weight (within
//     weightTieEpsilon), pick the first in queue order. This preserves the
//     legacy round-robin semantics whenever the pool is healthy and keeps
//     the existing test suite stable.
//  4. Otherwise apply Power-of-Two-Choices: sample two distinct candidates
//     and pick the higher score. Ties at this stage break to queue order.
//
// score = effectiveWeight(account) * (1 - inUse/maxInflightPerAccount)
// which mildly biases against accounts that are already busy; the leading
// term still dominates.
func (p *Pool) tryAcquire(exclude map[string]bool) (config.Account, bool) {
	if len(p.queue) == 0 {
		return config.Account{}, false
	}
	now := p.now()

	primary := make([]string, 0, len(p.queue))
	fallback := make([]string, 0, len(p.queue))
	for _, id := range p.queue {
		if exclude[id] || !p.canAcquireIDLocked(id) {
			continue
		}
		fallback = append(fallback, id)
		if p.healthCfg.enabled {
			if h := p.health[id]; h != nil && h.cooldownRemaining(now) > 0 {
				continue
			}
		}
		primary = append(primary, id)
	}
	candidates := primary
	if len(candidates) == 0 {
		// Every eligible account is in cooldown — degrade gracefully and
		// pick from the full set rather than failing the request.
		candidates = fallback
	}
	if len(candidates) == 0 {
		return config.Account{}, false
	}

	id, ok := p.selectCandidate(candidates, now)
	if !ok {
		return config.Account{}, false
	}
	acc, ok := p.store.FindAccount(id)
	if !ok {
		return config.Account{}, false
	}
	p.inUse[id]++
	p.bumpQueue(id)
	return acc, true
}

// selectCandidate applies the equal-weight short-circuit + P2C decision.
// Must be called with p.mu held; the only randomness it consumes comes
// from p.rng so tests can seed deterministically.
func (p *Pool) selectCandidate(candidates []string, now time.Time) (string, bool) {
	switch len(candidates) {
	case 0:
		return "", false
	case 1:
		return candidates[0], true
	}

	// Health disabled: keep strict legacy round-robin (queue order).
	if !p.healthCfg.enabled {
		return candidates[0], true
	}

	// Equal-weight short-circuit: if every candidate scores within the tie
	// threshold of the first, fall through to queue order. This is the
	// healthy-pool common case and matches the existing round-robin
	// behaviour and tests.
	first := p.scoreLocked(candidates[0], now)
	allTied := true
	for i := 1; i < len(candidates); i++ {
		if math.Abs(p.scoreLocked(candidates[i], now)-first) > weightTieEpsilon {
			allTied = false
			break
		}
	}
	if allTied {
		return candidates[0], true
	}

	// P2C: pick two distinct candidates and keep the better one. Drawing
	// from the full eligible set (rather than e.g. the top-K) preserves
	// load distribution; tested in production by Envoy / nginx-plus.
	a := p.rng.Intn(len(candidates))
	b := p.rng.Intn(len(candidates))
	for b == a && len(candidates) > 1 {
		b = p.rng.Intn(len(candidates))
	}
	idA := candidates[a]
	idB := candidates[b]
	scoreA := p.scoreLocked(idA, now)
	scoreB := p.scoreLocked(idB, now)
	if scoreB > scoreA {
		return idB, true
	}
	if scoreA > scoreB {
		return idA, true
	}
	// Score tie at the P2C round → fall back to whichever is earlier in
	// queue order to keep "older waiter" semantics.
	for _, id := range candidates {
		if id == idA || id == idB {
			return id, true
		}
	}
	return idA, true
}

// scoreLocked returns the weighted-by-load score for a single account.
// Higher is better. Must be called with p.mu held.
func (p *Pool) scoreLocked(accountID string, now time.Time) float64 {
	weight := 1.0
	if p.healthCfg.enabled {
		if h := p.health[accountID]; h != nil {
			weight = h.effectiveWeight(p.healthCfg, now)
		}
	}
	if p.maxInflightPerAccount <= 0 {
		return weight
	}
	loadFactor := 1.0 - float64(p.inUse[accountID])/float64(p.maxInflightPerAccount)
	if loadFactor < 0 {
		loadFactor = 0
	}
	return weight * loadFactor
}

func (p *Pool) bumpQueue(accountID string) {
	for i, id := range p.queue {
		if id != accountID {
			continue
		}
		p.queue = append(p.queue[:i], p.queue[i+1:]...)
		p.queue = append(p.queue, accountID)
		return
	}
}

func normalizeExclude(exclude map[string]bool) map[string]bool {
	if exclude == nil {
		return map[string]bool{}
	}
	return exclude
}
