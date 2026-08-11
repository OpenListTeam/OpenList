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
		{
			name:  "numeric date",
			input: "2026-08-12 03:32:44",
			want:  time.Date(2026, time.August, 12, 3, 32, 44, 0, time.Local),
		},
		{
			name:  "legacy month date",
			input: "Aug 12, 2026 15:32:44 PM",
			want:  time.Date(2026, time.August, 12, 15, 32, 44, 0, time.Local),
		},
		{
			name:  "updated month date",
			input: "Aug 12, 2026, 3:32:44 AM",
			want:  time.Date(2026, time.August, 12, 3, 32, 44, 0, time.Local),
		},
		{
			name:  "narrow no-break space before meridiem",
			input: "Aug 12, 2026, 3:32:44\u202fAM",
			want:  time.Date(2026, time.August, 12, 3, 32, 44, 0, time.Local),
		},
		{
			name:  "no-break space before meridiem",
			input: "Aug 12, 2026, 3:32:44\u00a0AM",
			want:  time.Date(2026, time.August, 12, 3, 32, 44, 0, time.Local),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Time
			if err := got.Unmarshal([]byte(tt.input)); err != nil {
				t.Fatalf("Time.Unmarshal(%q) returned error: %v", tt.input, err)
			}
			gotTime := time.Time(got)
			gotYear, gotMonth, gotDay := gotTime.Date()
			wantYear, wantMonth, wantDay := tt.want.Date()
			gotHour, gotMinute, gotSecond := gotTime.Clock()
			wantHour, wantMinute, wantSecond := tt.want.Clock()
			if gotYear != wantYear || gotMonth != wantMonth || gotDay != wantDay ||
				gotHour != wantHour || gotMinute != wantMinute || gotSecond != wantSecond {
				t.Fatalf("Time.Unmarshal(%q) = %v, want %v", tt.input, gotTime, tt.want)
			}
		})
	}
}

func TestTimeUnmarshalRejectsInvalidTime(t *testing.T) {
	var got Time
	if err := got.Unmarshal([]byte("Aug 12, 2026, 25:32:44 AM")); err == nil {
		t.Fatal("Time.Unmarshal accepted an invalid time")
	}
}
