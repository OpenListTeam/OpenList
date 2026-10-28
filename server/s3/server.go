// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"math/rand"
	"net/http"
	"strings"

	"github.com/OpenListTeam/gofakes3"
)

type trailingSlashRequestKey struct{}

func trailingSlashHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut && strings.HasSuffix(r.URL.Path, "/") {
			parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				r = r.WithContext(context.WithValue(r.Context(), trailingSlashRequestKey{}, true))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Make a new S3 Server to serve the remote
func NewServer(ctx context.Context) (h http.Handler, err error) {
	var newLogger logger
	authPairs := authlistResolver()
	faker := gofakes3.New(
		newBackend(),
		// gofakes3.WithHostBucket(!opt.pathBucketMode),
		gofakes3.WithLogger(newLogger),
		gofakes3.WithRequestID(rand.Uint64()),
		gofakes3.WithoutVersioning(),
		gofakes3.WithV4Auth(authPairs),
		gofakes3.WithIntegrityCheck(true), // Check Content-MD5 if supplied
	)

	return redirectHandler(trailingSlashHandler(rangeStatusHandler(faker.Server())), authPairs), nil
}
