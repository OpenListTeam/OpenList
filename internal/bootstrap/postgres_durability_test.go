package bootstrap

import "testing"

func TestPostgresDurabilityDSN(t *testing.T) {
	cases := []struct {
		name        string
		dsn         string
		want        string
		wantApplied bool
	}{
		{
			name:        "keyword form gains a quoted option",
			wantApplied: true,
			dsn:         "host=db user=openlist dbname=openlist port=5432 sslmode=disable TimeZone=Asia/Shanghai",
			want:        "host=db user=openlist dbname=openlist port=5432 sslmode=disable TimeZone=Asia/Shanghai options='-c synchronous_commit=off'",
		},
		{
			name:        "url form gains an encoded query parameter",
			wantApplied: true,
			dsn:         "postgres://openlist@db:5432/openlist?sslmode=disable",
			want:        "postgres://openlist@db:5432/openlist?options=-c+synchronous_commit%3Doff&sslmode=disable",
		},
		{
			name:        "url form without query",
			wantApplied: true,
			dsn:         "postgresql://db/openlist",
			want:        "postgresql://db/openlist?options=-c+synchronous_commit%3Doff",
		},
		{
			name: "an explicit level is left alone",
			dsn:  "host=db user=openlist dbname=openlist options='-c synchronous_commit=on'",
			want: "host=db user=openlist dbname=openlist options='-c synchronous_commit=on'",
		},
		{
			name: "another option list is not clobbered",
			dsn:  "host=db user=openlist dbname=openlist options='-c work_mem=64MB'",
			want: "host=db user=openlist dbname=openlist options='-c work_mem=64MB'",
		},
		{
			name: "url with another option list is not clobbered",
			dsn:  "postgres://db/openlist?options=-c%20work_mem%3D64MB",
			want: "postgres://db/openlist?options=-c%20work_mem%3D64MB",
		},
		{
			// libpq accepts blanks around the equals sign; a substring scan
			// would miss this and append a second options keyword whose
			// last-wins parsing silently drops the operator's list.
			name: "spaced options keyword is not clobbered",
			dsn:  "host=db user=openlist options = '-c work_mem=64MB'",
			want: "host=db user=openlist options = '-c work_mem=64MB'",
		},
		{
			// A fragment must not swallow the appended parameter: appending
			// raw text would turn the parameter into part of the fragment and
			// silently leave synchronous_commit on while logging success.
			name:        "url fragment is preserved and still gains the parameter",
			dsn:         "postgres://db/openlist#frag",
			want:        "postgres://db/openlist?options=-c+synchronous_commit%3Doff#frag",
			wantApplied: true,
		},
		{
			// A value that merely contains the substring is skipped: only a
			// performance loss, never a semantic change.
			// A value that merely contains the substring is *not* an options
			// keyword: the token scan proves it, so the rewrite is safe and
			// applies - the old substring check needlessly skipped these.
			name:        "value containing the substring is still accelerated",
			dsn:         "host=db password=xxoptions=yy",
			want:        "host=db password=xxoptions=yy options='-c synchronous_commit=off'",
			wantApplied: true,
		},
		{
			name:        "url with a value containing the substring is still accelerated",
			dsn:         "postgres://db/openlist?password=xxoptions%3Dyy",
			want:        "postgres://db/openlist?options=-c+synchronous_commit%3Doff&password=xxoptions%3Dyy",
			wantApplied: true,
		},
	}
	for _, c := range cases {
		got, applied := postgresDurabilityDSN(c.dsn)
		if got != c.want || applied != c.wantApplied {
			t.Errorf("%s: got %q applied=%v, want %q applied=%v", c.name, got, applied, c.want, c.wantApplied)
		}
	}
}
