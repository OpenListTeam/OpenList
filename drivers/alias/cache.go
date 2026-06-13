package alias

import (
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type aliasListCacheEntry struct {
	objs    []model.Obj
	expires time.Time
}

type aliasResolveCacheEntry struct {
	resolved aliasResolved
	expires  time.Time
}

type aliasResolved struct {
	paths   []string
	skipped bool
	obj     model.Object
	hasObj  bool
}

type aliasCache struct {
	mu       sync.RWMutex
	lists    map[string]aliasListCacheEntry
	resolved map[string]aliasResolveCacheEntry
}

func (c *aliasCache) init() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists = make(map[string]aliasListCacheEntry)
	c.resolved = make(map[string]aliasResolveCacheEntry)
}

func (c *aliasCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists = make(map[string]aliasListCacheEntry)
	c.resolved = make(map[string]aliasResolveCacheEntry)
}

func (d *Alias) cacheTTL() time.Duration {
	if !d.AliasCacheEnabled {
		return 0
	}
	expiration := d.AliasCacheExpiration
	if expiration <= 0 {
		expiration = 30
	}
	return time.Minute * time.Duration(expiration)
}

func (d *Alias) cacheMaxEntries() int {
	if !d.AliasCacheEnabled || d.AliasCacheMaxEntries <= 0 {
		return 0
	}
	return d.AliasCacheMaxEntries
}

func (c *aliasCache) listKey(dirPaths []string, withDetails bool) string {
	parts := make([]string, 0, len(dirPaths))
	for _, dirPath := range dirPaths {
		if dirPath == "" {
			continue
		}
		parts = append(parts, utils.FixAndCleanPath(dirPath))
	}
	key := "backend:" + strings.Join(parts, "\x00")
	if withDetails {
		return key + "\x00details"
	}
	return key
}

func (c *aliasCache) getList(key string, ttl time.Duration) ([]model.Obj, bool) {
	if ttl <= 0 {
		return nil, false
	}
	c.mu.RLock()
	entry, ok := c.lists[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		c.deleteList(key)
		return nil, false
	}
	return cloneObjs(entry.objs), true
}

func (c *aliasCache) setList(key string, objs []model.Obj, ttl time.Duration, maxEntries int) {
	if ttl <= 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked(now)
	c.lists[key] = aliasListCacheEntry{
		objs:    cloneObjs(objs),
		expires: now.Add(ttl),
	}
	c.enforceMaxEntriesLocked(maxEntries)
}

func (c *aliasCache) deleteList(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.lists, key)
}

func (c *aliasCache) getResolved(path string, ttl time.Duration) (aliasResolved, bool) {
	if ttl <= 0 {
		return aliasResolved{}, false
	}
	path = utils.FixAndCleanPath(path)
	c.mu.RLock()
	entry, ok := c.resolved[path]
	c.mu.RUnlock()
	if !ok {
		return aliasResolved{}, false
	}
	if time.Now().After(entry.expires) {
		c.deleteResolved(path)
		return aliasResolved{}, false
	}
	return cloneResolved(entry.resolved), true
}

func (c *aliasCache) setResolved(path string, resolved aliasResolved, ttl time.Duration, maxEntries int) {
	if ttl <= 0 || len(resolved.paths) == 0 {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneExpiredLocked(now)
	c.resolved[utils.FixAndCleanPath(path)] = aliasResolveCacheEntry{
		resolved: cloneResolved(resolved),
		expires:  now.Add(ttl),
	}
	c.enforceMaxEntriesLocked(maxEntries)
}

func (c *aliasCache) deleteResolved(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.resolved, utils.FixAndCleanPath(path))
}

func (c *aliasCache) deleteResolvedPrefix(prefix string) {
	prefix = utils.FixAndCleanPath(prefix)
	if prefix == "/" {
		c.clear()
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.resolved {
		if key == prefix || strings.HasPrefix(key, prefix+"/") {
			delete(c.resolved, key)
		}
	}
}

func (c *aliasCache) pruneExpiredLocked(now time.Time) {
	for key, entry := range c.lists {
		if now.After(entry.expires) {
			delete(c.lists, key)
		}
	}
	for key, entry := range c.resolved {
		if now.After(entry.expires) {
			delete(c.resolved, key)
		}
	}
}

func (c *aliasCache) enforceMaxEntriesLocked(maxEntries int) {
	if maxEntries <= 0 {
		return
	}
	for len(c.lists)+len(c.resolved) > maxEntries {
		if c.deleteOldestLocked() {
			continue
		}
		return
	}
}

func (c *aliasCache) deleteOldestLocked() bool {
	var oldestKey string
	var oldestList bool
	var oldestExpires time.Time
	found := false
	for key, entry := range c.lists {
		if !found || entry.expires.Before(oldestExpires) {
			oldestKey = key
			oldestList = true
			oldestExpires = entry.expires
			found = true
		}
	}
	for key, entry := range c.resolved {
		if !found || entry.expires.Before(oldestExpires) {
			oldestKey = key
			oldestList = false
			oldestExpires = entry.expires
			found = true
		}
	}
	if !found {
		return false
	}
	if oldestList {
		delete(c.lists, oldestKey)
	} else {
		delete(c.resolved, oldestKey)
	}
	return true
}

func cloneObjs(objs []model.Obj) []model.Obj {
	if len(objs) == 0 {
		return nil
	}
	cloned := make([]model.Obj, len(objs))
	copy(cloned, objs)
	return cloned
}

func cloneStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	cloned := make([]string, len(items))
	copy(cloned, items)
	return cloned
}

func cloneResolved(resolved aliasResolved) aliasResolved {
	resolved.paths = cloneStrings(resolved.paths)
	return resolved
}
