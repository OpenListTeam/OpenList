package search

import (
	"context"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	log "github.com/sirupsen/logrus"
)

var (
	Quit = atomic.Pointer[chan struct{}]{}
)

type indexBatch struct {
	sync.Mutex
	objs []ObjWithParent
}

func (batch *indexBatch) add(obj ObjWithParent) {
	batch.Lock()
	batch.objs = append(batch.objs, obj)
	batch.Unlock()
}

func (batch *indexBatch) take() []ObjWithParent {
	batch.Lock()
	objs := batch.objs
	batch.objs = nil
	batch.Unlock()
	return objs
}

func (batch *indexBatch) len() int {
	batch.Lock()
	defer batch.Unlock()
	return len(batch.objs)
}

func Running() bool {
	return Quit.Load() != nil
}

func BuildIndex(ctx context.Context, indexPaths, ignorePaths []string, maxDepth int, count bool) error {
	var (
		err      error
		objCount uint64 = 0
		fi       model.Obj
	)
	log.Infof("build index for: %+v", indexPaths)
	log.Infof("ignore paths: %+v", ignorePaths)
	quit := make(chan struct{}, 1)
	if !Quit.CompareAndSwap(nil, &quit) {
		// other goroutine is running
		return errs.BuildIndexIsRunning
	}
	var (
		indexMQ = &indexBatch{}
		running = atomic.Bool{} // current goroutine running
		wg      = &sync.WaitGroup{}
	)
	running.Store(true)
	wg.Add(1)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer func() {
			Quit.Store(nil)
			wg.Done()
			// notify walk to exit when StopIndex api called
			running.Store(false)
			ticker.Stop()
		}()
		tickCount := 0
		for {
			select {
			case <-ticker.C:
				tickCount += 1
				if indexMQ.len() < 1000 && tickCount != 5 {
					continue
				} else if tickCount >= 5 {
					tickCount = 0
				}
				log.Infof("index obj count: %d", objCount)
				func(messages []ObjWithParent) {
					if len(messages) != 0 {
						log.Debugf("current index: %s", messages[len(messages)-1].Parent)
					}
					if err = BatchIndex(ctx, messages); err != nil {
						log.Errorf("build index in batch error: %+v", err)
					} else {
						objCount = objCount + uint64(len(messages))
					}
					if count {
						WriteProgress(&model.IndexProgress{
							ObjCount:     objCount,
							IsDone:       false,
							LastDoneTime: nil,
						})
					}
				}(indexMQ.take())

			case <-quit:
				log.Debugf("build index for %+v received quit", indexPaths)
				eMsg := ""
				now := time.Now()
				originErr := err
				func(messages []ObjWithParent) {
					if err = BatchIndex(ctx, messages); err != nil {
						log.Errorf("build index in batch error: %+v", err)
					} else {
						objCount = objCount + uint64(len(messages))
					}
					if originErr != nil {
						log.Errorf("build index error: %+v", originErr)
						eMsg = originErr.Error()
					} else {
						log.Infof("success build index, count: %d", objCount)
					}
					if count {
						WriteProgress(&model.IndexProgress{
							ObjCount:     objCount,
							IsDone:       true,
							LastDoneTime: &now,
							Error:        eMsg,
						})
					}
				}(indexMQ.take())
				log.Debugf("build index for %+v quit success", indexPaths)
				return
			}
		}
	}()
	defer func() {
		if !running.Load() || Quit.Load() != &quit {
			log.Debugf("build index for %+v stopped by StopIndex", indexPaths)
			return
		}
		select {
		// avoid goroutine leak
		case quit <- struct{}{}:
		default:
		}
		wg.Wait()
	}()
	admin, err := op.GetAdmin()
	if err != nil {
		return err
	}
	if count {
		WriteProgress(&model.IndexProgress{
			ObjCount: 0,
			IsDone:   false,
		})
	}
	for _, indexPath := range indexPaths {
		walkFn := func(indexPath string, info model.Obj) error {
			if !running.Load() {
				return filepath.SkipDir
			}
			for _, avoidPath := range ignorePaths {
				if strings.HasPrefix(indexPath, avoidPath) {
					return filepath.SkipDir
				}
			}
			if storage, _, err := op.GetStorageAndActualPath(indexPath); err == nil {
				if storage.GetStorage().DisableIndex {
					return filepath.SkipDir
				}
			}
			// ignore root
			if indexPath == "/" {
				return nil
			}
			indexMQ.add(ObjWithParent{Obj: info, Parent: path.Dir(indexPath)})
			return nil
		}
		fi, err = fs.Get(ctx, indexPath, &fs.GetArgs{})
		if err != nil {
			return err
		}
		// TODO: run walkFS concurrently
		err = fs.WalkFS(context.WithValue(ctx, conf.UserKey, admin), maxDepth, indexPath, fi, walkFn)
		if err != nil {
			return err
		}
	}
	return nil
}

func Del(ctx context.Context, prefix string) error {
	return instance.Del(ctx, prefix)
}

func Clear(ctx context.Context) error {
	return instance.Clear(ctx)
}

func Config(ctx context.Context) searcher.Config {
	return instance.Config()
}

type snapshotUpdater interface {
	UpdateSnapshot(context.Context, string, []model.Obj) error
}

func UpdateSnapshot(ctx context.Context, parent string, objs []model.Obj) {
	if instance == nil || !instance.Config().AutoUpdate || !setting.GetBool(conf.AutoUpdateIndex) || Running() {
		return
	}
	if isIgnorePath(parent) {
		return
	}
	// only update when index have built
	progress, err := Progress()
	if err != nil {
		log.Errorf("update search index error while get progress: %+v", err)
		return
	}
	if !progress.IsDone {
		return
	}

	if updater, ok := instance.(snapshotUpdater); ok {
		if err := updater.UpdateSnapshot(ctx, parent, objs); err != nil {
			log.Errorf("update search index error for %s: %+v", parent, err)
		}
		return
	}

	nodes, err := instance.Get(ctx, parent)
	if err != nil {
		log.Errorf("update search index error while get nodes: %+v", err)
		return
	}
	removed, added := searcher.SnapshotDiff(objs, nodes)
	for _, node := range removed {
		if !op.HasStorage(path.Join(parent, node.Name)) {
			log.Debugf("delete index: %s", path.Join(parent, node.Name))
			err = instance.Del(ctx, path.Join(parent, node.Name))
			if err != nil {
				log.Errorf("update search index error while del old node: %+v", err)
				return
			}
		}
	}
	// collect files and folders to add in batch
	toAddObjs := make([]ObjWithParent, 0, len(added))
	for _, obj := range added {
		log.Debugf("add index: %s", path.Join(parent, obj.GetName()))
		toAddObjs = append(toAddObjs, ObjWithParent{Parent: parent, Obj: obj})
	}
	// batch index all files and folders at once
	if len(toAddObjs) > 0 {
		err = BatchIndex(ctx, toAddObjs)
		if err != nil {
			log.Errorf("update search index error while batch index new nodes: %+v", err)
			return
		}
	}
}
