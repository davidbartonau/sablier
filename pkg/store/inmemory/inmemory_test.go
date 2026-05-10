package inmemory

import (
	"context"
	"github.com/sablierapp/sablier/pkg/sablier"
	"github.com/sablierapp/sablier/pkg/store"
	"gotest.tools/v3/assert"
	"testing"
	"time"
)

func TestInMemory(t *testing.T) {
	t.Parallel()
	t.Run("InMemoryErrNotFound", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		vk := NewInMemory()

		_, err := vk.Get(ctx, "test")
		assert.ErrorIs(t, err, store.ErrKeyNotFound)
	})
	t.Run("InMemoryPut", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		vk := NewInMemory()

		err := vk.Put(ctx, sablier.InstanceInfo{Name: "test"}, 1*time.Second)
		assert.NilError(t, err)

		i, err := vk.Get(ctx, "test")
		assert.NilError(t, err)
		assert.Equal(t, i.Name, "test")

		<-time.After(2 * time.Second)
		_, err = vk.Get(ctx, "test")
		assert.ErrorIs(t, err, store.ErrKeyNotFound)
	})
	t.Run("InMemoryDelete", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		vk := NewInMemory()

		err := vk.Put(ctx, sablier.InstanceInfo{Name: "test"}, 30*time.Second)
		assert.NilError(t, err)

		i, err := vk.Get(ctx, "test")
		assert.NilError(t, err)
		assert.Equal(t, i.Name, "test")

		err = vk.Delete(ctx, "test")
		assert.NilError(t, err)

		_, err = vk.Get(ctx, "test")
		assert.ErrorIs(t, err, store.ErrKeyNotFound)
	})
	t.Run("InMemoryOnExpire", func(t *testing.T) {
		t.Parallel()
		vk := NewInMemory()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		expirations := make(chan string)
		err := vk.OnExpire(ctx, func(key string) {
			expirations <- key
		})
		assert.NilError(t, err)

		err = vk.Put(ctx, sablier.InstanceInfo{Name: "test"}, 1*time.Second)
		assert.NilError(t, err)
		expired := <-expirations
		assert.Equal(t, expired, "test")
	})
	t.Run("InMemoryListReturnsLoadedWithRecency", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		vk := NewInMemory()

		before := time.Now()
		err := vk.Put(ctx, sablier.InstanceInfo{Name: "first"}, 30*time.Second)
		assert.NilError(t, err)
		time.Sleep(5 * time.Millisecond)
		err = vk.Put(ctx, sablier.InstanceInfo{Name: "second"}, 30*time.Second)
		assert.NilError(t, err)
		after := time.Now()

		loaded, err := vk.List(ctx)
		assert.NilError(t, err)
		assert.Equal(t, len(loaded), 2)

		byName := map[string]sablier.LoadedInstance{}
		for _, l := range loaded {
			byName[l.Info.Name] = l
		}
		_, hasFirst := byName["first"]
		_, hasSecond := byName["second"]
		assert.Assert(t, hasFirst && hasSecond)

		for _, l := range loaded {
			assert.Assert(t, !l.LastUsed.Before(before), "LastUsed must be >= before")
			assert.Assert(t, !l.LastUsed.After(after), "LastUsed must be <= after")
		}
		assert.Assert(t, byName["second"].LastUsed.After(byName["first"].LastUsed))
	})
	t.Run("InMemoryListAfterDeleteOmits", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		vk := NewInMemory()

		assert.NilError(t, vk.Put(ctx, sablier.InstanceInfo{Name: "k"}, 30*time.Second))
		assert.NilError(t, vk.Delete(ctx, "k"))

		loaded, err := vk.List(ctx)
		assert.NilError(t, err)
		assert.Equal(t, len(loaded), 0)
	})
}
