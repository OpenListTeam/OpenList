package bootstrap

import "testing"

func TestCacheMemoryBudgetCapacity(t *testing.T) {
	tests := []struct {
		name      string
		available uint64
		minFree   uint64
		enabled   bool
		want      uint64
	}{
		{name: "enabled", available: 10, minFree: 3, enabled: true, want: 7},
		{name: "disabled", available: 10, minFree: 3, want: 0},
		{name: "reserve equals available", available: 10, minFree: 10, enabled: true, want: 0},
		{name: "reserve exceeds available", available: 10, minFree: 11, enabled: true, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cacheMemoryBudgetCapacity(tt.available, tt.minFree, tt.enabled); got != tt.want {
				t.Fatalf("cacheMemoryBudgetCapacity() = %d, want %d", got, tt.want)
			}
		})
	}
}
