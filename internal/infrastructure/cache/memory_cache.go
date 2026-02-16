package cache

import (
	"sync"
	"time"
)

/*
MemoryCache
-----------
Simple in-process cache with TTL.
Source of truth tetap DB.
*/

type MemoryCache struct {
	mu    sync.RWMutex
	items map[string]cacheItem
}

type cacheItem struct {
	value     interface{}
	expiresAt time.Time
}

// NewMemoryCache
func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		items: make(map[string]cacheItem),
	}
}

// Set
// ttl = 0 artinya tidak expired
func (c *MemoryCache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}

	c.items[key] = cacheItem{
		value:     value,
		expiresAt: exp,
	}
}

// Get
// return (value, true) jika ada & belum expired
func (c *MemoryCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		return nil, false
	}

	return item.value, true
}

// Delete
func (c *MemoryCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Clear
func (c *MemoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]cacheItem)
}
