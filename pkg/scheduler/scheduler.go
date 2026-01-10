package scheduler

import (
	"context"
	"errors"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

var schedulerMap = NewSafeMap[string, gocron.Scheduler]()
var schedulerJobCancelMap = NewSafeMap[string, jobCancelMap]()

func RegsiterScheduler(name string, ctx context.Context, factory SchedulerFactory) error {
	scheduler, err := factory(ctx)
	if err != nil {
		return err
	}
	if _, exists := schedulerMap.Get(name); exists {
		return errors.New("scheduler already exists: " + name)
	}
	schedulerMap.Set(name, scheduler)
	return nil
}

func GetScheduler(name string) (gocron.Scheduler, bool) {
	scheduler, exists := schedulerMap.Get(name)
	return scheduler, exists
}

func GetAllSchedulers() map[string]gocron.Scheduler {
	return schedulerMap.GetAll()
}

func RemoveScheduler(name string) error {
	if scheduler, exists := schedulerMap.Get(name); exists {
		err := scheduler.Shutdown()
		if err != nil {
			return err
		}
		schedulerMap.Delete(name)
	}
	return nil
}

func RegsiterJob(
	ctx context.Context, schedulername string,
	jobName string, tags []string,
	cron gocron.JobDefinition, runner JobRunner, pararms ...any) (gocron.Job, error) {
	scheduler, exists := GetScheduler(schedulername)
	if !exists {
		return nil, errors.New("scheduler not found: " + schedulername)
	}
	jobCtx, cancel := context.WithCancel(ctx)
	var finnalParams []any
	if len(pararms) == 0 {
		finnalParams = []any{jobCtx}
	} else {
		finnalParams = make([]any, 0, len(pararms)+1)
		finnalParams = append(finnalParams, jobCtx)
		finnalParams = append(finnalParams, pararms...)
	}
	task := gocron.NewTask(func(ctx context.Context, params ...any) error {
		return runner(ctx, params...)
	}, finnalParams...)

	job, err := scheduler.NewJob(cron, task, gocron.WithContext(jobCtx), gocron.WithName(jobName), gocron.WithTags(tags...))
	if err != nil {
		cancel()
		return nil, err
	}
	// 保存取消函数
	schedulerJobsCancel, exists := schedulerJobCancelMap.Get(schedulername)
	if !exists {
		schedulerJobsCancel = NewJobCancelMap()
		schedulerJobsCancel.Set(job.ID(), cancel)
		schedulerJobCancelMap.Set(schedulername, schedulerJobsCancel)
	} else {
		schedulerJobsCancel.Set(job.ID(), cancel)
	}
	return job, nil
}

func StopJobs(schedulerName string, jobUUID ...uuid.UUID) error {
	schedulerJobsCancel, exists := schedulerJobCancelMap.Get(schedulerName)
	if !exists {
		return errors.New("scheduler not found: " + schedulerName)
	}
	for _, jobID := range jobUUID {
		cancelFunc, exists := schedulerJobsCancel.Get(jobID)
		if !exists {
			return errors.New("job not found: " + jobID.String())
		}
		cancelFunc()
	}
	return nil
}

func RemoveJobs(schedulername string, jobUUID ...uuid.UUID) error {
	scheduler, exists := GetScheduler(schedulername)
	if !exists {
		return errors.New("scheduler not found: " + schedulername)
	}
	for _, jobID := range jobUUID {
		err := scheduler.RemoveJob(jobID)
		if err != nil {
			return err
		}
	}
	return nil
}

func RemoveJobByName(schedulername string, jobName string) error {
	scheduler, exists := GetScheduler(schedulername)
	if !exists {
		return errors.New("scheduler not found: " + schedulername)
	}
	jobs := scheduler.Jobs()
	for _, job := range jobs {
		if job.Name() == jobName {
			scheduler.RemoveJob(job.ID())
		}
	}
	return nil
}

// RemoveJobByTags removes all jobs that have at least one of the provided tags.
func RemoveJobByTags(schedulername string, tags ...string) error {
	scheduler, exists := GetScheduler(schedulername)
	if !exists {
		return errors.New("scheduler not found: " + schedulername)
	}
	scheduler.RemoveByTags(tags...)
	return nil
}

func StopAndRemoveJobs(schedulername string, jobUUID ...uuid.UUID) {
	for _, jobID := range jobUUID {
		_ = StopJobs(schedulername, jobID)
		_ = RemoveJobs(schedulername, jobID)
	}
}
