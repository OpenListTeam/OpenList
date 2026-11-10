package feishu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func (d *Feishu) Remove(ctx context.Context, obj model.Obj) error {
	s, err := d.current()
	if err != nil {
		return err
	}
	if s.cfg.ReadOnly {
		return fmt.Errorf("Feishu storage is read-only")
	}
	o, err := unwrap(obj)
	if err != nil {
		return err
	}

	ctx, cancel := s.operationContext(ctx, time.Minute)
	defer cancel()
	root, err := s.root(ctx)
	if err != nil {
		return err
	}
	if o.ID == root {
		return fmt.Errorf("cannot delete Feishu storage root")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var result struct {
		TaskID string `json:"task_id"`
	}
	query := url.Values{"type": {o.Kind}}
	if err := s.json(ctx, http.MethodDelete, "/drive/v1/files/"+o.ID+"?"+query.Encode(), nil, &result); err != nil {
		return err
	}
	if result.TaskID == "" {
		return nil
	}

	endpoint := "/drive/v1/files/task_check?" + url.Values{"task_id": {result.TaskID}}.Encode()
	for {
		var task struct {
			Status string `json:"status"`
		}
		if err := s.json(ctx, http.MethodGet, endpoint, nil, &task); err != nil {
			return fmt.Errorf("check Feishu deletion task: %w", err)
		}
		switch task.Status {
		case "success":
			return nil
		case "fail":
			return fmt.Errorf("Feishu deletion task failed")
		case "process":
			if err := wait(ctx, time.Second); err != nil {
				return fmt.Errorf("wait for Feishu deletion task: %w", err)
			}
		default:
			return fmt.Errorf("unexpected Feishu deletion task status %q", task.Status)
		}
	}
}
