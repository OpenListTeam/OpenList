package teldrive

import (
	"fmt"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

func NewCopyManager(ctx context.Context, concurrent int, d *Teldrive) *CopyManager {
	g, ctx := errgroup.WithContext(ctx)

	return &CopyManager{
		TaskChan: make(chan CopyTask, concurrent*2),
		Sem:      semaphore.NewWeighted(int64(concurrent)),
		G:        g,
		Ctx:      ctx,
		d:        d,
	}
}

func (cm *CopyManager) startWorkers() {
	workerCount := cap(cm.TaskChan) / 2
	for i := 0; i < workerCount; i++ {
		cm.G.Go(func() error {
			return cm.worker()
		})
	}
}

func (cm *CopyManager) worker() error {
	for {
		select {
		case task, ok := <-cm.TaskChan:
			if !ok {
				return nil
			}

			if err := cm.Sem.Acquire(cm.Ctx, 1); err != nil {
				return err
			}

			var err error

			err = cm.processFile(task)

			cm.Sem.Release(1)

			if err != nil {
				return fmt.Errorf("task processing failed: %w", err)
			}

		case <-cm.Ctx.Done():
			return cm.Ctx.Err()
		}
	}
}

func (cm *CopyManager) generateTasks(ctx context.Context, srcObj, dstDir model.Obj) error {
	if srcObj.IsDir() {
		return cm.generateFolderTasks(ctx, srcObj, dstDir)
	} else {
		// add single file task directly
		select {
		case cm.TaskChan <- CopyTask{SrcObj: srcObj, DstDir: dstDir}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (cm *CopyManager) generateFolderTasks(ctx context.Context, srcDir, dstDir model.Obj) error {
	objs, err := cm.d.List(ctx, srcDir, model.ListArgs{})
	if err != nil {
		return fmt.Errorf("failed to list directory %s: %w", srcDir.GetPath(), err)
	}

	err = cm.d.MakeDir(cm.Ctx, dstDir, srcDir.GetName())
	if err != nil || len(objs) == 0 {
		return err
	}
	newDstDir := &model.Object{
		ID:       dstDir.GetID(),
		Path:     dstDir.GetPath() + "/" + srcDir.GetName(),
		Name:     srcDir.GetName(),
		IsFolder: true,
	}

	for _, file := range objs {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}

		srcFile := &model.Object{
			ID:       file.GetID(),
			Path:     srcDir.GetPath() + "/" + file.GetName(),
			Name:     file.GetName(),
			IsFolder: file.IsDir(),
		}

		// 递归生成任务
		if err := cm.generateTasks(ctx, srcFile, newDstDir); err != nil {
			return err
		}
	}

	return nil
}

func (cm *CopyManager) processFile(task CopyTask) error {
	return cm.copySingleFile(cm.Ctx, task.SrcObj, task.DstDir)
}

func (cm *CopyManager) copySingleFile(ctx context.Context, srcObj, dstDir model.Obj) error {
	// `override copy mode` should delete the existing file
	if obj, err := cm.d.getFile(dstDir.GetPath(), srcObj.GetName(), srcObj.IsDir()); err == nil {
		if err := cm.d.Remove(ctx, obj); err != nil {
			return fmt.Errorf("failed to remove existing file: %w", err)
		}
	}

	// Do copy
	return cm.d.request(http.MethodPost, "/api/files/{id}/copy", func(req *resty.Request) {
		req.SetPathParam("id", srcObj.GetID())
		req.SetBody(base.Json{
			"newName":     srcObj.GetName(),
			"destination": dstDir.GetPath(),
		})
	}, nil)
}
