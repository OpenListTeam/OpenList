package cache

import "time"

type Expirable interface {
	Expired() bool
}

type ExpirationTime time.Time

func (e ExpirationTime) Expired() bool {
	return time.Now().After(time.Time(e))
}

type CacheEntry[T any] struct {
	Expirable
	data T
}
