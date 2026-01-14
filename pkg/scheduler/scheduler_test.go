package scheduler

import (
	"context"
	"log"
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
	defer cancel()
	arg0 := 0
	arg1 := "arg1"
	executeCalled := make(chan bool, 1)
	job, err := s.NewJob(
		gocron.DurationJob(5*time.Second),
		gocron.NewTask(
			func(ctx context.Context, arg0 int, arg1 string) error {
				t.Logf("task is running with args: %d, %s", arg0, arg1)
				executeCalled <- true
				return nil
			},
			arg0, arg1,
		),
		gocron.WithContext(ctx),
	)
	t.Logf("job ID: %d", job.ID())
	err = job.RunNow()
	if err != nil {
		t.Fatalf("failed to run job now: %v", err)
	}
	select {
	case <-executeCalled:
		t.Log("job executed successfully")
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			t.Fatalf("job did not execute within the expected time")
		} else if ctx.Err() != nil {
			t.Fatalf("context error: %v", ctx.Err())
		}
	}
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
	t.Log("registry Job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
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

func TestDisabledJob(t *testing.T) {
	t.Log("start test for disabled job")
	s, err := NewOpScheduler("test-scheduler-disabled", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{
		"env":  "test",
		"team": "devops",
	}
	chanCount := make(chan int, 1)
	var runner JobRunner = func(ctx context.Context) error {
		t.Fatalf("disabled job should not run")
		chanCount <- 1
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// runs every 5 hours, but is disabled
		gocron.DurationJob(
			5*time.Second,
		),
		true, // disabled
		labels,
		runner,
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	// runNow
	t.Log("attempt to run disabled job immediately")
	err = s.RunNow(afterCreated.ID(), false)
	if err != nil {
		t.Fatalf("failed to run disabled job now: %v", err)
	}
	// check the channel to see if the job ran
	select {
	case count := <-chanCount:
		t.Fatalf("disabled job ran unexpectedly, count: %d", count)
	case <-time.After(5 * time.Second):
		t.Log("disabled job did not run as expected")
	}
	t.Log("test complete for disabled job")
}

// TestEnableJob
func TestEnableJob(t *testing.T) {
	t.Log("start test for enable job")
	s, err := NewOpScheduler("test-scheduler-disabled", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{
		"env":  "test",
		"team": "devops",
	}
	chanCount := make(chan int, 1)
	var runner JobRunner = func(ctx context.Context) error {
		t.Log("job has run")
		chanCount <- 1
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// runs every 5 seconds, but is disabled
		gocron.DurationJob(
			5*time.Second,
		),
		true, // disabled
		labels,
		runner,
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	// enabled
	err = s.EnableJob(afterCreated.ID())
	if err != nil {
		t.Fatalf("enable job fail %v", err)
	}
	// check the channel to see if the job ran
	select {
	case count := <-chanCount:
		t.Logf("success run, count: %d", count)
	case <-time.After(10 * time.Second):
		t.Fatalf("enabled job did not run as expected")
	}
	t.Log("test complete for enable job")
}

func TestUpdateJob(t *testing.T) {
	// create job donothing
	t.Log("start test for enable job")
	s, err := NewOpScheduler("test-scheduler-disabled", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{
		"env":  "test",
		"team": "devops",
	}
	chanCount := make(chan int, 1)
	var runner JobRunner = func(ctx context.Context) error {
		t.Log("donothing")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// runs every 5 second, but is disabled
		gocron.DurationJob(
			5*time.Second,
		),
		false, // disabled
		labels,
		runner,
	)
	// wait for 10 seconds
	time.Sleep(10 * time.Second)
	var runner2 JobRunner = func(ctx context.Context) error {
		t.Log("change the chancount")
		chanCount <- 1
		return nil
	}
	err = s.UpdateJob(
		ctx, afterCreated.ID(),
		"afterUpdate",
		gocron.DurationJob(
			1*time.Second,
		),
		false,
		labels,
		runner2,
	)
	if err != nil {
		log.Fatalf("update found err: %v", err)
	}
	j, exists := s.GetJob(afterCreated.ID())
	if !exists {
		log.Fatalf("can't found after update")
	}
	if j.Name() != "afterUpdate" {
		log.Fatalf("update name faild")
	}
	if j.Disabled() {
		log.Fatalf("update diabled faild")
	}
	select {
	case count := <-chanCount:
		t.Logf("success run, count: %d", count)
	case <-time.After(10 * time.Second):
		t.Fatalf("enabled job did not run as expected")
	}
	t.Log("test complete for enable job")
}

func TestRemoveJob(t *testing.T) {
	t.Log("start test remove job")
	// create job donothing
	t.Log("start test for enable job")
	s, err := NewOpScheduler("test-scheduler-disabled", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{
		"env":  "test",
		"team": "devops",
	}
	chanCount := make(chan int, 1)
	var runner JobRunner = func(ctx context.Context) error {
		chanCount <- 1
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		ctx,
		"test-job",
		// runs every 5 second, but is disabled
		gocron.DurationJob(
			5*time.Second,
		),
		false, // disabled
		labels,
		runner,
	)
	err = s.RemoveJobs(afterCreated.ID())
	if err != nil {
		t.Fatalf("remove job err : %v", err)
	}
	j, exists := s.GetJob(afterCreated.ID())
	if exists || j != nil {
		t.Fatalf("job exists after removed")
	}
	// reset chanCoun
	chanCount <- 0
	// check the channel to see if the job ran
	select {
	case count := <-chanCount:
		if count > 0 {
			t.Fatalf("removed job ran unexpectedly, count: %d", count)
		}
	case <-time.After(10 * time.Second):
		t.Log("removed job did not run as expected")
	}
	t.Log("test complete for removed job")

}
