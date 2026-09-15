package cronjob

import (
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
)

// ParseSpecs 解析一组 cron 表达式；任一条无效都会返回错误并指出序号。
// API 校验和调度器共用，保证管理员更新后调度器无需重启即可识别。
func ParseSpecs(specs []string) ([]cron.CronSpec, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("cron specs must not be empty")
	}
	parsed := make([]cron.CronSpec, 0, len(specs))
	for i, spec := range specs {
		item, err := cron.ParseCronSpec(spec)
		if err != nil {
			return nil, fmt.Errorf("cron spec #%d (%q): %w", i+1, spec, err)
		}
		parsed = append(parsed, item)
	}
	return parsed, nil
}

// EarliestNext 返回一组 cron 表达式中最早的下一次触发时间。
// 任务在任何一条表达式命中时执行，因此下一次时间取所有表达式的最小值。
func EarliestNext(specs []string, after time.Time) (time.Time, error) {
	parsed, err := ParseSpecs(specs)
	if err != nil {
		return time.Time{}, err
	}
	var earliest time.Time
	for _, item := range parsed {
		next := item.NextAfter(after)
		if next.IsZero() {
			return time.Time{}, fmt.Errorf("cannot calculate next run time")
		}
		if earliest.IsZero() || next.Before(earliest) {
			earliest = next
		}
	}
	return earliest, nil
}
