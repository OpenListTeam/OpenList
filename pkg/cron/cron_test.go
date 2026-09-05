package cron

import (
	"testing"
	"time"
)

// TestCronSpec 测试标准 cron 的解析与下一次触发时间。
func TestCronSpec(t *testing.T) {
	// 每 5 分钟执行一次。
	spec, err := ParseCronSpec("*/5 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	next := spec.NextAfter(time.Date(2026, 9, 5, 1, 2, 3, 0, time.Local))
	if next.Minute() != 5 {
		t.Fatalf("unexpected next time: %s", next)
	}
}

// TestCronSpecDayOfWeekSeven 测试 crontab 中 7 表示周日。
func TestCronSpecDayOfWeekSeven(t *testing.T) {
	// 每周日 03:30 执行。
	spec, err := ParseCronSpec("30 3 * * 7")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-05 是周六，下一次应为 2026-09-06 03:30。
	next := spec.NextAfter(time.Date(2026, 9, 5, 3, 30, 0, 0, time.Local))
	if next.Weekday() != time.Sunday || next.Day() != 6 {
		t.Fatalf("unexpected next time: %s", next)
	}
}
