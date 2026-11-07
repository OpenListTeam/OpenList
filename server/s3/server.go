// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"math/rand"
	"net/http"

	"github.com/OpenListTeam/gofakes3"
)

// Make a new S3 Server to serve the remote
func NewServer(ctx context.Context) (h http.Handler, err error) {
	var newLogger logger
	// The ring is seeded from the settings once and then kept in sync while the
	// process runs, so adding or rotating an access key in the admin UI does not
	// require a restart.
	keys := newKeyRing(resolveAuthPairs())
	faker := gofakes3.New(
		newBackend(),
		// gofakes3.WithHostBucket(!opt.pathBucketMode),
		gofakes3.WithLogger(newLogger),
		gofakes3.WithRequestID(rand.Uint64()),
		gofakes3.WithoutVersioning(),
		gofakes3.WithV4Auth(keys.snapshot()),
		gofakes3.WithIntegrityCheck(true), // Check Content-MD5 if supplied
	)

	// A second, deliberately keyless instance serves anonymous reads of buckets
	// flagged public. gofakes3 accepts every request when it has no keys, so the
	// only way to allow anonymous reads without opening writes is to give them
	// their own instance and gate it in front.
	publicFaker := gofakes3.New(
		newBackend(),
		gofakes3.WithLogger(newLogger),
		gofakes3.WithRequestID(rand.Uint64()),
		gofakes3.WithoutVersioning(),
		gofakes3.WithIntegrityCheck(true), // Check Content-MD5 if supplied
	)

	keys.bind(faker)
	go keys.watch(ctx)

	return redirectHandler(bucketAuthHandler(
		rangeStatusHandler(faker.Server()),
		publicBucketGuard(rangeStatusHandler(publicFaker.Server())),
	), keys), nil
}
