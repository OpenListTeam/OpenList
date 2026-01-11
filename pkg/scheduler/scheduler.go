package scheduler

import (
	"context"
	"errors"
	"strings"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// OpScheduler is the main scheduler struct that manages jobs.
type OpScheduler struct {
	Name         string
	scheduler    gocron.Scheduler
	jobCancelMap jobCancelMap
	jobsMap      jobsMapType
}

type jobsMapType = *safeMap[uuid.UUID, *OpJob]

// NewOpScheduler creates a new OpScheduler instance.
func NewOpScheduler(name string, opts ...gocron.SchedulerOption) (*OpScheduler, error) {
	scheduler, err := gocron.NewScheduler(opts...)
	if err != nil {
		return nil, err
	}
	return &OpScheduler{
		scheduler:    scheduler,
		Name:         name,
		jobCancelMap: newJobCancelMap(),
		jobsMap:      newSafeMap[uuid.UUID, *OpJob](),
	}, nil
}

// RunNow runs a job immediately by its UUID.
func (o *OpScheduler) RunNow(jobUUID uuid.UUID) error {
	opJob, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	return opJob.job.RunNow()
}

func (o *OpScheduler) jobLabels2Tags(labels JobLabels) []string {
	tags := make([]string, 0, len(labels))
	for k, v := range labels {
		tags = append(tags, escape(k)+":"+escape(v))
	}
	return tags
}

func (o *OpScheduler) tags2JobLabels(tags []string) JobLabels {
	labels := make(JobLabels)
	for _, tag := range tags {
		parts := strings.SplitN(tag, ":", 2)
		if len(parts) == 2 {
			labels[unescape(parts[0])] = unescape(parts[1])
		}
	}
	return labels
}

func (o *OpScheduler) buildJobParams(ctx context.Context, jobUUID uuid.UUID, runner JobRunner, params ...any) (gocron.Task, context.Context, context.CancelFunc) {
	jobCtx, cancel := context.WithCancel(ctx)
	var finalParams []any
	if len(params) == 0 {
		finalParams = []any{jobCtx}
	} else {
		finalParams = make([]any, 0, len(params)+1)
		finalParams = append(finalParams, jobCtx)
		finalParams = append(finalParams, params...)
	}
	task := gocron.NewTask(func(ctx context.Context, params ...any) error {
		// check if job is exists and not disabled
		j, exists := o.jobsMap.Get(jobUUID)
		// In theory the job should always exist, but check just in case
		if !exists {
			return nil
		}
		// check disabled status
		j.disableRWMutex.RLock()
		disabled := j.disabled
		j.disableRWMutex.RUnlock()
		if disabled {
			return nil
		}
		return runner(ctx, params...)
	}, finalParams...)
	return task, jobCtx, cancel
}

// NewJob creates and schedules a new job.
func (o *OpScheduler) NewJob(
	ctx context.Context,
	jobName string,
	cron gocron.JobDefinition,
	labels JobLabels,
	runner JobRunner, params ...any) (*OpJob, error) {
	jobUUID := uuid.New()
	tags := o.jobLabels2Tags(labels)
	task, jobCtx, cancel := o.buildJobParams(ctx, jobUUID, runner, params...)
	job, err := o.scheduler.NewJob(cron, task, gocron.WithIdentifier(jobUUID), gocron.WithContext(jobCtx), gocron.WithName(jobName), gocron.WithTags(tags...))
	if err != nil {
		cancel()
		return nil, err
	}
	// save the cancel func
	o.jobCancelMap.Set(jobUUID, cancel)
	// save the job
	opJob := newOpJob(job, false)
	o.jobsMap.Set(jobUUID, opJob)
	return opJob, nil
}

// UpdateJob updates an existing job by its UUID.
func (o *OpScheduler) UpdateJob(
	ctx context.Context,
	jobUUID uuid.UUID,
	jobName string,
	cron gocron.JobDefinition,
	disabled bool,
	labels JobLabels,
	runner JobRunner, params ...any) error {
	// Stop and remove the existing job
	err := o.RemoveJobs(jobUUID)
	if err != nil {
		return err
	}
	task, jobCtx, cancel := o.buildJobParams(ctx, jobUUID, runner, params...)
	tags := o.jobLabels2Tags(labels)
	job, err := o.scheduler.Update(
		jobUUID, cron, task,
		gocron.WithContext(jobCtx), gocron.WithName(jobName), gocron.WithTags(tags...),
	)
	if err != nil {
		cancel()
		return err
	}
	// save cancel func
	o.jobCancelMap.Set(jobUUID, cancel)
	// save job
	opJob := newOpJob(job, disabled)
	o.jobsMap.Set(jobUUID, opJob)
	return nil
}

// GetJob retrieves a job by its UUID.
func (o *OpScheduler) GetJob(jobUUID uuid.UUID) (*OpJob, bool) {
	return o.jobsMap.Get(jobUUID)
}

// GetJobsByLabels retrieves jobs that have all of the provided labels.
func (o *OpScheduler) GetJobsByLabels(labels JobLabels) []*OpJob {
	var result []*OpJob
	filterLabels(o.jobsMap, func(j *OpJob) {
		result = append(result, j)
	}, labels)
	return result
}

// EnableJob enables a job by its UUID.
func (o *OpScheduler) EnableJob(jobUUID uuid.UUID) error {
	opJob, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	opJob.disableRWMutex.Lock()
	opJob.disabled = false
	opJob.disableRWMutex.Unlock()
	return nil
}

// DisableJob disables a job by its UUID.
func (o *OpScheduler) DisableJob(jobUUID uuid.UUID) error {
	opJob, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	opJob.disableRWMutex.Lock()
	opJob.disabled = true
	opJob.disableRWMutex.Unlock()
	return nil
}

// StopAndDisableJob stops and disables a job by its UUID.
func (o *OpScheduler) StopAndDisableJob(jobUUID uuid.UUID) error {
	err := o.StopJobs(jobUUID)
	if err != nil {
		return err
	}
	return o.DisableJob(jobUUID)
}

// StopJobs stops jobs by their UUIDs.
func (o *OpScheduler) StopJobs(jobUUIDs ...uuid.UUID) error {
	if len(jobUUIDs) == 0 {
		return nil
	}
	for _, jobID := range jobUUIDs {
		cancelFunc, exists := o.jobCancelMap.Get(jobID)
		if !exists {
			return errors.New("job not found: " + jobID.String())
		}
		cancelFunc()
	}
	return nil
}

// RemoveJobs removes jobs by their UUIDs.
func (o *OpScheduler) RemoveJobs(jobUUIDs ...uuid.UUID) error {
	if len(jobUUIDs) == 0 {
		return nil
	}
	for _, jobID := range jobUUIDs {
		err := o.scheduler.RemoveJob(jobID)
		if err != nil {
			return err
		}
		// Remove the cancel func
		o.jobCancelMap.Delete(jobID)
		// Remove from jobsMap
		o.jobsMap.Delete(jobID)
	}
	return nil
}

// RemoveJobByTags removes all jobs that have all of the provided labels.
func (o *OpScheduler) RemoveJobByLabels(labels JobLabels) error {
	if len(labels) == 0 {
		return nil
	}
	needRemovedJobsUUID := make([]uuid.UUID, 0)
	filterLabels(
		o.jobsMap,
		func(j *OpJob) {
			needRemovedJobsUUID = append(needRemovedJobsUUID, j.ID())
		},
		labels,
	)
	if len(needRemovedJobsUUID) > 0 {
		return o.RemoveJobs(needRemovedJobsUUID...)
	}
	return nil
}

// StopJobByLabels stops all jobs that have all of the provided labels.
func (o *OpScheduler) StopJobByLabels(labels JobLabels) error {
	if len(labels) == 0 {
		return nil
	}
	needStopJobsUUID := make([]uuid.UUID, 0)
	filterLabels(
		o.jobsMap,
		func(j *OpJob) {
			needStopJobsUUID = append(needStopJobsUUID, j.ID())
		},
		labels,
	)
	if len(needStopJobsUUID) > 0 {
		return o.StopJobs(needStopJobsUUID...)
	}
	return nil
}

// StopAndRemoveJobs stops and removes jobs by their UUIDs.
func (o *OpScheduler) StopAndRemoveJobs(jobUUID ...uuid.UUID) error {
	for _, jobID := range jobUUID {
		if err := o.StopJobs(jobID); err != nil {
			return err
		}
		if err := o.RemoveJobs(jobID); err != nil {
			return err
		}
	}
	return nil
}

// StopAndRemoveJobByLabels stops and removes jobs by their labels.
func (o *OpScheduler) StopAndRemoveJobByLabels(labels JobLabels) error {
	if err := o.StopJobByLabels(labels); err != nil {
		return err
	}
	return o.RemoveJobByLabels(labels)
}

// Start starts the scheduler.
func (o *OpScheduler) Start() {
	o.scheduler.Start()
}

// Close is an alias for Shutdown.
func (o *OpScheduler) Close() error {
	return o.Shutdown()
}

// Shutdown stops the scheduler.
func (o *OpScheduler) Shutdown() error {
	return o.scheduler.Shutdown()
}

// StopAllJobs stops all jobs in the scheduler.
func (o *OpScheduler) StopAllJobs() error {
	o.jobCancelMap.ForEach(func(u uuid.UUID, cf context.CancelFunc) {
		cf()
	})
	return nil
}

// RemoveAllJobs removes all jobs from the scheduler.
func (o *OpScheduler) RemoveAllJobs() error {
	var errs []error
	if err := o.scheduler.StopJobs(); err != nil {
		errs = append(errs, err)
	}
	for _, job := range o.scheduler.Jobs() {
		if err := o.scheduler.RemoveJob(job.ID()); err != nil {
			errs = append(errs, err)
		}
	}
	o.jobCancelMap.Clear()
	o.jobsMap.Clear()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
