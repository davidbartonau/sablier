package inmemory

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/sablierapp/sablier/pkg/sablier"
	"github.com/sablierapp/sablier/pkg/store"
	"github.com/sablierapp/sablier/pkg/tinykv"
)

var _ sablier.Store = (*InMemory)(nil)
var _ json.Marshaler = (*InMemory)(nil)
var _ json.Unmarshaler = (*InMemory)(nil)

func NewInMemory() sablier.Store {
	return &InMemory{
		kv:       tinykv.New[sablier.InstanceInfo](1*time.Second, nil),
		lastUsed: map[string]time.Time{},
	}
}

type InMemory struct {
	kv tinykv.KV[sablier.InstanceInfo]

	// lastUsed records the wall time of the most recent Put for each key.
	// Used by VRAM eviction as the recency signal. Maintained in parallel to
	// the kv (rather than threaded through tinykv) to keep tinykv generic.
	mu       sync.RWMutex
	lastUsed map[string]time.Time
}

func (i *InMemory) UnmarshalJSON(bytes []byte) error {
	return i.kv.UnmarshalJSON(bytes)
}

func (i *InMemory) MarshalJSON() ([]byte, error) {
	return i.kv.MarshalJSON()
}

func (i *InMemory) Get(_ context.Context, s string) (sablier.InstanceInfo, error) {
	val, ok := i.kv.Get(s)
	if !ok {
		return sablier.InstanceInfo{}, store.ErrKeyNotFound
	}
	return val, nil
}

func (i *InMemory) Put(_ context.Context, state sablier.InstanceInfo, duration time.Duration) error {
	if err := i.kv.Put(state.Name, state, duration); err != nil {
		return err
	}
	i.mu.Lock()
	i.lastUsed[state.Name] = time.Now()
	i.mu.Unlock()
	return nil
}

func (i *InMemory) Delete(_ context.Context, s string) error {
	i.kv.Delete(s)
	i.mu.Lock()
	delete(i.lastUsed, s)
	i.mu.Unlock()
	return nil
}

func (i *InMemory) OnExpire(_ context.Context, f func(string)) error {
	i.kv.SetOnExpire(func(k string, _ sablier.InstanceInfo) {
		i.mu.Lock()
		delete(i.lastUsed, k)
		i.mu.Unlock()
		f(k)
	})
	return nil
}

func (i *InMemory) List(_ context.Context) ([]sablier.LoadedInstance, error) {
	keys := i.kv.Keys()
	out := make([]sablier.LoadedInstance, 0, len(keys))

	i.mu.RLock()
	defer i.mu.RUnlock()

	for _, k := range keys {
		v, ok := i.kv.Get(k)
		if !ok {
			// Entry expired between Keys() and Get(); skip it.
			continue
		}
		out = append(out, sablier.LoadedInstance{
			Info:     v,
			LastUsed: i.lastUsed[k],
		})
	}
	return out, nil
}
