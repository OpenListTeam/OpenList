package s3

import "net/http"

func hasPreconditions(r *http.Request) bool {
	for _, name := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
		if _, ok := r.Header[name]; ok {
			return true
		}
	}
	return false
}
