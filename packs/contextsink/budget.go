package contextsink

import (
	"sync"
	"time"
)

// recoveryBudget tracks cumulative bytes returned by context_search for one
// session. Each session has its own mutex so unrelated sessions search in
// parallel; only the same session serializes budget updates.
type recoveryBudget struct {
	mu       sync.Mutex
	used     int
	lastUsed time.Time
}

var (
	budgetRegistryMu sync.Mutex
	budgetByKey      = map[string]*recoveryBudget{}
	budgetLRU        []string // oldest at index 0
)

func getRecoveryBudget(key string) *recoveryBudget {
	budgetRegistryMu.Lock()
	defer budgetRegistryMu.Unlock()
	if b, ok := budgetByKey[key]; ok {
		touchBudgetLRU(key)
		return b
	}
	b := &recoveryBudget{lastUsed: time.Now()}
	budgetByKey[key] = b
	budgetLRU = append(budgetLRU, key)
	evictBudgetLRUIfNeeded(key)
	return b
}

// acquireRecoveryBudget returns the per-session budget with its mutex held.
//
// Lock order: never block on b.mu while holding budgetRegistryMu.
//   - registry then TryLock(b.mu) is allowed (eviction)
//   - b.mu then registry is allowed (this function's identity check, chargeBudgetLocked)
//
// Eviction skips in-flight entries (TryLock fail) so a search cannot be
// detached from the map (which would reset its cumulative budget).
func acquireRecoveryBudget(key string) (b *recoveryBudget, release func()) {
	for {
		budgetRegistryMu.Lock()
		b, ok := budgetByKey[key]
		if !ok {
			b = &recoveryBudget{lastUsed: time.Now()}
			budgetByKey[key] = b
			budgetLRU = append(budgetLRU, key)
			evictBudgetLRUIfNeeded(key)
		} else {
			touchBudgetLRU(key)
		}
		budgetRegistryMu.Unlock()

		// Block rather than spin: a search holds b.mu across archive I/O, and
		// a parallel context_search of the same session must wait, not peg a CPU.
		b.mu.Lock()
		budgetRegistryMu.Lock()
		if budgetByKey[key] == b {
			budgetRegistryMu.Unlock()
			return b, func() { b.mu.Unlock() }
		}
		budgetRegistryMu.Unlock()
		b.mu.Unlock()
	}
}

func touchBudgetLRU(key string) {
	for i, k := range budgetLRU {
		if k == key {
			copy(budgetLRU[i:], budgetLRU[i+1:])
			budgetLRU[len(budgetLRU)-1] = key
			return
		}
	}
	budgetLRU = append(budgetLRU, key)
}

// evictBudgetLRUIfNeeded drops the least-recently-used session when over cap,
// never evicting keepKey (the session being charged).
func evictBudgetLRUIfNeeded(keepKey string) {
	skips := 0
	for len(budgetByKey) > contextSearchMaxBudgetSessions {
		if len(budgetLRU) == 0 {
			return
		}
		victim := budgetLRU[0]
		if victim == keepKey && len(budgetLRU) > 1 {
			victim = budgetLRU[1]
			copy(budgetLRU[1:], budgetLRU[2:])
			budgetLRU = budgetLRU[:len(budgetLRU)-1]
		} else if victim == keepKey {
			return
		} else {
			copy(budgetLRU, budgetLRU[1:])
			budgetLRU = budgetLRU[:len(budgetLRU)-1]
		}
		b := budgetByKey[victim]
		if b != nil && !b.mu.TryLock() {
			touchBudgetLRU(victim)
			skips++
			if skips >= len(budgetLRU) {
				return
			}
			continue
		}
		skips = 0
		if b != nil {
			b.mu.Unlock()
		}
		delete(budgetByKey, victim)
	}
}

func chargeBudget(key string, n int) int {
	b := getRecoveryBudget(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	chargeBudgetLocked(key, b, n)
	return b.used
}

func chargeBudgetLocked(key string, b *recoveryBudget, n int) {
	b.used += n
	b.lastUsed = time.Now()
	budgetRegistryMu.Lock()
	touchBudgetLRU(key)
	evictBudgetLRUIfNeeded(key)
	budgetRegistryMu.Unlock()
}

func budgetRemaining(key string) int {
	budgetRegistryMu.Lock()
	b, ok := budgetByKey[key]
	budgetRegistryMu.Unlock()
	if !ok {
		return contextSearchMaxRetrieved
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return contextSearchMaxRetrieved - b.used
}

// resetBudgetRegistryForTest clears process-global budget state (tests only).
func resetBudgetRegistryForTest() {
	budgetRegistryMu.Lock()
	defer budgetRegistryMu.Unlock()
	budgetByKey = map[string]*recoveryBudget{}
	budgetLRU = nil
}

// budgetHasKeyForTest reports whether key is in the registry (tests only).
func budgetHasKeyForTest(key string) bool {
	budgetRegistryMu.Lock()
	defer budgetRegistryMu.Unlock()
	_, ok := budgetByKey[key]
	return ok
}

// chargeBudgetForTest charges bytes under the per-session lock (tests only).
func chargeBudgetForTest(key string, n int) {
	chargeBudget(key, n)
}

// budgetUsedForTest returns charged bytes for key (tests only).
func budgetUsedForTest(key string) int {
	budgetRegistryMu.Lock()
	b, ok := budgetByKey[key]
	budgetRegistryMu.Unlock()
	if !ok {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}
