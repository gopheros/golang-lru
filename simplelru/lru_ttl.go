package simplelru

import (
	"container/list"
	"errors"
	"sync"
	"time"
)

// EvictCallback is used to get a callback when a cache entry is evicted
type EvictCallbackTtl func(key interface{}, value interface{})

// LRUTtl implements a thread safe fixed size, ttl enabled LRU cache
type LRUTtl struct {
	size      int
	evictList *list.List
	items     map[interface{}]*list.Element
	onEvict   EvictCallbackTtl
	expiry time.Duration
	lock sync.RWMutex
}


// entry is used to hold a value in the evictList
type entry_ttl struct {
	key   interface{}
	value interface{}
	ttl time.Time
	timer *time.Timer
}

func (e *entry_ttl) CleanUp(lru *LRUTtl) {
	<-e.timer.C

	// timer expired, remove item
	// possible deadlock: may have to sync this
	lru.lock.Lock()
	lru.Remove(e.key)
	lru.lock.Unlock()
}

func (e *entry_ttl) Reset(duration time.Duration) {
	if !e.timer.Stop() {
		<-e.timer.C
	}
	// reset timer
	e.timer.Reset(duration)
}

// NewLRU constructs an LRU of the given size
func NewLRUTtl(size int, expiry time.Duration, onEvict EvictCallbackTtl) (*LRUTtl, error) {
	if size <= 0 {
		return nil, errors.New("Must provide a positive size")
	}
	c := &LRUTtl{
		size:      size,
		evictList: list.New(),
		items:     make(map[interface{}]*list.Element),
		onEvict:   onEvict,
		expiry: expiry,
	}
	return c, nil
}

// Purge is used to completely clear the cache.
func (c *LRUTtl) Purge() {
	for k, v := range c.items {
		if c.onEvict != nil {
			c.onEvict(k, v.Value.(*entry_ttl).value)
		}
		delete(c.items, k)
	}
	c.evictList.Init()
}

// Add adds a value to the cache.  Returns true if an eviction occurred.
func (c *LRUTtl) Add(key, value interface{}) (evicted bool) {
	c.lock.Lock()
	defer c.lock.Unlock()
	// Check for existing item
	if ent, ok := c.items[key]; ok {
		c.evictList.MoveToFront(ent)
		ent.Value.(*entry_ttl).value = value
		ent.Value.(*entry_ttl).ttl = time.Now().Add(c.expiry)
		ent.Value.(*entry_ttl).Reset(c.expiry)
		//ent.Value.(*entry_ttl).CleanUp(c)
		return false
	}

	// Add new item
	ent := &entry_ttl{key,
		value,
		time.Now().Add(c.expiry),
		time.NewTimer(c.expiry),
	}
	go ent.CleanUp(c)
	entry := c.evictList.PushFront(ent)
	c.items[key] = entry

	evict := c.evictList.Len() > c.size
	// Verify size not exceeded
	if evict {
		c.removeOldest()
	}
	return evict
}

// Get looks up a key's value from the cache.
func (c *LRUTtl) Get(key interface{}) (value interface{}, ok bool) {
	if ent, ok := c.items[key]; ok {
		c.evictList.MoveToFront(ent)
		if ent.Value.(*entry_ttl) == nil {
			return nil, false
		}
		if time.Now().After(ent.Value.(*entry_ttl).ttl) {
			c.Remove(key)
			return nil, false
		}
		return ent.Value.(*entry_ttl).value, true
	}
	return
}

// Contains checks if a key is in the cache, without updating the recent-ness
// or deleting it for being stale.
func (c *LRUTtl) Contains(key interface{}) (ok bool) {
	_, ok = c.items[key]
	return ok
}

// Peek returns the key value (or undefined if not found) without updating
// the "recently used"-ness of the key.
func (c *LRUTtl) Peek(key interface{}) (value interface{}, ok bool) {
	var ent *list.Element
	if ent, ok = c.items[key]; ok {
		return ent.Value.(*entry_ttl).value, true
	}
	return nil, ok
}

// Remove removes the provided key from the cache, returning if the
// key was contained.
func (c *LRUTtl) Remove(key interface{}) (present bool) {
	if ent, ok := c.items[key]; ok {
		c.removeElement(ent)
		return true
	}
	return false
}

// RemoveOldest removes the oldest item from the cache.
func (c *LRUTtl) RemoveOldest() (key interface{}, value interface{}, ok bool) {
	ent := c.evictList.Back()
	if ent != nil {
		c.removeElement(ent)
		kv := ent.Value.(*entry_ttl)
		return kv.key, kv.value, true
	}
	return nil, nil, false
}

// GetOldest returns the oldest entry
func (c *LRUTtl) GetOldest() (key interface{}, value interface{}, ok bool) {
	ent := c.evictList.Back()
	if ent != nil {
		kv := ent.Value.(*entry_ttl)
		return kv.key, kv.value, true
	}
	return nil, nil, false
}

// Keys returns a slice of the keys in the cache, from oldest to newest.
func (c *LRUTtl) Keys() []interface{} {
	keys := make([]interface{}, len(c.items))
	i := 0
	for ent := c.evictList.Back(); ent != nil; ent = ent.Prev() {
		keys[i] = ent.Value.(*entry_ttl).key
		i++
	}
	return keys
}

// Len returns the number of items in the cache.
func (c *LRUTtl) Len() int {
	return c.evictList.Len()
}

// Resize changes the cache size.
func (c *LRUTtl) Resize(size int) (evicted int) {
	diff := c.Len() - size
	if diff < 0 {
		diff = 0
	}
	for i := 0; i < diff; i++ {
		c.removeOldest()
	}
	c.size = size
	return diff
}

// removeOldest removes the oldest item from the cache.
func (c *LRUTtl) removeOldest() {
	ent := c.evictList.Back()
	if ent != nil {
		c.removeElement(ent)
	}
}

// removeElement is used to remove a given list element from the cache
func (c *LRUTtl) removeElement(e *list.Element) {
	c.evictList.Remove(e)
	kv := e.Value.(*entry_ttl)
	delete(c.items, kv.key)
	if c.onEvict != nil {
		c.onEvict(kv.key, kv.value)
	}
}