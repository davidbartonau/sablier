package sablier

import (
	"context"
	"errors"
	"time"
)

//go:generate go tool -modfile=../../tools.mod mockgen -package storetest -source=store.go -destination=../store/storetest/mocks_store.go *

// ErrListUnsupported is returned by Store.List when the backend cannot
// enumerate currently-loaded instances. VRAM-aware eviction requires List;
// startup will fail-fast against an unsupported backend rather than silently
// degrade to no eviction.
var ErrListUnsupported = errors.New("store does not support listing loaded instances")

// LoadedInstance is one entry returned by Store.List. LastUsed is the wall
// time of the most recent Put for this instance and is used by the VRAM
// eviction policy as the recency signal.
type LoadedInstance struct {
	Info     InstanceInfo
	LastUsed time.Time
}

type Store interface {
	Get(context.Context, string) (InstanceInfo, error)
	Put(context.Context, InstanceInfo, time.Duration) error
	Delete(context.Context, string) error
	OnExpire(context.Context, func(string)) error
	// List returns a snapshot of every currently-loaded (non-expired)
	// instance. Backends that cannot enumerate efficiently must return
	// ErrListUnsupported.
	List(context.Context) ([]LoadedInstance, error)
}
