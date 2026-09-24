package bootstrap

import (
	"context"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/strm"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search"
	"github.com/OpenListTeam/OpenList/v4/pkg/mq"
	log "github.com/sirupsen/logrus"
)

var (
	searchProjection *mq.LatestProcessor[string, []model.Obj]
	strmProjection   *mq.LatestProcessor[string, []model.Obj]
)

func InitSnapshotProjection() {
	searchProjection = mq.NewLatestProcessor(256, 65536, 4, search.UpdateSnapshot)
	strmProjection = mq.NewLatestProcessor(128, 32768, 1, strm.UpdateLocalStrm)
	op.SetSnapshotProjector(func(_ context.Context, parent string, objs []model.Obj) {
		offerSnapshot("search", searchProjection, parent, objs)
		offerSnapshot("strm", strmProjection, parent, objs)
	})
	op.StartSnapshotReconciliation()
}

func offerSnapshot(name string, processor *mq.LatestProcessor[string, []model.Obj], parent string, objs []model.Obj) {
	result := processor.Offer(parent, objs, len(objs))
	if result == mq.OfferRejectedCapacity || result == mq.OfferRejectedClosed {
		count := processor.Stats().Rejected
		if count == 1 || count%100 == 0 {
			log.Warnf("%s snapshot rejected: parent=%s result=%d rejected=%d", name, parent, result, count)
		}
	}
}

func ReleaseSnapshotProjection() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := op.StopSnapshotReconciliation(ctx); err != nil {
		log.Warnf("stop snapshot reconciliation: %v", err)
	}
	op.SetSnapshotProjector(nil)
	if err := searchProjection.Stop(ctx); err != nil {
		log.Warnf("stop search snapshot projection: %v", err)
	}
	if err := strmProjection.Stop(ctx); err != nil {
		log.Warnf("stop strm snapshot projection: %v", err)
	}
}
