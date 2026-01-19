package scheduler

import "errors"

var (
	ErrJobCronNotDefined = errors.New("job cron not defined")
	ErrJobTaskNotDefined = errors.New("job task not defined")
)
