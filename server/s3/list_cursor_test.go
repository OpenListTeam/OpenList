package s3

import "testing"

// TestFixNextMarkerEncoding pins that the v1 paging cursor is URL-encoded the
// same way the rest of an encoding-type=url listing is.
//
// The cursor is compared as a plain string, so a "a+b/" cursor that reached the
// client as "a b/" made the next page start before the prefix it had just
// returned: the client looped between two pages for ever and never saw the keys
// sorting after the "+".
func TestFixNextMarkerEncoding(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plus is encoded, not left as a space",
			in:   `<ListBucketResult><NextMarker>q+a/</NextMarker></ListBucketResult>`,
			want: `<ListBucketResult><NextMarker>q%2Ba/</NextMarker></ListBucketResult>`,
		},
		{
			name: "space becomes %20 like the keys do",
			in:   `<NextMarker>q b/</NextMarker>`,
			want: `<NextMarker>q%20b/</NextMarker>`,
		},
		{
			name: "an XML-escaped key is decoded once, then encoded",
			in:   `<NextMarker>a&amp;b/</NextMarker>`,
			want: `<NextMarker>a%26b/</NextMarker>`,
		},
		{
			name: "no cursor leaves the document alone",
			in:   `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`,
			want: `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(fixNextMarkerEncoding([]byte(tc.in))); got != tc.want {
				t.Errorf("fixNextMarkerEncoding()\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
