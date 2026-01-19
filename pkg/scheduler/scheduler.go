// Package scheduler provides a job scheduling system using gocron.
package scheduler

import (
	"context"
	"errors"

	"github.com/OpenListTeam/OpenList/v4/pkg/generic_sync"
	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// jobsMapType is a thread-safe map for storing jobs.
type jobsMapType = *generic_sync.MapOf[uuid.UUID, gocron.Job]

// jobDisabledMapType is a thread-safe map for storing boolean values.
type jobDisabledMapType = *generic_sync.MapOf[uuid.UUID, any]

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
		jobDisabledMap: newSafeMap[uuid.UUID, any](),
		jobsMap:        newSafeMap[uuid.UUID, gocron.Job](),
	}, nil
}

// RunNow runs a job immediately by its UUID if the job is enabled.
func (o *OpScheduler) RunNow(jobUUID uuid.UUID) error {
	job, exists := o._internalGetCronJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	if o.jobIsDisabled(jobUUID) {
		// job is disabled, do not run
		return nil
	}
	return job.RunNow()
}

// jobIsDisabled checks if a job is disabled.
func (o *OpScheduler) jobIsDisabled(jobUUID uuid.UUID) bool {
	return o.jobDisabledMap.Has(jobUUID)
}

// buildJobParams builds a gocron.Task with the provided parameters.
func (o *OpScheduler) buildJobParams(
	jd *jobDefine,
) (gocron.Task, error) {
	// check runner as function and NumIn is match params length
	_task := gocron.NewTask(
		func(ctx context.Context) error {
			if o.jobIsDisabled(jd.id) {
				return nil
			}
			return jd.taskExecutor.Execute(ctx)
		},
	)
	return _task, nil
}

// NewJobByBuilder creates and schedules a new job by builder
func (o *OpScheduler) NewJob(jBuilder *jobBuilder) (*OpJob, error) {
	jd, err := jBuilder.Build()
	if err != nil {
		return nil, err
	}
	task, err := o.buildJobParams(jd)
	if err != nil {
		return nil, err
	}
	if jd.disabled {
		o.jobDisabledMap.Store(jd.id, struct{}{})
	}
	job, err := o.scheduler.NewJob(
		jd.cron,
		task,
		jd.opts...,
	)
	if err != nil {
		// remove the disabled status if job creation failed
		if jd.disabled {
			o.jobDisabledMap.Delete(jd.id)
		}
		return nil, err
	}
	o.jobsMap.Store(jd.id, job)
	return newOpJob(job, jd.disabled), nil
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
	// update the ID of jobBuilder to ensure consistency
	jb.ID(jobUUID)
	jd, err := jb.Build()
	if err != nil {
		return err
	}
	task, err := o.buildJobParams(jd)
	if err != nil {
		return err
	}
	// Update disabled status
	rawDsiabled := o.jobIsDisabled(jobUUID)
	if rawDsiabled != jd.disabled {
		if jd.disabled {
			o.jobDisabledMap.Store(jobUUID, struct{}{})
		} else {
			o.jobDisabledMap.Delete(jobUUID)
		}
	}
	job, err := o.scheduler.Update(
		jobUUID, jd.cron, task,
		jd.opts...,
	)
	if err != nil {
		// rollback disabled status if update failed
		if jd.disabled != rawDsiabled {
			if rawDsiabled {
				o.jobDisabledMap.Store(jobUUID, struct{}{})
			} else {
				o.jobDisabledMap.Delete(jobUUID)
			}
		}
		return err
	}
	// Save job
	o.jobsMap.Store(jobUUID, job)
	return nil
}

// Exists checks whether a job with the given UUID is registered in the scheduler.
func (o *OpScheduler) Exists(uuid uuid.UUID) bool {
	_, exists := o._internalGetCronJob(uuid)
	return exists
}

// _internalGetCronJob retrieves a gocron.Job by its UUID.
func (o *OpScheduler) _internalGetCronJob(jobUUID uuid.UUID) (gocron.Job, bool) {
	return o.jobsMap.Load(jobUUID)
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
	o.jobDisabledMap.Store(jobUUID, struct{}{})
	return nil
}

// RemoveJobs removes jobs by their UUIDs.
func (o *OpScheduler) RemoveJobs(waitForRemoveJobUUIDs ...uuid.UUID) error {
	if len(waitForRemoveJobUUIDs) == 0 {
		return nil
	}
	var errs []error
	// try to remove jobs one by one
	for _, jobID := range waitForRemoveJobUUIDs {
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

		existsJobIDs := make(map[uuid.UUID]bool)
		for _, item := range o.scheduler.Jobs() {
			existsJobIDs[item.ID()] = true
		}
		// if job removal failed, check if job not exists in scheduler, but still in internal maps
		for _, jobID := range waitForRemoveJobUUIDs {
			if _, exists := existsJobIDs[jobID]; exists {
				continue
			}
			// if job removal failed, but job not exists in scheduler, remove from internal maps
			o.jobsMap.Delete(jobID)
			o.jobDisabledMap.Delete(jobID)
		}
		return errors.Join(errs...)
	}
	return nil
}

// filterLabels filters jobs in the jobsMap based on the provided labels and applies the action function to matching jobs.
func (o *OpScheduler) filterLabels(
	labels JobLabels,
	action func(gocron.Job, JobLabels),
) {
	var loopFunc = func(_ uuid.UUID, job gocron.Job) bool {
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
		return true
	}
	o.jobsMap.Range(loopFunc)
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
	// First, stop all running jobs.
	if err := o.scheduler.StopJobs(); err != nil {
		errs = append(errs, err)
	}
	// Only clear the internal maps if the scheduler successfully removed all jobs.
	if len(errs) == 0 {
		o.jobDisabledMap.Clear()
		o.jobsMap.Clear()
		return nil
	}
	return errors.Join(errs...)
}
