package sablier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sablierapp/sablier/pkg/store"
)

type pendingStart struct {
	done chan struct{}
	err  error
	info InstanceInfo // set at creation time; used to return consistent data while starting
}

// consumePendingError checks whether a pending start exists for the given
// instance. It returns (pending, error):
//   - pending=true, err=nil  -> start still in progress, caller should skip inspect
//   - pending=false, err!=nil -> start completed with error, entry cleared for retry
//   - pending=false, err=nil  -> no pending entry or already cleaned up
func (s *Sablier) consumePendingError(name string) (bool, error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	ps, exists := s.pendingStarts[name]
	if !exists {
		return false, nil
	}

	select {
	case <-ps.done:
		// Goroutine finished — clean up regardless of outcome
		delete(s.pendingStarts, name)
		if ps.err != nil {
			return false, fmt.Errorf("instance start failed: %w", ps.err)
		}
		return false, nil
	default:
		// Still running
		return true, nil
	}
}

func (s *Sablier) requestStart(ctx context.Context, name string) (InstanceInfo, error) {
	// First critical section: check whether a start is already in progress.
	// We release the lock before doing the remote inspect to avoid holding a
	// mutex across a potentially slow network call.
	s.pendingMu.Lock()
	if ps, exists := s.pendingStarts[name]; exists {
		select {
		case <-ps.done:
			// Goroutine completed
			if ps.err != nil {
				err := ps.err
				delete(s.pendingStarts, name)
				s.pendingMu.Unlock()
				return InstanceInfo{}, fmt.Errorf("instance start failed: %w", err)
			}
			// Succeeded previously but instance is no longer in store — fall through to restart
			delete(s.pendingStarts, name)
		default:
			// Still running — return the cached InstanceInfo to preserve provider fields
			s.l.DebugContext(ctx, "instance start already in progress", slog.String("instance", name))
			info := ps.info
			s.pendingMu.Unlock()
			return info, nil
		}
	}
	s.pendingMu.Unlock()

	// Inspect outside the lock: this may be a slow remote network call.
	// If inspect fails (e.g. first boot), fall back to a minimal struct so
	// the start still proceeds.
	info, err := s.provider.InstanceInspect(ctx, name)
	if err != nil {
		s.l.DebugContext(ctx, "pre-start inspect failed, using bare info", slog.String("instance", name), slog.Any("error", err))
		info = InstanceInfo{Name: name, CurrentReplicas: 0, DesiredReplicas: 1}
	}
	info.Status = InstanceStatusStarting

	// VRAM admission: plan and apply any evictions before claiming the
	// pendingStarts slot, so a refusal here doesn't leave a half-registered
	// entry that future requests would skip past.
	if err := s.ensureVRAMAdmission(ctx, name, info.PeakVRAMMB); err != nil {
		return InstanceInfo{}, err
	}

	// Second critical section: register the pending entry. Re-check in case
	// another goroutine raced past the first unlock and registered first.
	s.pendingMu.Lock()
	if existing, exists := s.pendingStarts[name]; exists {
		select {
		case <-existing.done:
			// The racing goroutine already finished; proceed with our own entry.
			delete(s.pendingStarts, name)
		default:
			// A concurrent goroutine won the race; return its cached info.
			s.l.DebugContext(ctx, "instance start already in progress (post-inspect race)", slog.String("instance", name))
			existingInfo := existing.info
			s.pendingMu.Unlock()
			// We made a VRAM reservation that nobody will consume because
			// the racing goroutine owns the start. Release it.
			if s.vram != nil {
				s.vram.AbortReserve(name)
			}
			return existingInfo, nil
		}
	}
	ps := &pendingStart{done: make(chan struct{}), info: info}
	s.pendingStarts[name] = ps
	s.pendingMu.Unlock()

	// Detach from the request context to avoid retaining HTTP request values,
	// but use a bounded timeout to prevent goroutine leaks.
	startCtx, cancel := context.WithTimeout(context.Background(), s.InstanceStartTimeout)

	go func() {
		defer cancel()
		defer close(ps.done)
		// Released via ConfirmLoaded on success; the deferred abort below
		// covers errors and panics so a reservation cannot leak.
		var loaded bool
		defer func() {
			if !loaded && s.vram != nil {
				s.vram.AbortReserve(name)
			}
		}()

		if err := s.provider.InstanceStart(startCtx, name); err != nil {
			ps.err = err
			s.l.Error("async instance start failed", slog.String("instance", name), slog.Any("error", err))
			return
		}

		s.l.InfoContext(ctx, "instance is ready", slog.String("instance", name))
		if s.vram != nil {
			s.vram.ConfirmLoaded(name)
		}
		loaded = true

		// Success — clean up immediately so the entry doesn't linger
		s.pendingMu.Lock()
		// Only delete if ps is still the current entry (not replaced by a retry)
		if current, ok := s.pendingStarts[name]; ok && current == ps {
			delete(s.pendingStarts, name)
		}
		s.pendingMu.Unlock()
	}()

	return info, nil
}

// ensureVRAMAdmission plans and executes any evictions required to fit a new
// reservation of peakMB for `name`. It is a no-op when VRAM management is
// disabled or the instance does not participate (peakMB == 0).
//
// On return, either:
//   - a reservation has been recorded and any victims have been stopped and
//     deleted from the store (the caller should proceed to start `name`), or
//   - no state has changed and an error is returned (the caller should
//     reject the request).
func (s *Sablier) ensureVRAMAdmission(ctx context.Context, name string, peakMB uint64) error {
	if s.vram == nil || peakMB == 0 {
		return nil
	}

	loaded, err := s.sessions.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot list loaded instances for VRAM planning: %w", err)
	}

	victims, err := s.vram.Plan(name, peakMB, loaded)
	if err != nil {
		s.l.WarnContext(ctx, "vram admission refused",
			slog.String("instance", name),
			slog.Uint64("peak_mb", peakMB),
			slog.Any("error", err))
		return err
	}

	for _, v := range victims {
		s.l.InfoContext(ctx, "evicting instance for VRAM pressure",
			slog.String("victim", v),
			slog.String("for", name))
		if stopErr := s.provider.InstanceStop(ctx, v); stopErr != nil {
			// Eviction stop failed. Roll back our reservations so the
			// system stays consistent and surface the error.
			s.l.ErrorContext(ctx, "vram eviction stop failed",
				slog.String("victim", v),
				slog.Any("error", stopErr))
			s.vram.AbortReserve(name)
			// Confirm-freed for *this* victim so its slot doesn't leak; we
			// don't know the actual VRAM state for victims we already
			// stopped successfully, but those entries will be cleaned by
			// the deferred event-driven RemoveInstance path.
			s.vram.ConfirmFreed(v)
			return fmt.Errorf("eviction stop failed for %s: %w", v, stopErr)
		}
		if delErr := s.sessions.Delete(ctx, v); delErr != nil {
			s.l.WarnContext(ctx, "could not delete evicted instance from store",
				slog.String("victim", v),
				slog.Any("error", delErr))
		}
		s.vram.ConfirmFreed(v)
	}

	return nil
}

func (s *Sablier) InstanceRequest(ctx context.Context, name string, duration time.Duration) (InstanceInfo, error) {
	if name == "" {
		return InstanceInfo{}, errors.New("instance name cannot be empty")
	}

	state, err := s.sessions.Get(ctx, name)
	if errors.Is(err, store.ErrKeyNotFound) {
		s.l.DebugContext(ctx, "request to start instance received", slog.String("instance", name))

		state, err = s.requestStart(ctx, name)
		if err != nil {
			return InstanceInfo{}, err
		}

		s.l.InfoContext(ctx, "request to start instance dispatched", slog.String("instance", name), slog.String("status", string(state.Status)), slog.Duration("expiration", duration))
	} else if err != nil {
		s.l.ErrorContext(ctx, "request to start instance failed", slog.String("instance", name), slog.Any("error", err))
		return InstanceInfo{}, fmt.Errorf("cannot retrieve instance from store: %w", err)
	} else if state.Status != InstanceStatusReady {
		// Check for a completed (possibly failed) async start before inspecting
		pending, pendingErr := s.consumePendingError(name)
		if pendingErr != nil {
			return InstanceInfo{}, pendingErr
		}

		if pending {
			// Start is still in progress — no point inspecting, return current state
			s.l.DebugContext(ctx, "instance start still in progress, skipping inspect", slog.String("instance", name))
		} else {
			s.l.DebugContext(ctx, "request to check instance status received", slog.String("instance", name), slog.String("current_status", string(state.Status)))
			state, err = s.provider.InstanceInspect(ctx, name)
			if err != nil {
				return InstanceInfo{}, err
			}
			s.l.DebugContext(ctx, "request to check instance status completed", slog.String("instance", name), slog.String("new_status", string(state.Status)))
		}
	}

	// VRAM participants opt out of TTL-based eviction. Once a container
	// declares peak_vram_mb (and VRAM management is enabled), pressure is
	// the only signal that should stop it: the user has explicitly accepted
	// the cost of keeping the container loaded indefinitely until VRAM is
	// needed elsewhere. Override the request's session_duration with a
	// far-future sentinel so the TTL machinery effectively never fires.
	storeDuration := duration
	if s.vram != nil && state.PeakVRAMMB > 0 {
		storeDuration = vramParticipantTTL
		s.l.DebugContext(ctx, "vram participant: TTL bypassed", slog.String("instance", name))
	} else {
		s.l.DebugContext(ctx, "set expiration for instance", slog.String("instance", name), slog.Duration("expiration", duration))
	}

	err = s.sessions.Put(ctx, state, storeDuration)
	if err != nil {
		s.l.ErrorContext(ctx, "could not put instance to store, will not expire", slog.Any("error", err), slog.String("instance", state.Name))
		return InstanceInfo{}, fmt.Errorf("could not put instance to store: %w", err)
	}
	return state, nil
}

// vramParticipantTTL is the duration recorded in the store for instances
// that opt into VRAM management. It must be long enough that no realistic
// system uptime will reach it (so OnInstanceExpired never fires for
// participants) and short enough that time arithmetic does not overflow.
// 100 years comfortably satisfies both.
const vramParticipantTTL = 100 * 365 * 24 * time.Hour
