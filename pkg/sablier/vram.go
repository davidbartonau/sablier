package sablier

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrInsufficientVRAM is returned by VRAMManager.Plan when the requested
// reservation cannot be satisfied even after evicting every eligible
// candidate. The caller should surface this to the user; the request is
// rejected without state change.
var ErrInsufficientVRAM = errors.New("insufficient VRAM and no further eviction candidates")

// VRAMManager tracks pessimistic VRAM accounting for instances that opt in
// (PeakVRAMMB > 0). It does not measure actual GPU usage; it trusts the
// declared peak per container as a worst-case guarantee and allocates
// against TotalMB.
//
// Two pending sets bridge the windows where store state lags the manager's
// view of reality:
//
//   - pendingReserve: instance has been authorised to start, container is
//     not yet visible in the store as "loaded".
//   - pendingFree:    instance has been authorised to evict, container is
//     not yet absent from the store.
//
// Available is computed as
//
//	Total
//	  - sum(loaded.PeakVRAMMB for loaded ∉ pendingFree)
//	  - sum(pendingReserve)
//
// so concurrent Plan calls cannot double-count either victims being stopped
// or new starts already authorised.
type VRAMManager struct {
	totalMB    uint64
	headroomMB uint64

	mu             sync.Mutex
	pendingReserve map[string]uint64
	pendingFree    map[string]uint64
}

// NewVRAMManager returns a manager with the given total VRAM budget and
// headroom (always-free buffer kept above the threshold that triggers
// eviction).
func NewVRAMManager(totalMB, headroomMB uint64) *VRAMManager {
	return &VRAMManager{
		totalMB:        totalMB,
		headroomMB:     headroomMB,
		pendingReserve: map[string]uint64{},
		pendingFree:    map[string]uint64{},
	}
}

// TotalMB returns the configured total VRAM budget.
func (m *VRAMManager) TotalMB() uint64 { return m.totalMB }

// HeadroomMB returns the configured headroom.
func (m *VRAMManager) HeadroomMB() uint64 { return m.headroomMB }

// Plan computes a reservation plan for `name` (peak peakMB) given the
// snapshot of currently-loaded instances. On success it records the
// reservation (and any victim entries) in pending state and returns the
// list of victims to stop, in the order they should be stopped. The caller
// must:
//
//  1. for each victim: call provider.InstanceStop, then store.Delete, then
//     ConfirmFreed(victim).
//  2. proceed with the start.
//  3. on start success: ConfirmLoaded(name).
//     on start failure: AbortReserve(name).
//
// On error (ErrInsufficientVRAM) no state is mutated.
//
// Plan is a no-op (returns nil, nil) when peakMB == 0, treating the
// instance as a non-participant.
func (m *VRAMManager) Plan(name string, peakMB uint64, loaded []LoadedInstance) ([]string, error) {
	if peakMB == 0 {
		return nil, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	available := m.availableLocked(loaded, name)
	needed := int64(peakMB) + int64(m.headroomMB)

	if available >= needed {
		m.pendingReserve[name] = peakMB
		return nil, nil
	}

	cands := m.candidatesLocked(loaded, name)
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Info.Priority != cands[j].Info.Priority {
			return cands[i].Info.Priority < cands[j].Info.Priority
		}
		return cands[i].LastUsed.Before(cands[j].LastUsed)
	})

	var victims []string
	freed := int64(0)
	for _, c := range cands {
		if available+freed >= needed {
			break
		}
		victims = append(victims, c.Info.Name)
		freed += int64(c.Info.PeakVRAMMB)
	}

	if available+freed < needed {
		return nil, fmt.Errorf("%w: need %d MB (incl %d headroom), can free at most %d MB",
			ErrInsufficientVRAM, peakMB+m.headroomMB, m.headroomMB, available+freed)
	}

	for _, v := range victims {
		// Find the candidate's peak to record in pendingFree. cands is the
		// authoritative source for peaks at decision time; loaded may be
		// stale by the time ConfirmFreed runs.
		for _, c := range cands {
			if c.Info.Name == v {
				m.pendingFree[v] = c.Info.PeakVRAMMB
				break
			}
		}
	}
	m.pendingReserve[name] = peakMB
	return victims, nil
}

// availableLocked computes available VRAM excluding the request's own name
// from loaded (in case it appears there from a stale Put).
func (m *VRAMManager) availableLocked(loaded []LoadedInstance, self string) int64 {
	var consumed int64
	for _, l := range loaded {
		if l.Info.Name == self {
			continue
		}
		if _, freeing := m.pendingFree[l.Info.Name]; freeing {
			continue
		}
		consumed += int64(l.Info.PeakVRAMMB)
	}
	for n, mb := range m.pendingReserve {
		if n == self {
			continue
		}
		consumed += int64(mb)
	}
	return int64(m.totalMB) - consumed
}

// candidatesLocked returns eligible eviction candidates: loaded instances
// not equal to self, not already being freed, and with PeakVRAMMB > 0.
func (m *VRAMManager) candidatesLocked(loaded []LoadedInstance, self string) []LoadedInstance {
	out := make([]LoadedInstance, 0, len(loaded))
	for _, l := range loaded {
		if l.Info.Name == self {
			continue
		}
		if l.Info.PeakVRAMMB == 0 {
			continue
		}
		if _, freeing := m.pendingFree[l.Info.Name]; freeing {
			continue
		}
		out = append(out, l)
	}
	return out
}

// ConfirmFreed clears the pendingFree entry for victim. Call after
// provider.InstanceStop has returned successfully and the store entry has
// been deleted.
func (m *VRAMManager) ConfirmFreed(victim string) {
	m.mu.Lock()
	delete(m.pendingFree, victim)
	m.mu.Unlock()
}

// ConfirmLoaded clears the pendingReserve entry for name. Call when the
// container is loaded and its peak is now reflected via the store snapshot.
func (m *VRAMManager) ConfirmLoaded(name string) {
	m.mu.Lock()
	delete(m.pendingReserve, name)
	m.mu.Unlock()
}

// AbortReserve clears the pendingReserve entry for name without committing
// the load. Call when the start fails so the reservation does not leak.
func (m *VRAMManager) AbortReserve(name string) {
	m.mu.Lock()
	delete(m.pendingReserve, name)
	m.mu.Unlock()
}

// snapshot is exposed for tests.
type vramSnapshot struct {
	pendingReserve map[string]uint64
	pendingFree    map[string]uint64
}

func (m *VRAMManager) snapshotForTest() vramSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	pr := make(map[string]uint64, len(m.pendingReserve))
	for k, v := range m.pendingReserve {
		pr[k] = v
	}
	pf := make(map[string]uint64, len(m.pendingFree))
	for k, v := range m.pendingFree {
		pf[k] = v
	}
	return vramSnapshot{pendingReserve: pr, pendingFree: pf}
}

// quiescent reports whether the manager has no in-flight reservations or
// evictions. Useful for tests.
func (m *VRAMManager) quiescent() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pendingReserve) == 0 && len(m.pendingFree) == 0
}
