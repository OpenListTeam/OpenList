package scheduler

import (
	"context"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
)

type jobBuilder struct {
	id                                  uuid.UUID
	ctx                                 context.Context
	jobName                             string
	cron                                gocron.JobDefinition
	disabled                            bool
	labels                              JobLabels
	runner                              JobRunner
	params                              []any
	afterJobRuns                        []func(jobID uuid.UUID, jobName string)
	afterJobRunsWithErrors              []func(jobID uuid.UUID, jobName string, runErr error)
	afterJobRunsWithPanics              []func(jobID uuid.UUID, jobName string, panicData any)
	beforeJobRuns                       []func(jobID uuid.UUID, jobName string)
	beforeJobRunsSkipIfBeforeFuncErrors []func(jobID uuid.UUID, jobName string) error
}

// NewJobBuilder create a jobBuilder
func NewJobBuilder() *jobBuilder {
	return &jobBuilder{
		disabled: false,
		labels:   make(JobLabels),
	}
}

// ID sets the job ID if needed.
func (jb *jobBuilder) ID(id uuid.UUID) *jobBuilder {
	jb.id = id
	return jb
}

// Ctx sets the job context.
func (jb *jobBuilder) Ctx(ctx context.Context) *jobBuilder {
	jb.ctx = ctx
	return jb
}

// Name sets the job name.
func (jb *jobBuilder) Name(name string) *jobBuilder {
	jb.jobName = name
	return jb
}

// _internalCron sets the job cron definition.
// This is an internal method; prefer using the By... methods.
func (jb *jobBuilder) _internalCron(cron gocron.JobDefinition) *jobBuilder {
	jb.cron = cron
	return jb
}

// ByCrontab defines a new job using the crontab syntax: `* * * * *`.
// An optional 6th field can be used at the beginning if withSeconds
// is set to true: `* * * * * *`.
// The timezone can be set on the Scheduler using WithLocation, or in the
// crontab in the form `TZ=America/Chicago * * * * *` or
// `CRON_TZ=America/Chicago * * * * *`
func (jb *jobBuilder) ByCrontab(crontab string, withSeconds bool) *jobBuilder {
	return jb._internalCron(gocron.CronJob(crontab, withSeconds))
}

// ByDuration defines a new job using time.Duration
// for the interval.
func (jb *jobBuilder) ByDuration(d time.Duration) *jobBuilder {
	return jb._internalCron(gocron.DurationJob(d))
}

// ByDurationRandomJob defines a new job that runs on a random interval
// between the min and max duration values provided.
//
// To achieve a similar behavior as tools that use a splay/jitter technique
// consider the median value as the baseline and the difference between the
// max-median or median-min as the splay/jitter.
//
// For example, if you want a job to run every 5 minutes, but want to add
// up to 1 min of jitter to the interval, you could use
// ByDurationRandomJob(4*time.Minute, 6*time.Minute)
func (jb *jobBuilder) ByDurationRandomJob(min, max time.Duration) *jobBuilder {
	return jb._internalCron(gocron.DurationRandomJob(min, max))
}

// ByDaily defines a new job that runs daily at the specified time.
func (jb *jobBuilder) ByDaily(interval uint, atTimes AtTimes) *jobBuilder {
	return jb._internalCron(gocron.DailyJob(interval, newAtTimes(atTimes)))
}

// ByWeekly defines a new job that runs weekly at the specified time.
func (jb *jobBuilder) ByWeekly(interval uint, weekdays []time.Weekday, atTimes AtTimes) *jobBuilder {
	return jb._internalCron(gocron.WeeklyJob(interval, newWeekdays(weekdays), newAtTimes(atTimes)))
}

// ByMonthly runs the job on the interval of months, on the specific days of the month
// specified, and at the set times. Days of the month can be 1 to 31 or negative (-1 to -31), which
// count backwards from the end of the month. E.g. -1 is the last day of the month.
//
// If a day of the month is selected that does not exist in all months (e.g. 31st)
// any month that does not have that day will be skipped.
//
// By default, the job will start the next available day, considering the last run to be now,
// and the time and month based on the interval, days and times you input.
// This means, if you select an interval greater than 1, your job by default will run
// X (interval) months from now if there are no daysOfTheMonth left in the current month.
// You can use WithStartAt to tell the scheduler to start the job sooner.
//
// Carefully consider your configuration!
//   - For example: an interval of 2 months on the 31st of each month, starting 12/31
//     would skip Feb, April, June, and next run would be in August.
func (jb *jobBuilder) ByMonthly(interval uint, daysOfTheMonth []int, atTimes AtTimes) *jobBuilder {
	return jb._internalCron(gocron.MonthlyJob(
		interval,
		newDaysOfTheMonth(daysOfTheMonth),
		newAtTimes(atTimes)))
}

// ByOneTimeJobStartImmediately tells the scheduler to run the one time job immediately.
func (jb *jobBuilder) ByOneTimeJobStartImmediately() *jobBuilder {
	return jb._internalCron(gocron.OneTimeJob(gocron.OneTimeJobStartImmediately()))
}

// ByOneTimeJobStartDateTime sets the date & time at which the job should run.
// This datetime must be in the future (according to the scheduler clock).
func (jb *jobBuilder) ByOneTimeJobStartDateTime(start time.Time) *jobBuilder {
	return jb._internalCron(gocron.OneTimeJob(gocron.OneTimeJobStartDateTime(start)))
}

// ByOneTimeJobStartDateTimes sets the date & times at which the job should run.
// At least one of the date/times must be in the future (according to the scheduler clock).
func (jb *jobBuilder) ByOneTimeJobStartDateTimes(times ...time.Time) *jobBuilder {
	return jb._internalCron(gocron.OneTimeJob(gocron.OneTimeJobStartDateTimes(times...)))
}

// Disabled sets the job disabled status.
func (jb *jobBuilder) Disabled(disabled bool) *jobBuilder {
	jb.disabled = disabled
	return jb
}

// Label add or replaces a label key/value pair.
func (jb *jobBuilder) Label(key, value string) *jobBuilder {
	jb.labels[key] = value
	return jb
}

// Labels batch adds or replaces multiple label key/value pairs.
func (jb *jobBuilder) Labels(labels JobLabels) *jobBuilder {
	if len(labels) == 0 {
		return jb
	}
	for k, v := range labels {
		jb.labels[k] = v
	}
	return jb
}

// Runner sets the job runner function and the params.
func (jb *jobBuilder) Runner(runner JobRunner, params ...any) *jobBuilder {
	jb.runner = runner
	jb.params = params
	return jb
}

// AfterJobRuns sets functions to be called after the job runs.
func (jb *jobBuilder) AfterJobRuns(eventListenerFunc func(jobID uuid.UUID, jobName string)) *jobBuilder {
	jb.afterJobRuns = append(jb.afterJobRuns, eventListenerFunc)
	return jb
}

// AfterJobRunsWithError is used to listen for when a job has run and returned an error, and then run the provided function.
func (jb *jobBuilder) AfterJobRunsWithError(eventListenerFunc func(jobID uuid.UUID, jobName string, runErr error)) *jobBuilder {
	jb.afterJobRunsWithErrors = append(jb.afterJobRunsWithErrors, eventListenerFunc)
	return jb
}

// AfterJobRunsWithPanic is used to listen for when a job has run and returned panicked recover data, and then run the provided function.
func (jb *jobBuilder) AfterJobRunsWithPanic(eventListenerFunc func(jobID uuid.UUID, jobName string, panicData any)) *jobBuilder {
	jb.afterJobRunsWithPanics = append(jb.afterJobRunsWithPanics, eventListenerFunc)
	return jb
}

// BeforeJobRuns sets functions to be called before the job runs.
func (jb *jobBuilder) BeforeJobRuns(eventListenerFunc func(jobID uuid.UUID, jobName string)) *jobBuilder {
	jb.beforeJobRuns = append(jb.beforeJobRuns, eventListenerFunc)
	return jb
}

// BeforeJobRunsSkipIfBeforeFuncErrors sets functions to be called before the job runs.
// If any of these functions return an error, the job run will be skipped.
func (jb *jobBuilder) BeforeJobRunsSkipIfBeforeFuncErrors(eventListenerFunc func(jobID uuid.UUID, jobName string) error) *jobBuilder {
	jb.beforeJobRunsSkipIfBeforeFuncErrors = append(jb.beforeJobRunsSkipIfBeforeFuncErrors, eventListenerFunc)
	return jb
}

func (jb *jobBuilder) _internalGetOrCreateID() uuid.UUID {
	if jb.id == uuid.Nil {
		jb.id = uuid.New()
	}
	return jb.id
}

func (jb *jobBuilder) _internalGetOptions() []gocron.JobOption {
	tags := jobLabels2Tags(jb.labels)
	opts := []gocron.JobOption{}
	if jb.id != uuid.Nil {
		opts = append(opts, gocron.WithIdentifier(jb.id))
	}
	if jb.ctx != nil {
		opts = append(opts, gocron.WithContext(jb.ctx))
	}
	if jb.jobName != "" {
		opts = append(opts, gocron.WithName(jb.jobName))
	}
	if len(tags) > 0 {
		opts = append(opts, gocron.WithTags(tags...))
	}
	if jb.afterJobRuns != nil {
		opts = append(opts, gocron.WithEventListeners(
			gocron.AfterJobRuns(
				func(jobID uuid.UUID, jobName string) {
					for _, e := range jb.afterJobRuns {
						e(jobID, jobName)
					}
				}),
		))
	}
	if jb.afterJobRunsWithErrors != nil {
		opts = append(opts, gocron.WithEventListeners(
			gocron.AfterJobRunsWithError(
				func(jobID uuid.UUID, jobName string, runErr error) {
					for _, e := range jb.afterJobRunsWithErrors {
						e(jobID, jobName, runErr)
					}
				}),
		))
	}
	if jb.afterJobRunsWithPanics != nil {
		opts = append(opts, gocron.WithEventListeners(
			gocron.AfterJobRunsWithPanic(
				func(jobID uuid.UUID, jobName string, panicData any) {
					for _, e := range jb.afterJobRunsWithPanics {
						e(jobID, jobName, panicData)
					}
				}),
		))
	}
	if jb.beforeJobRuns != nil {
		opts = append(opts, gocron.WithEventListeners(
			gocron.BeforeJobRuns(
				func(jobID uuid.UUID, jobName string) {
					for _, e := range jb.beforeJobRuns {
						e(jobID, jobName)
					}
				}),
		))
	}
	if jb.beforeJobRunsSkipIfBeforeFuncErrors != nil {
		opts = append(opts, gocron.WithEventListeners(
			gocron.BeforeJobRunsSkipIfBeforeFuncErrors(
				func(jobID uuid.UUID, jobName string) error {
					for _, e := range jb.beforeJobRunsSkipIfBeforeFuncErrors {
						if err := e(jobID, jobName); err != nil {
							return err
						}
					}
					return nil
				}),
		))
	}
	return opts
}

func newAtTimes(atTimes []AtTime) gocron.AtTimes {
	if len(atTimes) == 0 {
		return nil
	}
	if len(atTimes) == 1 {
		at := gocron.NewAtTime(atTimes[0].hours, atTimes[0].minutes, atTimes[0].seconds)
		return gocron.NewAtTimes(at)
	}
	var gocronAtTimes []gocron.AtTime
	for _, at := range atTimes[1:] {
		gocronAtTimes = append(gocronAtTimes, gocron.NewAtTime(at.hours, at.minutes, at.seconds))
	}
	return gocron.NewAtTimes(
		gocron.NewAtTime(atTimes[0].hours, atTimes[0].minutes, atTimes[0].seconds),
		gocronAtTimes...,
	)
}
func newWeekdays(weekdays []time.Weekday) gocron.Weekdays {
	if len(weekdays) == 0 {
		return nil
	}
	if len(weekdays) == 1 {
		return gocron.NewWeekdays(weekdays[0])
	}
	return gocron.NewWeekdays(weekdays[0], weekdays[1:]...)
}

func newDaysOfTheMonth(days []int) gocron.DaysOfTheMonth {
	if len(days) == 0 {
		return nil
	}
	if len(days) == 1 {
		return gocron.NewDaysOfTheMonth(days[0])
	}
	return gocron.NewDaysOfTheMonth(days[0], days[1:]...)
}
