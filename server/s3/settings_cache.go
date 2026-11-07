package s3

import "sync"

// The three S3 settings (bucket list, bucket credentials, per-bucket CORS) are
// read on every request, including anonymous requests that never reach
// authentication, and each read used to unmarshal the whole document again. A
// large configuration therefore turned into CPU and allocation per request that
// an unauthenticated client could drive at will, so the parse result is kept
// until the stored value changes.
type parsedSettingCache[T any] struct {
	mu  sync.Mutex
	key string
	val T
	err error
	hit bool
}

// get returns the parse of key, re-parsing only when key differs from the value
// that produced the cached result. Parse errors are cached as well: a broken
// document would otherwise be re-parsed (and re-logged) on every request.
func (c *parsedSettingCache[T]) get(key string, parse func(string) (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hit && c.key == key {
		return c.val, c.err
	}
	val, err := parse(key)
	c.key, c.val, c.err, c.hit = key, val, err, true
	return val, err
}
