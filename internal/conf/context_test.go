package conf_test

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func TestGetApiUrl(t *testing.T) {
	const want = "https://openlist.example"
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "present", ctx: context.WithValue(context.Background(), conf.ApiUrlKey, want), want: want},
		{name: "absent", ctx: context.Background()},
		{name: "wrong type", ctx: context.WithValue(context.Background(), conf.ApiUrlKey, 1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := conf.GetApiUrl(tt.ctx); got != tt.want {
				t.Fatalf("origin = %q, want %q", got, tt.want)
			}
		})
	}
}
