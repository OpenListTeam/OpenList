package op

import (
	"errors"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var errConflictingLinkLifecycle = errors.New("invalid link lifecycle: expiration cannot be combined with owned closers or RequireReference")

func admitLink(link *model.Link, obj model.Obj) (*objWithLink, error) {
	if link.Expiration != nil && (link.RequireReference || link.SyncClosers.Length() > 0) {
		return nil, errors.Join(errConflictingLinkLifecycle, link.Close())
	}
	return &objWithLink{link: link, obj: obj}, nil
}

func (ol *objWithLink) acquire() *model.Link {
	if ol.link.Expiration != nil || ol.link.SyncClosers.AcquireReference() || !ol.link.RequireReference {
		return ol.link.Clone()
	}
	return nil
}
