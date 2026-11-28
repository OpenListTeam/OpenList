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

// TestCronSpecFullRangeDomIsRestricted 测试 "1-31" 这类覆盖全部取值的 DOM
// 仍然是受限字段，必须与受限的 DOW 做 OR 组合。
// 若按“展开后是否覆盖全部取值”判断通配，"0 0 1-31 * 1" 会被错误地限制为周一。
func TestCronSpecFullRangeDomIsRestricted(t *testing.T) {
	spec, err := ParseCronSpec("0 0 1-31 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-10 是周二，DOM 受限命中全部日期，应当在下一天零点触发。
	next := spec.NextAfter(time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local))
	if next.Month() != time.November || next.Day() != 11 {
		t.Fatalf("expected daily run for full-range DOM with restricted DOW, got %s", next)
	}
}

// TestCronSpecStepWildcardIsWildcard 测试 "*/2" 这类以 "*" 开头的步进写法仍是通配语法。
// DOM 通配时按 AND 组合：既要满足 DOM 的奇数日，也要满足 DOW 的周一。
func TestCronSpecStepWildcardIsWildcard(t *testing.T) {
	spec, err := ParseCronSpec("0 0 */2 * 1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-10（周二）之后满足“奇数日且为周一”的下一次是 2026-11-23。
	next := spec.NextAfter(time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local))
	if next.Month() != time.November || next.Day() != 23 || next.Weekday() != time.Monday {
		t.Fatalf("expected next odd-day Monday for step wildcard DOM, got %s", next)
	}
}

// TestCronSpecLeapDay 测试闰日规则跨越非闰年仍能计算出下一次触发时间。
// "0 0 29 2 *" 在平年不会命中，上一次与下一次之间可能相隔数年。
func TestCronSpecLeapDay(t *testing.T) {
	spec, err := ParseCronSpec("0 0 29 2 *")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-01 之后下一个闰日是 2028-02-29。
	next := spec.NextAfter(time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local))
	if next.IsZero() {
		t.Fatal("leap day schedule should have a next run time")
	}
	if next.Year() != 2028 || next.Month() != time.February || next.Day() != 29 {
		t.Fatalf("unexpected next leap day: %s", next)
	}
}
