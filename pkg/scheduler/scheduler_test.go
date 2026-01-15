package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
)

const (
	fastInterval   = 50 * time.Millisecond
	fasterInterval = 20 * time.Millisecond
	defaultTimeout = 2 * time.Second
	shortWait      = 300 * time.Millisecond
)

var donothingRunner = func(ctx context.Context) error { return nil }

// TestGoCron sanity-checks direct gocron usage with immediate execution.
func TestGoCron(t *testing.T) {
	s, err := gocron.NewScheduler(gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	arg0 := 0
	arg1 := "arg1"
	executeCalled := make(chan bool, 1)
	job, err := s.NewJob(
		gocron.DurationJob(fastInterval),
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

// TestSchedulerNormal verifies a normal job runs with provided params and labels.
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	t.Log("registry Job")
	afterCreated, err := s.NewJob(
		NewJobBuilder().
			Ctx(ctx).
			ByDuration(fastInterval).
			Name("test-job").
			Labels(labels).
			Runner(runner, arg0, arg1),
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

// TestDisabledJob ensures a job created disabled does not execute.
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).
			Name("test-job").
			ByDuration(fastInterval).
			Labels(labels).
			Runner(runner),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if job, ok := s.GetJob(afterCreated.ID()); !ok {
		t.Fatalf("expected disabled job to exist after creation")
	} else if !job.Disabled() {
		t.Fatalf("expected job %s to be disabled", job.ID())
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
	case <-time.After(shortWait):
		t.Log("disabled job did not run as expected")
	}
	t.Log("test complete for disabled job")
}

// TestEnableJob ensures enabling a disabled job allows execution.
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).
			Name("test-job").
			ByDuration(fastInterval).
			Disabled(true).
			Labels(labels).
			Runner(runner),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	// enabled
	err = s.EnableJob(afterCreated.ID())
	if err != nil {
		t.Fatalf("enable job fail %v", err)
	}
	if job, ok := s.GetJob(afterCreated.ID()); !ok {
		t.Fatalf("expected job to exist after enable")
	} else if job.Disabled() {
		t.Fatalf("job %s should be enabled after EnableJob", job.ID())
	}
	// check the channel to see if the job ran
	select {
	case count := <-chanCount:
		t.Logf("success run, count: %d", count)
	case <-time.After(defaultTimeout):
		t.Fatalf("enabled job did not run as expected")
	}
	t.Log("test complete for enable job")
}

// TestRemoveJob ensures removing a job deletes it and prevents execution.
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
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	t.Log("register disabled job")
	afterCreated, err := s.NewJob(
		NewJobBuilder().
			Ctx(ctx).
			Name("test-job").
			ByDuration(time.Hour).
			Labels(labels).
			Runner(runner),
	)
	// avoid blocking if the channel already has a value
	select {
	case chanCount <- 0:
	default:
	}
	err = s.RemoveJobs(afterCreated.ID())
	if err != nil {
		t.Fatalf("remove job %s err: %v", afterCreated.ID(), err)
	}
	j, exists := s.GetJob(afterCreated.ID())
	if exists || j != nil {
		t.Fatalf("job %s exists after removed", afterCreated.ID())
	}
	// check the channel to see if the job ran
	select {
	case count := <-chanCount:
		if count > 0 {
			t.Fatalf("removed job ran unexpectedly, count: %d", count)
		}
	case <-time.After(defaultTimeout):
		t.Log("removed job did not run as expected")
	}
	t.Log("test complete for removed job")

}

// TestDisableJobMethod ensures DisableJob marks an existing job disabled and prevents RunNow(false) from executing it.
func TestDisableJobMethod(t *testing.T) {
	s, err := NewOpScheduler("test-disable-job", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{"env": "test"}
	executed := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	job, err := s.NewJob(
		NewJobBuilder().
			Ctx(ctx).
			Name("test-job").
			ByDuration(time.Hour).
			Labels(labels).
			Runner(func(ctx context.Context) error {
				executed <- true
				return nil
			}),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if err := s.DisableJob(job.ID()); err != nil {
		t.Fatalf("disable job failed: %v", err)
	}
	updated, ok := s.GetJob(job.ID())
	if !ok || !updated.Disabled() {
		t.Fatalf("expected job disabled")
	}
	if err := s.RunNow(job.ID(), false); err != nil {
		t.Fatalf("run now failed for %s: %v", job.ID(), err)
	}
	select {
	case <-executed:
		t.Fatalf("disabled job should not run")
	case <-time.After(shortWait):
	}
}

// TestRunNowForceExecutesJob ensures RunNow(true) triggers execution even on demand.
func TestRunNowForceExecutesJob(t *testing.T) {
	s, err := NewOpScheduler("test-run-now-force", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	labels := JobLabels{"env": "test"}
	executed := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	job, err := s.NewJob(
		NewJobBuilder().
			Ctx(ctx).
			Name("force-run-job").
			ByDuration(time.Hour).
			Labels(labels).
			Disabled(true).
			Runner(func(ctx context.Context) error {
				executed <- true
				return nil
			}),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	if _, ok := s.GetJob(job.ID()); !ok {
		t.Fatalf("force-run job not found after creation")
	}
	if err := s.RunNow(job.ID(), true); err != nil {
		t.Fatalf("force run failed for %s: %v", job.ID(), err)
	}
	select {
	case <-executed:
		return
	case <-time.After(defaultTimeout):
		t.Fatalf("force run did not execute")
	}
}

// TestUpdateJobLabelsAndEnable ensures UpdateJob toggles disabled->enabled and updates labels.
func TestUpdateJobLabelsAndEnable(t *testing.T) {
	s, err := NewOpScheduler("test-update-toggle", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	initialLabels := JobLabels{"env": "old", "team": "ops"}
	job, err := s.NewJob(
		NewJobBuilder().
			Ctx(ctx).
			Name("update-job").
			ByDuration(time.Hour).
			Labels(initialLabels).
			Disabled(true).
			Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	updatedLabels := JobLabels{"env": "new", "team": "dev"}
	executed := make(chan bool, 1)
	if err := s.UpdateJob(
		job.ID(),
		NewJobBuilder().Ctx(ctx).Name("update-job-new").ByDuration(fastInterval).Labels(updatedLabels).Runner(func(ctx context.Context) error {
			executed <- true
			return nil
		}),
	); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	updated, ok := s.GetJob(job.ID())
	if !ok {
		t.Fatalf("job not found after update")
	}
	if updated.Disabled() {
		t.Fatalf("job should be enabled after update")
	}
	if updated.Name() != "update-job-new" {
		t.Fatalf("unexpected name after update: %s", updated.Name())
	}
	labels := updated.Labels()
	if labels["env"] != "new" || labels["team"] != "dev" {
		t.Fatalf("labels not updated: %+v", labels)
	}
	select {
	case <-executed:
	case <-time.After(defaultTimeout):
		t.Fatalf("updated job did not run")
	}
}

// TestRemoveJobsLeavesOthers removes one job while keeping another running.
func TestRemoveJobsLeavesOthers(t *testing.T) {
	s, err := NewOpScheduler("test-remove-jobs", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	keepRan := make(chan bool, 1)
	removeRan := make(chan bool, 1)
	jobRemove, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("remove-me").ByDuration(fastInterval).Label("env", "remove").
			Runner(
				func(ctx context.Context) error {
					removeRan <- true
					return nil
				},
			),
	)
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}
	jobKeep, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("keep-me").ByDuration(fastInterval).Label("env", "keep").
			Runner(
				func(ctx context.Context) error {
					keepRan <- true
					return nil
				},
			),
	)
	if err != nil {
		t.Fatalf("failed to create keep job: %v", err)
	}
	if err := s.RemoveJobs(jobRemove.ID()); err != nil {
		t.Fatalf("remove jobs failed for %s: %v", jobRemove.ID(), err)
	}
	// reset channels
	removeRan <- false
	keepRan <- false
	if _, ok := s.GetJob(jobRemove.ID()); ok {
		t.Fatalf("removed job still exists: %s", jobRemove.ID())
	}
	if keepJob, ok := s.GetJob(jobKeep.ID()); !ok {
		t.Fatalf("kept job missing: %s", jobKeep.ID())
	} else if keepJob.Labels()["env"] != "keep" {
		t.Fatalf("kept job label mismatch: got %q want %q", keepJob.Labels()["env"], "keep")
	}
	select {
	case <-removeRan:
		t.Fatalf("removed job executed")
	case <-time.After(shortWait):
	}
	select {
	case <-keepRan:
	case <-time.After(defaultTimeout):
		t.Fatalf("kept job did not execute")
	}
	// ensure keep job still exists
	if _, ok := s.GetJob(jobKeep.ID()); !ok {
		t.Fatalf("kept job missing: %s", jobKeep.ID())
	}
}

// TestRemoveJobByLabels removes all jobs matching specific labels while keeping others.
func TestRemoveJobByLabels(t *testing.T) {
	s, err := NewOpScheduler("test-remove-by-label", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	labelsDev := JobLabels{"env": "dev"}
	labelsProd := JobLabels{"env": "prod"}

	_, err = s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("dev-1").ByDuration(time.Hour).Labels(labelsDev).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create dev-1: %v", err)
	}
	devTwo, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("dev-2").ByDuration(time.Hour).Labels(labelsDev).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create dev-2: %v", err)
	}
	prod, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("dev-2").ByDuration(time.Hour).Labels(labelsProd).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create prod: %v", err)
	}
	if err := s.RemoveJobByLabels(labelsDev); err != nil {
		t.Fatalf("remove by labels failed for %v: %v", labelsDev, err)
	}
	if _, ok := s.GetJob(devTwo.ID()); ok {
		t.Fatalf("dev job still exists after removal: %s labels=%v", devTwo.ID(), labelsDev)
	}
	if _, ok := s.GetJob(prod.ID()); !ok {
		t.Fatalf("prod job should remain: %s labels=%v", prod.ID(), labelsProd)
	}
}

// TestGetJobsByLabelsFilters verifies label-based filtering returns matching jobs only.
func TestGetJobsByLabelsFilters(t *testing.T) {
	s, err := NewOpScheduler("test-get-by-labels", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	labelsA := JobLabels{"env": "dev", "team": "a"}
	labelsB := JobLabels{"env": "dev", "team": "b"}
	labelsC := JobLabels{"env": "prod", "team": "a"}
	jobA, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("job-a").ByDuration(time.Hour).Labels(labelsA).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job-a: %v", err)
	}
	jobB, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("job-b").ByDuration(time.Hour).Labels(labelsB).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job-b: %v", err)
	}
	_, err = s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("job-c").ByDuration(time.Hour).Labels(labelsC).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job-c: %v", err)
	}
	devJobs := s.GetJobsByLabels(JobLabels{"env": "dev"})
	if len(devJobs) != 2 {
		t.Fatalf("expected 2 dev jobs for env=dev, got %d", len(devJobs))
	}
	var seenA, seenB bool
	for _, j := range devJobs {
		if j.ID() == jobA.ID() {
			seenA = true
		}
		if j.ID() == jobB.ID() {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("missing dev jobs: seenA=%v seenB=%v", seenA, seenB)
	}
}

// TestRemoveAllJobs clears all jobs and verifies none remain runnable.
func TestRemoveAllJobs(t *testing.T) {
	s, err := NewOpScheduler("test-remove-all", gocron.WithLocation(time.Local))
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}
	s.Start()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	labels := JobLabels{"env": "test"}
	job1, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("job-1").ByDuration(time.Hour).Labels(labels).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job1: %v", err)
	}
	job2, err := s.NewJob(
		NewJobBuilder().Ctx(ctx).Name("job-2").ByDuration(time.Hour).Labels(labels).Runner(donothingRunner),
	)
	if err != nil {
		t.Fatalf("failed to create job2: %v", err)
	}
	if err := s.RemoveAllJobs(); err != nil {
		t.Fatalf("remove all jobs failed: %v", err)
	}
	if _, ok := s.GetJob(job1.ID()); ok {
		t.Fatalf("job1 still exists after remove all: %s", job1.ID())
	}
	if _, ok := s.GetJob(job2.ID()); ok {
		t.Fatalf("job2 still exists after remove all: %s", job2.ID())
	}
	if got := s.GetJobsByLabels(JobLabels{"env": "test"}); len(got) != 0 {
		t.Fatalf("expected no jobs after remove all, got %d", len(got))
	}
	if err := s.RunNow(job1.ID(), false); err == nil {
		t.Fatalf("expected error running removed job: %s", job1.ID())
	}
}
