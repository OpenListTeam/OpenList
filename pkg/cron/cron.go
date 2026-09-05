// Package cron 同时提供两类定时能力：
//  1. 传统的固定间隔 Cron（drivers 在 token 刷新等场景已经大量使用，必须保持兼容）；
//  2. 标准 5 字段 CronSpec 与 CronScheduler（供持久化计划任务调度使用）。
package cron

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cron 是传统的固定间隔定时器，保留旧接口以兼容所有 drivers。
type Cron struct {
	d  time.Duration
	ch chan struct{}
}

// NewCron 创建一个每隔固定时间触发一次的传统定时器。
func NewCron(d time.Duration) *Cron {
	return &Cron{
		d:  d,
		ch: make(chan struct{}),
	}
}

// Do 启动传统定时器；回调会在每个 interval 到达时执行一次。
func (c *Cron) Do(f func()) {
	go func() {
		ticker := time.NewTicker(c.d)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				f()
			case <-c.ch:
				return
			}
		}
	}()
}

// Stop 停止传统定时器；重复调用是安全的。
func (c *Cron) Stop() {
	select {
	case _, _ = <-c.ch:
	default:
		c.ch <- struct{}{}
		close(c.ch)
	}
}

// CronSpec 描述一条 5 字段标准 cron 规则。
// 字段顺序与 Linux crontab 一致：minute hour day-of-month month day-of-week。
type CronSpec struct {
	// 分钟字段，取值 0-59。
	minute IntRange
	// 小时字段，取值 0-23。
	hour IntRange
	// 月内日期字段，取值 1-31。
	dayOfMonth IntRange
	// 月份字段，取值 1-12。
	month IntRange
	// 周字段，取值 0-6；解析时会把 7 转换为周日 0。
	dayOfWeek IntRange
}

// IntRange 表示一个 cron 字段解析后命中的整数集合。
type IntRange struct {
	// min 是字段的最小值。
	min int
	// max 是字段的最大值。
	max int
	// values 是该字段命中的所有取值。
	values map[int]struct{}
}

// Contains 判断某个整数是否命中当前字段。
func (r IntRange) Contains(v int) bool {
	if v < r.min || v > r.max {
		return false
	}
	_, ok := r.values[v]
	return ok
}

// ParseCronSpec 解析标准 5 字段 cron 表达式。
// 支持 "*"、单个数字、范围（1-5）、列表（1,2,3）和步进（*/5、1-10/2）。
// 当前不支持 ?、L、W、@daily 等扩展语法，前端可以明确提示用户使用标准语法。
func ParseCronSpec(spec string) (CronSpec, error) {
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return CronSpec{}, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}

	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return CronSpec{}, fmt.Errorf("invalid minute field: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return CronSpec{}, fmt.Errorf("invalid hour field: %w", err)
	}
	dayOfMonth, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return CronSpec{}, fmt.Errorf("invalid day-of-month field: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return CronSpec{}, fmt.Errorf("invalid month field: %w", err)
	}
	dayOfWeek, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return CronSpec{}, fmt.Errorf("invalid day-of-week field: %w", err)
	}

	// crontab 规范里 7 也可表示周日，这里统一转换为 0。
	if dayOfWeek.Contains(7) {
		dayOfWeek.values[0] = struct{}{}
	}

	return CronSpec{
		minute:     minute,
		hour:       hour,
		dayOfMonth: dayOfMonth,
		month:      month,
		dayOfWeek:  dayOfWeek,
	}, nil
}

// parseCronField 解析单个 cron 字段并生成命中集合。
func parseCronField(field string, min, max int) (IntRange, error) {
	result := IntRange{
		min:    min,
		max:    max,
		values: make(map[int]struct{}),
	}

	// 逗号分隔的每一项都独立解析后合并。
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return result, fmt.Errorf("empty field part")
		}

		// step 是 "a-b/step" 或 "*/step" 中的 step。
		var expr, stepText string
		if idx := strings.Index(part, "/"); idx >= 0 {
			expr = part[:idx]
			stepText = part[idx+1:]
		} else {
			expr = part
		}

		low, high, err := parseCronBounds(expr, min, max)
		if err != nil {
			return result, err
		}

		// 没有显式步进时按 1 遍历所有值。
		step := 1
		if stepText != "" {
			step, err = strconv.Atoi(stepText)
			if err != nil || step <= 0 {
				return result, fmt.Errorf("invalid step %q", stepText)
			}
		}
		for value := low; value <= high; value += step {
			result.values[value] = struct{}{}
		}
	}

	if len(result.values) == 0 {
		return result, fmt.Errorf("field matches no values")
	}
	return result, nil
}

// parseCronBounds 解析字段中不含 step 的部分，返回闭区间边界。
func parseCronBounds(expr string, min, max int) (int, int, error) {
	// "*" 和 "?" 都表示整个字段范围；容忍 "?" 可以减少前端配置出错。
	if expr == "*" || expr == "?" {
		return min, max, nil
	}

	if idx := strings.Index(expr, "-"); idx >= 0 {
		low, err := strconv.Atoi(expr[:idx])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid lower bound %q", expr[:idx])
		}
		high, err := strconv.Atoi(expr[idx+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid upper bound %q", expr[idx+1:])
		}
		if low < min || high > max || low > high {
			return 0, 0, fmt.Errorf("range %d-%d outside [%d,%d]", low, high, min, max)
		}
		return low, high, nil
	}

	value, err := strconv.Atoi(expr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid value %q", expr)
	}
	if value < min || value > max {
		return 0, 0, fmt.Errorf("value %d outside [%d,%d]", value, min, max)
	}
	// 单个值等价于只包含该值的范围。
	return value, value, nil
}

// String 输出规范化的 cron 表达式，便于日志排查。
func (c CronSpec) String() string {
	return fmt.Sprintf("%s %s %s %s %s",
		formatCronField(c.minute), formatCronField(c.hour), formatCronField(c.dayOfMonth),
		formatCronField(c.month), formatCronField(c.dayOfWeek))
}

// formatCronField 把整数集合排序后还原为逗号分隔字符串。
func formatCronField(field IntRange) string {
	values := make([]int, 0, len(field.values))
	for value := range field.values {
		values = append(values, value)
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

// matches 判断时间点是否命中整条 cron 规则。
func (c CronSpec) matches(t time.Time) bool {
	// crontab 的标准行为是：DOM 和 DOW 同时受限时，任一命中即可。
	dayMatches := c.dayOfMonth.Contains(t.Day()) || c.dayOfWeek.Contains(int(t.Weekday()))
	return c.minute.Contains(t.Minute()) &&
		c.hour.Contains(t.Hour()) &&
		c.month.Contains(int(t.Month())) &&
		dayMatches
}

// NextAfter 返回严格晚于 after 的下一次触发时间。
// 为避免日历字段遗漏，这里按分钟推进一次检查；上层也会保存 next_run_at。
func (c CronSpec) NextAfter(after time.Time) time.Time {
	// 去掉秒和纳秒，cron 只精确到分钟。
	next := after.Truncate(time.Minute)
	// 最多向后搜索一年（366 天 * 1440 分钟），避免畸形规则导致死循环。
	for i := 0; i < 527040; i++ {
		next = next.Add(time.Minute)
		if c.matches(next) {
			return next
		}
	}
	// 正常字段不会到这里；返回零时间便于上层发现表达式错误或极端规则。
	return time.Time{}
}

// CronScheduler 是一个固定周期唤醒的通用调度器。
// 它不自己决定哪些任务到期，而是每次 tick 回调检查函数，便于持久化任务模型复用。
type CronScheduler struct {
	// ctx 用于停止后台 goroutine。
	ctx context.Context
	// cancel 对应 ctx 的取消函数。
	cancel context.CancelFunc
	// done 在 goroutine 完全退出后关闭，供 Stop 等待。
	done chan struct{}
	// interval 是检查任务是否到期的周期。
	interval time.Duration
	// onTick 是到期检查回调。
	onTick func(context.Context)
}

// NewCronScheduler 创建调度器；周期必须大于 0，否则会 panic。
func NewCronScheduler(interval time.Duration, onTick func(context.Context)) *CronScheduler {
	if interval <= 0 {
		panic("cron scheduler interval must be positive")
	}
	if onTick == nil {
		panic("cron scheduler tick function must not be nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &CronScheduler{
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		interval: interval,
		onTick:   onTick,
	}
}

// Start 启动后台调度循环。
func (s *CronScheduler) Start() {
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.onTick(s.ctx)
			}
		}
	}()
}

// Stop 停止调度循环；当前实现只保证不再产生新的 tick，由业务层自行等待运行中任务。
func (s *CronScheduler) Stop() {
	s.cancel()
}
