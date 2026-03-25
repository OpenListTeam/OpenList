package plugin

import (
	"fmt"
	"strconv"
)

func IntValue(config map[string]any, key string, fallback int) (int, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case int:
		return v, nil
	case int8:
		return int(v), nil
	case int16:
		return int(v), nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case uint:
		return int(v), nil
	case uint8:
		return int(v), nil
	case uint16:
		return int(v), nil
	case uint32:
		return int(v), nil
	case uint64:
		return int(v), nil
	case float32:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("parse %s as int: %w", key, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("parse %s as int: unsupported type %T", key, value)
	}
}

func BoolValue(config map[string]any, key string, fallback bool) (bool, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("parse %s as bool: %w", key, err)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("parse %s as bool: unsupported type %T", key, value)
	}
}

func StringValue(config map[string]any, key, fallback string) (string, error) {
	value, ok := config[key]
	if !ok || value == nil {
		return fallback, nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	default:
		return "", fmt.Errorf("parse %s as string: unsupported type %T", key, value)
	}
}
