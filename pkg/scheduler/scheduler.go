package scheduler

import (
	"context"
	"errors"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

// label的连接符
const labelSep = "="

type OpScheduler struct {
	Name           string
	scheduler      gocron.Scheduler
	jobCancelMap   jobCancelMap
	jobDisabledMap *safeMap[uuid.UUID, bool]
	jobsMap        *safeMap[uuid.UUID, *OpJob]
}

func NewOpScheduler(name string, opts ...gocron.SchedulerOption) (*OpScheduler, error) {
	scheduler, err := gocron.NewScheduler(opts...)
	if err != nil {
		return nil, err
	}
	return &OpScheduler{
		scheduler:      scheduler,
		Name:           name,
		jobCancelMap:   newJobCancelMap(),
		jobDisabledMap: newSafeMap[uuid.UUID, bool](),
		jobsMap:        newSafeMap[uuid.UUID, *OpJob](),
	}, nil
}

func (o *OpScheduler) NewJob(
	ctx context.Context,
	jobName string,
	cron gocron.JobDefinition,
	labels []JobLabels,
	runner JobRunner, params ...any) (*OpJob, error) {
	jobCtx, cancel := context.WithCancel(ctx)
	var finalParams []any
	if len(params) == 0 {
		finalParams = []any{jobCtx}
	} else {
		finalParams = make([]any, 0, len(params)+1)
		finalParams = append(finalParams, jobCtx)
		finalParams = append(finalParams, params...)
	}
	jobUUID := uuid.New()
	task := gocron.NewTask(func(ctx context.Context, params ...any) error {
		// 判断是否被禁用
		if disabled, exists := o.jobDisabledMap.Get(jobUUID); exists && disabled {
			return nil
		}
		return runner(ctx, params...)
	}, finalParams...)
	var tags []string
	if len(labels) > 0 {
		for _, label := range labels {
			for k, v := range label {
				tags = append(tags, k+labelSep+v)
			}
		}
	}
	job, err := o.scheduler.NewJob(cron, task, gocron.WithIdentifier(jobUUID), gocron.WithContext(jobCtx), gocron.WithName(jobName), gocron.WithTags(tags...))
	if err != nil {
		cancel()
		return nil, err
	}
	// 保存取消函数
	o.jobCancelMap.Set(jobUUID, cancel)
	disabled, exists := o.jobDisabledMap.Get(jobUUID)
	if !exists {
		disabled = false
	}
	opJob := newOpJob(job, disabled)
	o.jobsMap.Set(jobUUID, opJob)
	return opJob, nil
}

// RunNow runs a job immediately by its UUID.
func (o *OpScheduler) RunNow(jobUUID uuid.UUID) error {
	opJob, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	err := opJob.job.RunNow()
	return err
}

// UpdateJob updates an existing job by its UUID.
func (o *OpScheduler) UpdateJob(
	ctx context.Context,
	jobUUID uuid.UUID,
	jobName string,
	cron gocron.JobDefinition,
	disabled bool,
	labels []JobLabels,
	runner JobRunner, params ...any) error {
	if _, exists := o.GetJob(jobUUID); !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	// Stop and remove the existing job
	err := o.scheduler.RemoveJob(jobUUID)
	if err != nil {
		return err
	}
	o.jobsMap.Delete(jobUUID)
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
		// 判断是否被禁用
		if disabled, exists := o.jobDisabledMap.Get(jobUUID); exists && disabled {
			return nil
		}
		return runner(ctx, params...)
	}, finalParams...)
	var tags []string
	if len(labels) > 0 {
		for _, label := range labels {
			for k, v := range label {
				tags = append(tags, k+labelSep+v)
			}
		}
	}
	job, err := o.scheduler.Update(
		jobUUID, cron, task,
		gocron.WithContext(jobCtx), gocron.WithName(jobName), gocron.WithTags(tags...),
	)
	if err != nil {
		cancel()
		return err
	}
	// 保存取消函数
	o.jobCancelMap.Set(jobUUID, cancel)
	// 更新禁用状态
	o.jobDisabledMap.Set(jobUUID, disabled)
	// 更新 jobsMap
	opJob := newOpJob(job, disabled)
	o.jobsMap.Set(jobUUID, opJob)
	return nil
}

// GetJob retrieves a job by its UUID.
func (o *OpScheduler) GetJob(jobUUID uuid.UUID) (*OpJob, bool) {
	return o.jobsMap.Get(jobUUID)
}

// GetJobsByLabels retrieves jobs that have all of the provided labels.
func (o *OpScheduler) GetJobsByLabels(labels ...JobLabels) []*OpJob {
	result := make([]*OpJob, 0)
	o.jobsMap.ForEach(func(_ uuid.UUID, opJob *OpJob) {
		matched := true
		for _, label := range labels {
			for k, v := range label {
				if val, exists := opJob.Label(k); !exists || val != v {
					matched = false
					break
				}
			}
			if !matched {
				break
			}
		}
		if matched {
			result = append(result, opJob)
		}
	})
	return result
}

// DisableJob disables a job by its UUID.
func (o *OpScheduler) DisableJob(jobUUID uuid.UUID) error {
	_, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	o.jobDisabledMap.Set(jobUUID, true)
	return nil
}

// EnableJob enables a job by its UUID.
func (o *OpScheduler) EnableJob(jobUUID uuid.UUID) error {
	_, exists := o.GetJob(jobUUID)
	if !exists {
		return errors.New("job not found: " + jobUUID.String())
	}
	o.jobDisabledMap.Set(jobUUID, false)
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
func (o *OpScheduler) StopJobs(jobUUID ...uuid.UUID) error {
	for _, jobID := range jobUUID {
		cancelFunc, exists := o.jobCancelMap.Get(jobID)
		if !exists {
			return errors.New("job not found: " + jobID.String())
		}
		cancelFunc()
	}
	return nil
}

// RemoveJobs removes jobs by their UUIDs.
func (o *OpScheduler) RemoveJobs(jobUUID ...uuid.UUID) error {
	for _, jobID := range jobUUID {
		err := o.scheduler.RemoveJob(jobID)
		if err != nil {
			return err
		}
		// remove cancel func
		o.jobCancelMap.Delete(jobID)
		// remove disabled mark
		o.jobDisabledMap.Delete(jobID)
		// remove from jobsMap
		o.jobsMap.Delete(jobID)
	}
	return nil
}

// RemoveJobByTags removes all jobs that have all of the provided labels.
func (o *OpScheduler) RemoveJobByLabels(labels ...JobLabels) error {
	jobs := o.scheduler.Jobs()
	if len(labels) == 0 {
		return nil
	}
	needRemovedJobsUUID := make([]uuid.UUID, 0)
	for _, job := range jobs {
		matched := true
		for _, label := range labels {
			for k, v := range label {
				exists := sliceHasItem(job.Tags(), k+labelSep+v)
				if !exists {
					matched = false
					break
				}
			}
			if !matched {
				break
			}
		}
		if matched {
			needRemovedJobsUUID = append(needRemovedJobsUUID, job.ID())
		}
	}
	if len(needRemovedJobsUUID) > 0 {
		return o.RemoveJobs(needRemovedJobsUUID...)
	}
	return nil
}

// StopJobByLabels stops all jobs that have all of the provided labels.
func (o *OpScheduler) StopJobByLabels(labels ...JobLabels) error {
	jobs := o.scheduler.Jobs()
	if len(labels) == 0 {
		return nil
	}
	needStopJobsUUID := make([]uuid.UUID, 0)
	for _, job := range jobs {
		matched := true
		for _, label := range labels {
			for k, v := range label {
				exists := sliceHasItem(job.Tags(), k+labelSep+v)
				if !exists {
					matched = false
					break
				}
			}
			if !matched {
				break
			}
		}
		if matched {
			needStopJobsUUID = append(needStopJobsUUID, job.ID())
		}
	}
	if len(needStopJobsUUID) > 0 {
		return o.StopJobs(needStopJobsUUID...)
	}
	return nil
}

// StopAndRemoveJobs stops and removes jobs by their UUIDs.
func (o *OpScheduler) StopAndRemoveJobs(jobUUID ...uuid.UUID) {
	for _, jobID := range jobUUID {
		_ = o.StopJobs(jobID)
		_ = o.RemoveJobs(jobID)
	}
}

// StopAndRemoveJobByLabels stops and removes jobs by their labels.
func (o *OpScheduler) StopAndRemoveJobByLabels(labels ...JobLabels) {
	_ = o.StopJobByLabels(labels...)
	_ = o.RemoveJobByLabels(labels...)
}

// Start starts the scheduler.
func (o *OpScheduler) Start() error {
	o.scheduler.Start()
	return nil
}

// Close is an alias for Shutdown.
func (o *OpScheduler) Close() error {
	return o.Shutdown()
}

// Shutdown stops the scheduler.
func (o *OpScheduler) Shutdown() error {
	o.scheduler.Shutdown()
	return nil
}

func (o *OpScheduler) StopAllJobs() error {
	o.scheduler.StopJobs()
	return nil
}

func (o *OpScheduler) RemoveAllJobs() error {
	o.scheduler.StopJobs()
	for _, job := range o.scheduler.Jobs() {
		o.scheduler.RemoveJob(job.ID())
	}
	o.jobCancelMap.Clear()
	o.jobDisabledMap.Clear()
	o.jobsMap.Clear()
	return nil
}
