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

// TestCronSpecMonthly 测试每月某日执行：30 1 3 * * 只命中每月 3 日。
func TestCronSpecMonthly(t *testing.T) {
	spec, err := ParseCronSpec("30 1 3 * *")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-15 之后下一次是 2026-10-03 01:30。
	next := spec.NextAfter(time.Date(2026, 9, 15, 0, 0, 0, 0, time.Local))
	if next.Month() != time.October || next.Day() != 3 || next.Hour() != 1 || next.Minute() != 30 {
		t.Fatalf("unexpected next time: %s", next)
	}
}

// TestCronSpecWeekly 测试每周某天执行：30 2 * * 4 只命中周四。
func TestCronSpecWeekly(t *testing.T) {
	spec, err := ParseCronSpec("30 2 * * 4")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-15 是周二，下一次应是 2026-09-17（周四）02:30。
	next := spec.NextAfter(time.Date(2026, 9, 15, 0, 0, 0, 0, time.Local))
	if next.Weekday() != time.Thursday || next.Hour() != 2 || next.Minute() != 30 {
		t.Fatalf("unexpected next time: %s", next)
	}
}

// TestCronSpecDomDowOr 测试 DOM 与 DOW 同时受限时任一命中即可。
func TestCronSpecDomDowOr(t *testing.T) {
	spec, err := ParseCronSpec("0 0 13 * 5")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-13 既是 13 日也是周五，应命中当天。
	next := spec.NextAfter(time.Date(2026, 11, 12, 0, 0, 0, 0, time.Local))
	if next.Month() != time.November || next.Day() != 13 {
		t.Fatalf("unexpected next time: %s", next)
	}
	// 2026-11-14（周六）之后：13 日已过，下一个命中是 2026-11-20 周五。
	next = spec.NextAfter(time.Date(2026, 11, 14, 0, 0, 0, 0, time.Local))
	if next.Month() != time.November || next.Day() != 20 {
		t.Fatalf("unexpected next time: %s", next)
	}
}
