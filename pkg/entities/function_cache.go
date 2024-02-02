package entities

import (
	"context"
	"fmt"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
)

var (
	_ FunctionReader = (*CachedFunctionReader)(nil)
)

// NewCachedFunctionReader creates a cache for functions. It implements FunctionReader and thus can be
// used as a drop-in replacement.
func NewCachedFunctionReader(reader FunctionReader, size int) (*CachedFunctionReader, error) {
	cache, err := lru.New[cacheKey, func() (*Function, error)](size)
	if err != nil {
		return nil, fmt.Errorf("failed to create LRU cache: %w", err)
	}

	return &CachedFunctionReader{
		reader: reader,
		cache:  cache,
	}, nil
}

// CachedFunctionReader implements a cached version of FunctionReader.
type CachedFunctionReader struct {
	reader FunctionReader

	// The cache itself is thread-safe but since there's no atomic GetOrAdd function, we lock
	// ourselves to avoid races.
	mux   sync.Mutex
	cache *lru.Cache[cacheKey, func() (*Function, error)]
}

// cacheKey is the key for a cache entry. We avoid string concatenations by using a comparable
// struct.
type cacheKey struct {
	name     string
	revision string
}

// GetFunction implements FunctionReader.
func (c *CachedFunctionReader) GetFunction(ctx context.Context, fqdn string, rev string) (*Function, error) {
	key := cacheKey{name: fqdn, revision: rev}
	entry, ok := c.cache.Get(key)
	if !ok {
		c.mux.Lock()
		// Check again under a lock to avoid races due to a missing atomic GetOrAdd function.
		entry, ok := c.cache.Get(key)
		if ok {
			// If there was an entry now, we've raced to the first Get above and another call was
			// quicker. Attach to the existing entry.
			c.mux.Unlock()
			metricCacheHit.Inc()

			return entry()
		}

		// If there's no entry yet, create one and immediately add it to the cache so that subsequent
		// calls can attach to the existing get operation.
		metricCacheMiss.Inc()
		entry = sync.OnceValues(func() (*Function, error) {
			fn, err := c.reader.GetFunction(ctx, fqdn, rev)
			if err != nil {
				// If the operation failed, we propagate the error to everybody who's attached to the
				// entry, but remove the entry immediately too.
				c.cache.Remove(key)
			}
			return fn, err
		})
		c.cache.Add(key, entry)

		// Only unlock after we've added the entry into the cache.
		c.mux.Unlock()

		// Execute the actual call after unlocking to allow others to attach.
		return entry()
	}

	metricCacheHit.Inc()
	return entry()
}
