package aliyundrive_share

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"
)

// See issue https://github.com/OpenListTeam/OpenList/issues/724
// Seems there is no limit per user.

type limiterType int

const (
	limiterList limiterType = iota
	limiterLink
	limiterOther
)

const (
	listRateLimit  = 3.9  // 4 per second in document, but we use 3.9 per second to be safe
	linkRateLimit  = 0.9  // 1 per second in document, but we use 0.9 per second to be safe
	otherRateLimit = 14.9 // 15 per second in document, but we use 14.9 per second to be safe
)

type limiter struct {
	list  *rate.Limiter
	link  *rate.Limiter
	other *rate.Limiter
}

func getLimiter() *limiter {
	return &limiter{
		list:  rate.NewLimiter(rate.Limit(listRateLimit), 1),
		link:  rate.NewLimiter(rate.Limit(linkRateLimit), 1),
		other: rate.NewLimiter(rate.Limit(otherRateLimit), 1),
	}
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

}
func (d *AliyundriveShare) wait(ctx context.Context, typ limiterType) error {
	if d == nil {
		return fmt.Errorf("driver not init")
	}
	//if d.ref != nil {
	//	return d.ref.wait(ctx, typ) // If this is a reference driver, wait on the reference driver.
	//}
	return d.limiter.wait(ctx, typ)
}
