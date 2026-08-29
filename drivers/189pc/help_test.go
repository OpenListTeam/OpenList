package _189pc

import (
	"testing"
	"time"
)

func TestTimeUnmarshal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{"numeric date", `"2026-08-11 10:37:18"`, time.Date(2026, 8, 11, 10, 37, 18, 0, time.FixedZone("", 8*3600))},
		{"legacy month date", `"Aug 11, 2026 10:37:18 PM"`, time.Date(2026, 8, 11, 22, 37, 18, 0, time.FixedZone("", 8*3600))},
		{"new format with tz", `"Aug 11, 2026, 10:37:18 PM +08"`, time.Date(2026, 8, 11, 22, 37, 18, 0, time.FixedZone("", 8*3600))},
		{"new format no tz", `"Aug 11, 2026, 10:37:18 PM"`, time.Date(2026, 8, 11, 22, 37, 18, 0, time.FixedZone("", 8*3600))},
		{"narrow no-break space (U+202F)", "\"Aug 12, 2026, 12:35:41\u202fAM +08\"", time.Date(2026, 8, 12, 0, 35, 41, 0, time.FixedZone("", 8*3600))},
		{"no-break space (U+00A0)", "\"Aug 12, 2026, 12:35:41\u00a0AM +08\"", time.Date(2026, 8, 12, 0, 35, 41, 0, time.FixedZone("", 8*3600))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tm Time
			if err := tm.Unmarshal([]byte(tt.input)); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", tt.input, err)
			}
			if !tt.want.Equal(time.Time(tm)) {
				t.Fatalf("Unmarshal(%s) = %v, want %v", tt.input, time.Time(tm), tt.want)
			}
		})
	}
}

func TestTimeUnmarshalRejectsInvalid(t *testing.T) {
	var tm Time
	if err := tm.Unmarshal([]byte("Aug 12, 2026, 25:32:44 AM")); err == nil {
		t.Fatal("Unmarshal accepted an invalid time")
	}
}
