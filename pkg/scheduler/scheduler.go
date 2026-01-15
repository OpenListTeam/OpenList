package scheduler

import (
	"context"
	"errors"
	"reflect"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// jobsMapType is a thread-safe map for storing jobs.
type jobsMapType = *safeMap[uuid.UUID, gocron.Job]

// jobDisabledMapType is a thread-safe map for storing boolean values.
type jobDisabledMapType = *safeMap[uuid.UUID, bool]

// OpScheduler is the main scheduler struct that manages jobs.
type OpScheduler struct {
	// Name is an optional human-readable identifier for this scheduler instance.
	// Callers can use it for logging, metrics, or debugging when working with
	// multiple OpScheduler instances.
	Name           string
	scheduler      gocron.Scheduler
	jobsMap        jobsMapType
	jobDisabledMap jobDisabledMapType
}

// NewOpScheduler creates a new OpScheduler instance.
func NewOpScheduler(name string, opts ...gocron.SchedulerOption) (*OpScheduler, error) {
	scheduler, err := gocron.NewScheduler(opts...)
	if err != nil {
		return nil, err
	}
	return &OpScheduler{
		scheduler:      scheduler,
		Name:           name,
		jobDisabledMap: newSafeMap[uuid.UUID, bool](),
		jobsMap:        newSafeMap[uuid.UUID, gocron.Job](),
	}, nil
}

// RunNow runs a job immediately by its UUID.
func (o *OpScheduler) RunNow(jobUUID uuid.UUID, force bool) error {
	job, exists := o._internalGetCronJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	if !force && o.jobIsDisabled(jobUUID) {
		// job is disabled, do not run
		return nil
	}
	return job.RunNow()
}

// jobIsDisabled checks if a job is disabled.
func (o *OpScheduler) jobIsDisabled(jobUUID uuid.UUID) bool {
	disabled, exists := o.jobDisabledMap.Get(jobUUID)
	return exists && disabled
}

// buildJobParams builds a gocron.Task with the provided parameters.
func (o *OpScheduler) buildJobParams(
	jobUUID uuid.UUID,
	runner JobRunner,
	params []any,
) (gocron.Task, error) {
	f := reflect.ValueOf(runner)
	if f.IsZero() {
		return nil, errors.New("runner is nil")
	}
	if len(params)+1 != f.Type().NumIn() {
		return nil, errors.New("number of params does not match runner function signature (expected N params plus context parameter)")
	}
	// check runner as function and NumIn is match params length
	task := gocron.NewTask(func(_ctx context.Context, params []any) error {
		// check if job is exists and not disabled
		j, exists := o._internalGetCronJob(jobUUID)
		// In theory the job should always exist, but check just in case
		if !exists {
			return errors.New("cron job not found")
		}
		// check disabled status
		if o.jobIsDisabled(j.ID()) {
			return nil
		}
		in := make([]reflect.Value, len(params)+1)
		in[0] = reflect.ValueOf(_ctx)
		for k, param := range params {
			in[k+1] = reflect.ValueOf(param)
		}
		// call runner with params appended context at first
		returnValues := f.Call(in)
		result := returnValues[0].Interface()
		// if runner returns an error, return it
		if result == nil {
			return nil
		}
		return result.(error)
	}, params)
	return task, nil
}

// NewJobByBuilder creates and shedules a new job by builder
func (o *OpScheduler) NewJob(jb *jobBuilder) (*OpJob, error) {
	if jb.runner == nil {
		return nil, errors.New("runner is nil")
	}
	if jb.jobName == "" {
		return nil, errors.New("jobName is empty")
	}
	opts := jb._internalGetOptions()
	var jobUUID uuid.UUID = jb._internalGetOrCreateID()
	task, err := o.buildJobParams(jobUUID, jb.runner, jb.params)
	if err != nil {
		return nil, err
	}
	job, err := o.scheduler.NewJob(
		jb.cron,
		task,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	o.jobsMap.Set(jobUUID, job)
	if jb.disabled {
		o.jobDisabledMap.Set(jobUUID, true)
	}
	return newOpJob(job, jb.disabled), nil
}

// UpdateJob updates an existing job by its UUID using a job builder.
func (o *OpScheduler) UpdateJob(
	jobUUID uuid.UUID,
	jb *jobBuilder,
) error {
	// Stop and remove the existing job
	if exists := o.Exists(jobUUID); !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	task, err := o.buildJobParams(jobUUID, jb.runner, jb.params)
	if err != nil {
		return err
	}
	// update the ID of jobBuilder to ensure consistency
	jb.ID(jobUUID)
	opts := jb._internalGetOptions()
	job, err := o.scheduler.Update(
		jobUUID, jb.cron, task,
		opts...,
	)
	if err != nil {
		return err
	}
	// Save job
	o.jobsMap.Set(jobUUID, job)
	// Update disabled status
	if jb.disabled {
		o.jobDisabledMap.Set(jobUUID, true)
	} else {
		o.jobDisabledMap.Delete(jobUUID)
	}
	return nil
}

// Exists checks whether a job with the given UUID is registered in the scheduler.
func (o *OpScheduler) Exists(uuid uuid.UUID) bool {
	_, exists := o._internalGetCronJob(uuid)
	return exists
}

// _internalGetCronJob retrieves a gocron.Job by its UUID.
func (o *OpScheduler) _internalGetCronJob(jobUUID uuid.UUID) (gocron.Job, bool) {
	return o.jobsMap.Get(jobUUID)
}

// GetJob retrieves a job by its UUID.
func (o *OpScheduler) GetJob(jobUUID uuid.UUID) (*OpJob, bool) {
	job, exists := o._internalGetCronJob(jobUUID)
	if !exists {
		return nil, false
	}
	return newOpJob(job, o.jobIsDisabled(jobUUID)), true
}

// GetJobsByLabels retrieves jobs that have all of the provided labels.
func (o *OpScheduler) GetJobsByLabels(labels JobLabels) []*OpJob {
	var result []*OpJob
	o.filterLabels(labels, func(j gocron.Job, jobLabels JobLabels) {
		result = append(result, newOpJob(j, o.jobIsDisabled(j.ID())))
	})
	return result
}

// EnableJob enables a job by its UUID.
func (o *OpScheduler) EnableJob(jobUUID uuid.UUID) error {
	if !o.Exists(jobUUID) {
		return errors.New("job not found: " + jobUUID.String())
	}
	o.jobDisabledMap.Delete(jobUUID)
	return nil
}

// DisableJob disables a job by its UUID.
func (o *OpScheduler) DisableJob(jobUUID uuid.UUID) error {
	if !o.Exists(jobUUID) {
		return errors.New("job not found: " + jobUUID.String())
	}
	o.jobDisabledMap.Set(jobUUID, true)
	return nil
}

// RemoveJobs removes jobs by their UUIDs.
func (o *OpScheduler) RemoveJobs(jobUUIDs ...uuid.UUID) error {
	if len(jobUUIDs) == 0 {
		return nil
	}
	var errs []error
	for _, jobID := range jobUUIDs {
		err := o.scheduler.RemoveJob(jobID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// Remove from jobsMap
		o.jobsMap.Delete(jobID)
		// Remove from disabled map
		o.jobDisabledMap.Delete(jobID)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// filterLabels filters jobs in the jobsMap based on the provided labels and applies the action function to matching jobs.
func (o *OpScheduler) filterLabels(
	labels JobLabels,
	action func(gocron.Job, JobLabels),
) {
	if len(o.jobsMap.data) == 0 {
		return
	}
	o.jobsMap.ForEach(func(_ uuid.UUID, job gocron.Job) {
		jobLabels := tags2JobLabels(job.Tags())
		matches := true
		for k, v := range labels {
			if jobVal, exists := jobLabels[k]; !exists || jobVal != v {
				matches = false
				break
			}
		}
		if matches {
			action(job, jobLabels)
		}
	})
}

// RemoveJobByLabels removes all jobs that have all of the provided labels.
func (o *OpScheduler) RemoveJobByLabels(labels JobLabels) error {
	if len(labels) == 0 {
		return nil
	}
	needRemovedJobsUUID := make([]uuid.UUID, 0)
	o.filterLabels(
		labels,
		func(j gocron.Job, jobLabels JobLabels) {
			needRemovedJobsUUID = append(needRemovedJobsUUID, j.ID())
		},
	)
	if len(needRemovedJobsUUID) > 0 {
		return o.RemoveJobs(needRemovedJobsUUID...)
	}
	return nil
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
	return o.scheduler.StopJobs()
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
	o.jobDisabledMap.Clear()
	o.jobsMap.Clear()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
