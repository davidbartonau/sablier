package sablier

import (
	"errors"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func mkLoaded(name string, peak uint64, priority int, lastUsed time.Time) LoadedInstance {
	return LoadedInstance{
		Info: InstanceInfo{
			Name:       name,
			PeakVRAMMB: peak,
			Priority:   priority,
		},
		LastUsed: lastUsed,
	}
}

func TestVRAM_NonParticipantIsNoOp(t *testing.T) {
	m := NewVRAMManager(8000, 0)
	victims, err := m.Plan("foo", 0, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(victims), 0)
	assert.Assert(t, m.quiescent(), "manager should not record any state for peak=0")
}

func TestVRAM_DisabledByNilManager(t *testing.T) {
	// Sanity: a nil VRAMManager is the disabled state. We don't dereference
	// it in the Sablier hot path; the call site checks for nil. Here we
	// just confirm the constructor returns a non-nil pointer.
	m := NewVRAMManager(1, 0)
	assert.Assert(t, m != nil)
}

func TestVRAM_ReservesWhenSpaceAvailable(t *testing.T) {
	m := NewVRAMManager(24000, 1000)
	loaded := []LoadedInstance{
		mkLoaded("a", 8000, 50, time.Now()),
	}

	victims, err := m.Plan("b", 6000, loaded)
	assert.NilError(t, err)
	assert.Equal(t, len(victims), 0, "no eviction expected")

	snap := m.snapshotForTest()
	assert.Equal(t, snap.pendingReserve["b"], uint64(6000))
	assert.Equal(t, len(snap.pendingFree), 0)
}

func TestVRAM_HeadroomTriggersEviction(t *testing.T) {
	// Total=24000, headroom=2000. Loaded sums to 20000. New peak=2000.
	// Available = 4000; needed = 2000 + 2000 = 4000. Just fits, no eviction.
	m := NewVRAMManager(24000, 2000)
	loaded := []LoadedInstance{
		mkLoaded("a", 10000, 50, time.Now()),
		mkLoaded("b", 10000, 50, time.Now()),
	}
	victims, err := m.Plan("c", 2000, loaded)
	assert.NilError(t, err)
	assert.Equal(t, len(victims), 0)

	// Same setup but peak=3000 → needed=5000, only 4000 available.
	m2 := NewVRAMManager(24000, 2000)
	victims, err = m2.Plan("c", 3000, loaded)
	assert.NilError(t, err)
	assert.Assert(t, len(victims) >= 1, "expected at least one eviction")
}

func TestVRAM_EvictsLowestPriorityFirst(t *testing.T) {
	now := time.Now()
	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("high", 4000, 100, now),       // most recent, high pri
		mkLoaded("low", 4000, 10, now.Add(-1)), // older, low pri
	}
	// Available = 10000 - 8000 = 2000. New peak=3000. Need to free 1000+.
	victims, err := m.Plan("new", 3000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, victims, []string{"low"})
}

func TestVRAM_TieBreaksByOldestLastUsed(t *testing.T) {
	now := time.Now()
	older := now.Add(-1 * time.Hour)
	newer := now.Add(-1 * time.Minute)

	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("recent", 4000, 50, newer),
		mkLoaded("stale", 4000, 50, older),
	}
	victims, err := m.Plan("new", 3000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, victims, []string{"stale"})
}

func TestVRAM_EvictsMultipleUntilEnoughFreed(t *testing.T) {
	now := time.Now()
	m := NewVRAMManager(20000, 0)
	loaded := []LoadedInstance{
		mkLoaded("a", 5000, 10, now.Add(-3*time.Hour)),
		mkLoaded("b", 5000, 20, now.Add(-2*time.Hour)),
		mkLoaded("c", 5000, 30, now.Add(-1*time.Hour)),
		mkLoaded("d", 5000, 40, now),
	}
	// Available = 0. Need 12000. Should evict a, b, c (3 × 5000 = 15000 freed).
	victims, err := m.Plan("new", 12000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, victims, []string{"a", "b", "c"})
}

func TestVRAM_RefusesWhenInsufficientEvenAfterAllEvictions(t *testing.T) {
	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("a", 4000, 10, time.Now()),
	}
	// Even after evicting a, we have 10000 free. Request peak=11000 → impossible.
	_, err := m.Plan("huge", 11000, loaded)
	assert.Assert(t, errors.Is(err, ErrInsufficientVRAM))
	assert.Assert(t, m.quiescent(), "no state should be mutated on refusal")
}

func TestVRAM_NonParticipantsNotSelectedAsVictims(t *testing.T) {
	now := time.Now()
	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("noparticipant", 0, 1, now),  // peak=0, lowest priority
		mkLoaded("participant", 4000, 50, now),
	}
	// Available = 10000 - 4000 = 6000 (noparticipant doesn't count). New peak=7000.
	// Need to free 1000+; only `participant` is eligible.
	victims, err := m.Plan("new", 7000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, victims, []string{"participant"})
}

func TestVRAM_ConfirmFreedReleasesPendingFreeSlot(t *testing.T) {
	now := time.Now()
	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("a", 4000, 10, now),
	}
	victims, err := m.Plan("b", 7000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, victims, []string{"a"})

	snap := m.snapshotForTest()
	assert.Equal(t, snap.pendingFree["a"], uint64(4000))
	assert.Equal(t, snap.pendingReserve["b"], uint64(7000))

	m.ConfirmFreed("a")
	snap = m.snapshotForTest()
	_, hasA := snap.pendingFree["a"]
	assert.Assert(t, !hasA, "pendingFree[a] should be cleared")
	assert.Equal(t, snap.pendingReserve["b"], uint64(7000))
}

func TestVRAM_ConfirmLoadedReleasesPendingReserveSlot(t *testing.T) {
	m := NewVRAMManager(10000, 0)
	_, err := m.Plan("b", 4000, nil)
	assert.NilError(t, err)
	assert.Equal(t, m.snapshotForTest().pendingReserve["b"], uint64(4000))

	m.ConfirmLoaded("b")
	assert.Assert(t, m.quiescent())
}

func TestVRAM_AbortReserveReleasesPendingReserveSlot(t *testing.T) {
	m := NewVRAMManager(10000, 0)
	_, err := m.Plan("b", 4000, nil)
	assert.NilError(t, err)

	m.AbortReserve("b")
	assert.Assert(t, m.quiescent())
}

func TestVRAM_ConcurrentReservesDoNotDoubleCount(t *testing.T) {
	// Two consecutive Plan calls (no Confirm in between). Second should see
	// the first's pendingReserve and account for it.
	m := NewVRAMManager(12000, 0)
	// No loaded instances. Available = 12000.
	v1, err := m.Plan("a", 5000, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(v1), 0)

	// Now available should be 12000 - 5000 = 7000 (a is in pendingReserve).
	v2, err := m.Plan("b", 5000, nil)
	assert.NilError(t, err)
	assert.Equal(t, len(v2), 0)

	// Third request needs 5000 but only 2000 left, no candidates → error.
	_, err = m.Plan("c", 5000, nil)
	assert.Assert(t, errors.Is(err, ErrInsufficientVRAM))
}

func TestVRAM_VictimAlreadyBeingFreedIsNotPickedAgain(t *testing.T) {
	// First Plan picks 'a' as victim → a goes to pendingFree.
	// Second Plan, before ConfirmFreed, should not pick 'a' again.
	// Sizing: total=10000, loaded a+b=8000, so available=2000. With peak=5000
	// the deficit is 3000 — evicting just one 4000-MB candidate suffices.
	now := time.Now()
	m := NewVRAMManager(10000, 0)
	loaded := []LoadedInstance{
		mkLoaded("a", 4000, 10, now),
		mkLoaded("b", 4000, 20, now),
	}

	v1, err := m.Plan("new1", 5000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, v1, []string{"a"})

	// 'a' is now in pendingFree, and new1 holds a 5000-MB pending reserve.
	// Loaded snapshot still contains a (caller hasn't deleted yet). The
	// next Plan should treat a as already-being-freed and pick b instead.
	// Available now = 10000 - 4000(b) - 5000(new1 reserve) = 1000;
	// peak=5000 needs 4000 more; b's 4000 is exactly enough.
	v2, err := m.Plan("new2", 5000, loaded)
	assert.NilError(t, err)
	assert.DeepEqual(t, v2, []string{"b"})
}

func TestVRAM_AccountingRespectsTotal(t *testing.T) {
	m := NewVRAMManager(8000, 500)
	assert.Equal(t, m.TotalMB(), uint64(8000))
	assert.Equal(t, m.HeadroomMB(), uint64(500))
}
