package aliyundrive_open

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/time/rate"
)

// See document https://www.yuque.com/aliyundrive/zpfszx/mqocg38hlxzc5vcd
// See issue https://github.com/OpenListTeam/OpenList/issues/724
// We got limit per user per app, so the limiter should be global.

type limiterType int

const (
	limiterList limiterType = iota
	limiterLink
	limiterOther
)

const (
	listRateLimit       = 3.9  // 4 per second in document, but we use 3.9 per second to be safe
	linkRateLimit       = 0.9  // 1 per second in document, but we use 0.9 per second to be safe
	otherRateLimit      = 14.9 // 15 per second in document, but we use 14.9 per second to be safe
	globalLimiterUserID = ""   // Global limiter user ID, used to limit the initial requests.
)

type limiter struct {
	usedBy int
	list   *rate.Limiter
	link   *rate.Limiter
	other  *rate.Limiter
}

var limiters = make(map[string]*limiter)
var limitersLock = &sync.Mutex{}

func getLimiterForUser(userid string) *limiter {
	limitersLock.Lock()
	defer limitersLock.Unlock()
	defer func() {
		// Clean up limiters that are no longer used.
		for id, lim := range limiters {
			if lim.usedBy <= 0 && id != globalLimiterUserID { // Do not delete the global limiter.
				delete(limiters, id)
			}
		}
	}()
	if lim, ok := limiters[userid]; ok {
		lim.usedBy++
		return lim
	}
	lim := &limiter{
		usedBy: 1,
		list:   rate.NewLimiter(rate.Limit(listRateLimit), 1),
		link:   rate.NewLimiter(rate.Limit(linkRateLimit), 1),
		other:  rate.NewLimiter(rate.Limit(otherRateLimit), 1),
	}
	limiters[userid] = lim
	return lim
}

func (l *limiter) wait(ctx context.Context, typ limiterType) error {
	if l == nil {
		return fmt.Errorf("driver not init")
	}
	switch typ {
	case limiterList:
		return l.list.Wait(ctx)
	case limiterLink:
		return l.link.Wait(ctx)
	case limiterOther:
		return l.other.Wait(ctx)
	default:
		return fmt.Errorf("unknown limiter type")
	}
}
func (l *limiter) free() {
	if l == nil {
		return
	}
	limitersLock.Lock()
	defer limitersLock.Unlock()
	l.usedBy--
}
func (d *AliyundriveOpen) wait(ctx context.Context, typ limiterType) error {
	if d == nil {
		return fmt.Errorf("driver not init")
	}
	if d.ref != nil {
		return d.ref.wait(ctx, typ) // If this is a reference driver, wait on the reference driver.
	}
	return d.limiter.wait(ctx, typ)
}
