package _189pc

import (
	"encoding/json"
	"encoding/xml"
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
		{"JSON escaped narrow space", `"Sep 4, 2026, 10:59:33\u202fPM +08"`, time.Date(2026, 9, 4, 22, 59, 33, 0, time.FixedZone("", 8*3600))},
		{"JSON escaped space without timezone", `"Sep 4, 2026, 12:59:33\u00a0AM"`, time.Date(2026, 9, 4, 0, 59, 33, 0, time.FixedZone("", 8*3600))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tm Time
			if err := json.Unmarshal([]byte(tt.input), &tm); err != nil {
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

func TestTimeUnmarshalJSONRejectsInvalid(t *testing.T) {
	for _, input := range []string{`"invalid"`, `123`, `null`, `"Sep 4, 2026, 10:59:33\uZZZZPM +08"`} {
		t.Run(input, func(t *testing.T) {
			var tm Time
			if err := tm.UnmarshalJSON([]byte(input)); err == nil {
				t.Fatalf("UnmarshalJSON(%s) accepted invalid input", input)
			}
		})
	}
}

func TestTimeUnmarshalXML(t *testing.T) {
	var tm Time
	if err := xml.Unmarshal([]byte(`<time>Sep 4, 2026, 10:59:33&#x202f;PM +08</time>`), &tm); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 4, 22, 59, 33, 0, time.FixedZone("", 8*3600))
	if !want.Equal(time.Time(tm)) {
		t.Fatalf("UnmarshalXML = %v, want %v", time.Time(tm), want)
	}
}
