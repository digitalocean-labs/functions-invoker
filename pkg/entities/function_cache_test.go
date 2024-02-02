package entities

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCachedFunctionReader(t *testing.T) {
	t.Parallel()

	want := &Function{Name: "testfunction"}
	var calls int
	db := fakeFunctionReader(func(ctx context.Context, fqdn, rev string) (*Function, error) {
		calls++
		return want, nil
	})
	cachedReader, err := NewCachedFunctionReader(db, 1)
	require.NoError(t, err)

	// The first call causes a "database" read.
	got, err := cachedReader.GetFunction(context.Background(), "testfunction", "testrevision")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 1, calls)

	// The second call doesn't.
	got2, err := cachedReader.GetFunction(context.Background(), "testfunction", "testrevision")
	require.NoError(t, err)
	require.Equal(t, want, got2)
	require.Equal(t, 1, calls) // Remains 1.

	// Fetching another function evicts the cached value.
	_, err = cachedReader.GetFunction(context.Background(), "cachebreaker", "testrevision")
	require.NoError(t, err)
	require.Equal(t, 2, calls)

	// The the next call causes a "database" read again.
	got3, err := cachedReader.GetFunction(context.Background(), "testfunction", "testrevision")
	require.NoError(t, err)
	require.Equal(t, want, got3)
	require.Equal(t, 3, calls)
}

func TestCachedFunctionReaderDoesntCacheErrors(t *testing.T) {
	t.Parallel()

	var calls int
	db := fakeFunctionReader(func(ctx context.Context, fqdn, rev string) (*Function, error) {
		calls++
		return nil, assert.AnError
	})
	cachedReader, err := NewCachedFunctionReader(db, 1)
	require.NoError(t, err)

	// The first call causes a "database" read.
	_, err = cachedReader.GetFunction(context.Background(), "testfunction", "testrevision")
	require.ErrorIs(t, err, assert.AnError)
	require.Equal(t, 1, calls)

	// The second call does as well as we don't cache errors.
	_, err = cachedReader.GetFunction(context.Background(), "testfunction", "testrevision")
	require.ErrorIs(t, err, assert.AnError)
	require.Equal(t, 2, calls)
}

func TestCachedFunctionReaderCachesParallel(t *testing.T) {
	t.Parallel()

	fn := &Function{Name: "testfunction"}
	var mux sync.Mutex
	var calls atomic.Int64
	db := fakeFunctionReader(func(ctx context.Context, fqdn, rev string) (*Function, error) {
		calls.Add(1)
		mux.Lock()
		defer mux.Unlock()
		return fn, nil
	})

	cachedReader, err := NewCachedFunctionReader(db, 1)
	require.NoError(t, err)

	// Prevent the "database" operation from finishing.
	mux.Lock()

	// Start 10 parallel GetFunction operations.
	var wg sync.WaitGroup
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()

			got, err := cachedReader.GetFunction(context.Background(), "testfunction", "")
			assert.NoError(t, err)
			assert.Equal(t, fn, got)
		}()
	}

	// We have no chance to actually wait for all calls to have reached the cache, thus we have to
	// guesstimate here that after a few milliseconds, all goroutines will have attached to the
	// cached entry.
	time.Sleep(50 * time.Millisecond)

	// Allow the database operation to finish.
	mux.Unlock()
	// Wait for all parallel requests to finish.
	wg.Wait()

	// We should've seen only a single call to the database.
	require.EqualValues(t, 1, calls.Load())

	// Start 10 more parallel GetFunction operations. Everything should be cached now.
	var wg2 sync.WaitGroup
	wg2.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg2.Done()

			got, err := cachedReader.GetFunction(context.Background(), "testfunction", "")
			assert.NoError(t, err)
			assert.Equal(t, fn, got)
		}()
	}

	// Wait for all parallel requests to finish.
	wg2.Wait()
	// We should've still only seen 1 request to the database.
	require.EqualValues(t, 1, calls.Load())
}

type fakeFunctionReader func(context.Context, string, string) (*Function, error)

func (f fakeFunctionReader) GetFunction(ctx context.Context, fqdn string, rev string) (*Function, error) {
	return f(ctx, fqdn, rev)
}
