package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
)

func TestGoCron(t *testing.T) {
	s, err := gocron.NewScheduler(gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	arg0 := 0
	arg1 := "arg1"
	job, err := s.NewJob(
		gocron.DurationJob(5*time.Second),
		gocron.NewTask(
			func(ctx context.Context, arg0 int, arg1 string) error {
				t.Logf("task is running with args: %d, %s", arg0, arg1)
				return nil
			},
			arg0, arg1,
		),
		gocron.WithContext(ctx),
	)
	defer cancel()
	t.Logf("job ID: %d", job.ID())
	err = job.RunNow()
	if err != nil {
		t.Fatalf("failed to run job now: %v", err)
	}
	time.Sleep(30 * time.Second)
}

func TestSchedulerNormal(t *testing.T) {
	t.Log("start test")
	t.Logf("Localtime: %v", time.Local)
	s, err := NewOpScheduler("test-scheduler", gocron.WithLocation(time.Local))
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
	executed := make(chan bool, 1)
	var runner JobRunner = func(ctx context.Context, _arg0 int, _arg1 string) error {
		t.Log("task is running")
		if _arg0 != arg0 {
			t.Fatalf("expected _arg0 to be %d, got %v", arg0, _arg0)
		}
		if _arg1 != arg1 {
			t.Fatalf("expected _arg1 to be %q, got %v", arg1, _arg1)
		}
		executed <- true
		t.Log("task done")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Log("regsitry Job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// run every 10 seconds
		gocron.DurationJob(
			5*time.Second,
		),
		false,
		labels,
		runner,
		arg0, arg1,
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	t.Log("check the job exists")
	job, exists := s.GetJob(afterCreated.ID())
	if !exists {
		t.Fatalf("job not found after creation")
	}
	t.Log("check the job name")
	if job.Name() != "test-job" {
		t.Fatalf("expected job name to be %q, got %q", "test-job", job.Name())
	}
	t.Log("check the labels")
	jobLabels := job.Labels()
	if len(jobLabels) != len(labels) {
		t.Fatalf("expected %d labels, got %d", len(labels), len(jobLabels))
	}
	for k, v := range labels {
		if jobLabels[k] != v {
			t.Fatalf("expected label %q to be %q, got %q", k, v, jobLabels[k])
		}
	}
	t.Log("wait for job execution")
	select {
	case <-executed:
		t.Log("job executed successfully")
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("job did not execute within the expected time")
		} else if ctx.Err() != nil {
			t.Fatalf("context error: %v", ctx.Err())
		}
	}
}
