package cronjob

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SyncArgs 是 sync 任务类型的参数。
// 字段命名保持接近 rclone，便于有 rclone 使用经验的用户理解。
type SyncArgs struct {
	// Src 是源目录（或源文件）的 OpenList 虚拟路径。
	Src string `json:"src"`
	// Dst 是目标目录的 OpenList 虚拟路径。
	Dst string `json:"dst"`
	// MaxDepth 是最大递归深度；0 表示不限制。
	// 1 表示只处理源目录第一层中的文件，不进入子目录。
	MaxDepth int `json:"max_depth"`
	// IgnoreExisting 为 true 时，目标已存在的文件一律跳过，不做覆盖。
	IgnoreExisting bool `json:"ignore_existing"`
	// MaxSize 单位为字节；源文件大小 >= MaxSize 时跳过，0 表示不限制。
	MaxSize int64 `json:"max_size"`
	// MinSize 单位为字节；源文件大小 <= MinSize 时跳过，0 表示不限制。
	MinSize int64 `json:"min_size"`
	// MaxAge 表示只同步“比该时间更年轻”的文件，例如 30d；空表示不限制。
	MaxAge string `json:"max_age"`
	// MinAge 表示只同步“比该时间更老”的文件，例如 7d；空表示不限制。
	MinAge string `json:"min_age"`
	// Exclude 是 glob 过滤规则，匹配相对路径时命中的对象会被跳过。
	Exclude []string `json:"exclude"`
	// ExcludeRegexp 是 Go 标准库 regexp 规则，多行时每行一条。
	ExcludeRegexp []string `json:"exclude_regexp"`
	// ExcludeRegexp2 是 dlclark/regexp2 规则，用于兼容 .NET 风格正则。
	ExcludeRegexp2 []string `json:"exclude_regexp2"`
	// Size 表示使用文件大小判断文件是否变化。
	Size bool `json:"size"`
	// MTime 表示使用修改时间判断文件是否变化；部分网盘时间不可靠，请谨慎启用。
	MTime bool `json:"mtime"`
	// Checksum 表示尽量使用两侧共同支持的哈希判断文件是否变化。
	Checksum bool `json:"checksum"`
}

// validateSyncArgs 校验 JSON 反序列化后的同步参数。
func validateSyncArgs(args SyncArgs) error {
	if args.Src == "" {
		return fmt.Errorf("sync src is required")
	}
	if args.Dst == "" {
		return fmt.Errorf("sync dst is required")
	}
	if args.MaxDepth < 0 {
		return fmt.Errorf("sync max_depth must not be negative")
	}
	if args.MaxSize < 0 || args.MinSize < 0 {
		return fmt.Errorf("sync size limits must not be negative")
	}
	if args.MaxAge != "" {
		if _, err := parseSyncAge(args.MaxAge); err != nil {
			return fmt.Errorf("invalid sync max_age: %w", err)
		}
	}
	if args.MinAge != "" {
		if _, err := parseSyncAge(args.MinAge); err != nil {
			return fmt.Errorf("invalid sync min_age: %w", err)
		}
	}
	return nil
}

// parseSyncAge 解析时间过滤值。
// 支持 Go duration（10m、2h）以及 rclone 常用的 d/w/y 单位；y 按 365 天计算。
func parseSyncAge(value string) (time.Duration, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, fmt.Errorf("age is empty")
	}

	// Go duration 单位已经满足 m/h/ms/us/ns；d/w/y 需要自行换算。
	unit := text[len(text)-1]
	if unit == 'd' || unit == 'w' || unit == 'y' {
		numberText := strings.TrimSuffix(text, string(unit))
		number, err := strconv.ParseFloat(numberText, 64)
		if err != nil || number < 0 {
			return 0, fmt.Errorf("invalid duration %q", text)
		}

		var days float64
		switch unit {
		case 'd':
			days = number
		case 'w':
			days = number * 7
		case 'y':
			days = number * 365
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}

	duration, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", text)
	}
	if duration < 0 {
		return 0, fmt.Errorf("duration must not be negative")
	}
	return duration, nil
}
