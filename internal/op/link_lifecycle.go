package op

import (
	"errors"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var errConflictingLinkLifecycle = errors.New("invalid link lifecycle: expiration cannot be combined with owned closers or RequireReference")

type linkCachePolicy struct {
	expiration       *time.Duration
	requireReference bool
}

func admitLink(link *model.Link, obj model.Obj) (*objWithLink, error) {
	if link.Expiration != nil && (link.RequireReference || link.SyncClosers.Length() > 0) {
		return nil, errors.Join(errConflictingLinkLifecycle, link.Close())
	}
	return &objWithLink{
		link: link,
		obj:  obj,
		policy: linkCachePolicy{
			expiration:       link.Expiration,
			requireReference: link.RequireReference,
		},
	}, nil
}

func (ol *objWithLink) acquire() bool {
	return ol.policy.expiration != nil ||
		ol.link.SyncClosers.AcquireReference() || !ol.policy.requireReference
}
