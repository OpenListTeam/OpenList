package s3

import (
	"net/http/httptest"
	"testing"
)

// TestHasResponseOverride pins that a request asking the gateway to rewrite a
// response header stays on the proxy path.
//
// A direct link hands the response to the storage provider, which knows nothing
// about response-content-disposition and friends: the signed request that asked
// for attachment;filename="report.pdf" came back as an inline provider file
// while the very same request against a local storage downloaded report.pdf.
func TestHasResponseOverride(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{"/bucket/obj.pdf?response-content-disposition=attachment%3Bfilename%3D%22report.pdf%22", true},
		{"/bucket/obj.pdf?response-content-type=application%2Fpdf", true},
		{"/bucket/obj.pdf?response-cache-control=max-age%3D60", true},
		{"/bucket/obj.pdf?response-expires=0", true},
		{"/bucket/obj.pdf", false},
		{"/bucket?list-type=2&prefix=a%2F", false},
	}
	for _, tc := range tests {
		r := httptest.NewRequest("GET", tc.target, nil)
		if got := hasResponseOverride(r); got != tc.want {
			t.Errorf("hasResponseOverride(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}
