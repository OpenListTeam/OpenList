package _115_open

import (
	"encoding/json"
	"errors"
	"strconv"

	sdk "github.com/OpenListTeam/115-sdk-go"
)

func ParseInt64(v json.Number) (int64, error) {
	i, err := v.Int64()
	if err == nil {
		return i, nil
	}
	f, e1 := v.Float64()
	if e1 == nil {
		return int64(f), nil
	}
	return int64(0), err
}

func parseTime(value string) int64 {
	timestamp, _ := strconv.ParseInt(value, 10, 64)
	return timestamp
}

func isObjectNotFound(err error) bool {
	if errors.Is(err, sdk.ErrObjectNotFound) {
		return true
	}
	var sdkErr *sdk.Error
	return errors.As(err, &sdkErr) && sdkErr.Code == 430004
}
