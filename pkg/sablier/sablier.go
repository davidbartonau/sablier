package sablier

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
)

type Sablier struct {
	provider Provider
	sessions Store

	groupsMu sync.RWMutex
	groups   map[string][]string

	pendingMu     sync.Mutex
	pendingStarts map[string]*pendingStart

	// vram is non-nil when VRAM-aware eviction is enabled. When nil, the
	// pre-existing time-only eviction policy applies and starts proceed
	// without any pressure check.
	vram *VRAMManager

	// BlockingRefreshFrequency is the frequency at which the instances are checked
	// against the provider. Defaults to 5 seconds.
	BlockingRefreshFrequency time.Duration

	// InstanceStartTimeout is the maximum time allowed for an async InstanceStart
	// call before it is cancelled. Defaults to 5 minutes.
	InstanceStartTimeout time.Duration

	l *slog.Logger
}

func New(logger *slog.Logger, store Store, provider Provider) *Sablier {
	return &Sablier{
		provider:                 provider,
		sessions:                 store,
		groupsMu:                 sync.RWMutex{},
		groups:                   map[string][]string{},
		pendingStarts:            map[string]*pendingStart{},
		l:                        logger,
		BlockingRefreshFrequency: 5 * time.Second,
		InstanceStartTimeout:     5 * time.Minute,
	}
}

func (s *Sablier) SetGroups(groups map[string][]string) {
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	if groups == nil {
		groups = map[string][]string{}
	}
	if diff := cmp.Diff(s.groups, groups); diff != "" {
		// TODO: Change this log for a friendly logging, groups rarely change, so we can put some effort on displaying what changed
		s.l.Info("set groups", slog.Any("old", s.groups), slog.Any("new", groups), slog.Any("diff", diff))
		s.groups = groups
	}
}

func (s *Sablier) RemoveInstance(ctx context.Context, name string) error {
	return s.sessions.Delete(ctx, name)
}

// SetVRAMManager wires a VRAMManager into Sablier. Pass nil to disable
// VRAM-aware eviction (the default after construction).
func (s *Sablier) SetVRAMManager(m *VRAMManager) {
	s.vram = m
}

// VRAMManager returns the configured VRAMManager, or nil when the feature is
// disabled.
func (s *Sablier) VRAMManager() *VRAMManager {
	return s.vram
}
