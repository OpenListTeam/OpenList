package onedrive_app

import "testing"

func TestNormalizeSiteID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "already resolved",
			in:   "contoso.sharepoint.com,11111111-2222-3333-4444-555555555555,66666666-7777-8888-9999-aaaaaaaaaaaa",
			want: "contoso.sharepoint.com,11111111-2222-3333-4444-555555555555,66666666-7777-8888-9999-aaaaaaaaaaaa",
		},
		{
			name: "web url",
			in:   "https://contoso.sharepoint.com/sites/team",
			want: "contoso.sharepoint.com:/sites/team:",
		},
		{
			name: "web url with trailing slash",
			in:   "https://contoso.sharepoint.com/sites/team/",
			want: "contoso.sharepoint.com:/sites/team:",
		},
		{
			name: "web url at site collection root",
			in:   "https://contoso.sharepoint.com/",
			want: "contoso.sharepoint.com",
		},
		{
			name: "web url without trailing slash at root",
			in:   "https://contoso.sharepoint.com",
			want: "contoso.sharepoint.com",
		},
		{
			name: "bare hostname",
			in:   "contoso.sharepoint.com",
			want: "contoso.sharepoint.com",
		},
		{
			name: "hostname with trailing slash",
			in:   "contoso.sharepoint.com/",
			want: "contoso.sharepoint.com",
		},
		{
			name: "path form without colon",
			in:   "contoso.sharepoint.com:/sites/team",
			want: "contoso.sharepoint.com:/sites/team:",
		},
		{
			name: "path form with colon and slash",
			in:   "contoso.sharepoint.com:/sites/team/:",
			want: "contoso.sharepoint.com:/sites/team:",
		},
		{
			name: "path form of host root",
			in:   "contoso.sharepoint.com:/",
			want: "contoso.sharepoint.com",
		},
		{
			name: "surrounding whitespace",
			in:   "  contoso.sharepoint.com:/sites/team:  ",
			want: "contoso.sharepoint.com:/sites/team:",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeSiteID(c.in); got != c.want {
				t.Fatalf("NormalizeSiteID(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestGetMetaUrlSharepoint(t *testing.T) {
	d := &OnedriveAPP{
		Addition: Addition{
			Region:       "global",
			IsSharepoint: true,
			SiteID:       "contoso.sharepoint.com,11111111-2222-3333-4444-555555555555,66666666-7777-8888-9999-aaaaaaaaaaaa",
		},
	}
	const wantRoot = "https://graph.microsoft.com/v1.0/sites/contoso.sharepoint.com,11111111-2222-3333-4444-555555555555,66666666-7777-8888-9999-aaaaaaaaaaaa/drive/root"
	if got := d.GetMetaUrl(false, "/"); got != wantRoot {
		t.Fatalf("root url = %q, want %q", got, wantRoot)
	}
	if got, want := d.GetMetaUrl(false, "/a b.txt"), wantRoot+":/a%20b.txt:"; got != want {
		t.Fatalf("file url = %q, want %q", got, want)
	}
}

func TestGetMetaUrlUserDrive(t *testing.T) {
	d := &OnedriveAPP{
		Addition: Addition{
			Region: "global",
			Email:  "user@contoso.com",
		},
	}
	const wantRoot = "https://graph.microsoft.com/v1.0/users/user@contoso.com/drive/root"
	if got := d.GetMetaUrl(false, "/"); got != wantRoot {
		t.Fatalf("root url = %q, want %q", got, wantRoot)
	}
	if got, want := d.GetMetaUrl(false, "/dir"), wantRoot+":/dir:"; got != want {
		t.Fatalf("dir url = %q, want %q", got, want)
	}
}
