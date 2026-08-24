package tests

import (
	"errors"
	"fmt"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"
	open115 "github.com/OpenListTeam/OpenList/v4/drivers/115_open"
)

func Test115OpenObjectNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "SDK sentinel", err: sdk.ErrObjectNotFound, want: true},
		{name: "missing folder API code", err: &sdk.Error{Code: 430004, Message: "not found"}, want: true},
		{name: "wrapped missing folder API code", err: fmt.Errorf("get folder: %w", &sdk.Error{Code: 430004}), want: true},
		{name: "other API code", err: &sdk.Error{Code: 401000}, want: false},
		{name: "unrelated error", err: errors.New("boom"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := open115.IsObjectNotFoundError(tt.err); got != tt.want {
				t.Fatalf("IsObjectNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}
