package weiyun_open

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

type jsonInt64 int64
type jsonUint32 uint32
type jsonUint64 uint64

func (n *jsonInt64) UnmarshalJSON(data []byte) error {
	value, err := parseJSONInteger(data)
	if err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse int64 %q: %w", value, err)
	}
	*n = jsonInt64(parsed)
	return nil
}

func (n *jsonUint32) UnmarshalJSON(data []byte) error {
	value, err := parseJSONInteger(data)
	if err != nil {
		return err
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return fmt.Errorf("parse uint32 %q: %w", value, err)
	}
	*n = jsonUint32(parsed)
	return nil
}

func (n *jsonUint64) UnmarshalJSON(data []byte) error {
	value, err := parseJSONInteger(data)
	if err != nil {
		return err
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fmt.Errorf("parse uint64 %q: %w", value, err)
	}
	*n = jsonUint64(parsed)
	return nil
}

func parseJSONInteger(data []byte) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", fmt.Errorf("unexpected empty integer")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return "", fmt.Errorf("unexpected null integer")
	}
	if trimmed[0] != '"' {
		return string(trimmed), nil
	}
	value := ""
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("unexpected empty integer string")
	}
	return value, nil
}
