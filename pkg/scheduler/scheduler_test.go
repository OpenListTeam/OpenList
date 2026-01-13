package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
)

func TestSchedulerNormal(t *testing.T) {
	s, err := NewOpScheduler("test-scheduler")
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{
		"env":  "test",
		"team": "devops",
	}
	arg0 := 0
	arg1 := "arg1"
	// store task status
	var forTest = make(map[string]bool)
	var runner JobRunner = func(ctx context.Context, params ...any) error {
		if len(params) != 2 {
			t.Fatalf("expected 2 params, got %d", len(params))
		}
		if v0, ok := params[0].(int); !ok || v0 != arg0 {
			t.Fatalf("expected param 0 to be %d, got %v", arg0, params[0])
		}
		if v1, ok := params[1].(string); !ok || v1 != arg1 {
			t.Fatalf("expected param 1 to be %q, got %v", arg1, params[1])
		}
		forTest["runner_executed"] = true
		return nil
	}
	ctx := context.WithoutCancel(context.Background())
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// run every 1 minute
		gocron.CronJob("* * * * *", false),
		false,
		labels,
		runner,
		arg0, arg1,
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	job, exists := s.GetJob(afterCreated.ID())
	if !exists {
		t.Fatalf("job not found after creation")
	}
	if job.Name() != "test-job" {
		t.Fatalf("expected job name to be %q, got %q", "test-job", job.Name())
	}
	jobLabels := job.Labels()
	if len(jobLabels) != len(labels) {
		t.Fatalf("expected %d labels, got %d", len(labels), len(jobLabels))
	}
	for k, v := range labels {
		if jobLabels[k] != v {
			t.Fatalf("expected label %q to be %q, got %q", k, v, jobLabels[k])
		}
	}
	// wait for a short while to let the job be scheduled
	time.Sleep(2 * time.Minute)
	if !forTest["runner_executed"] {
		t.Fatalf("expected runner to be executed")
	}
	t.Logf("scheduler test passed")
}
