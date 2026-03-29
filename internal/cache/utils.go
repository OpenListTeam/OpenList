package cache

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	log "github.com/sirupsen/logrus"
)

var (
	cacheGcCron *cron.Cron
	gcFuncs     []func()
)

func init() {
	// TODO Move to bootstrap
	cacheGcCron = cron.NewCron(time.Hour)
	cacheGcCron.Do(func() {
		log.Infof("Start cache GC")
		for _, f := range gcFuncs {
			f()
		}
	})
}
