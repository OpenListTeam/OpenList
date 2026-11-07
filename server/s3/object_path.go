package s3

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
)

// Object keys are opaque strings in S3, but this gateway resolves them against
// a virtual filesystem, where "." and ".." are real path segments. Without a
// guard, "/bucket/../other/key" is authorised as bucket "bucket" and then read
// from whatever "/other/key" points at, so a key scoped to one bucket could
// reach another bucket, or any other mount, by walking out of it.
//
// Two layers close that: objectFSPath checks every key right where it is turned
// into a filesystem path, and objectKeyGuard rejects such a request before any
// handler sees it.
var (
	errInvalidObjectKey    = errors.New("the object key is not valid: it contains a \".\" or \"..\" path segment")
	errObjectKeyUnwritable = errors.New("the object key is not valid: it contains a NUL byte or a backslash")
	errObjectKeyEscapes    = errors.New("the object key is not valid: it resolves outside its bucket")
	errObjectKeyEmptySeg   = errors.New("the object key is not valid: it contains an empty path segment")
)

// cleanObjectKey rejects a key that contains a NUL byte, a backslash, or a "."
// or ".." segment. A key is otherwise free-form: dots inside a segment
// ("archive.tar.gz") and empty segments ("a//b") are left alone, because
// path.Join collapses those exactly as it always has and neither can leave the
// bucket.
//
// The backslash is rejected because path.Join and pathWithin here treat it as an
// ordinary character while the virtual filesystem behind the bucket does not:
// utils.FixAndCleanPath rewrites it to "/" before cleaning, so a key such as
// "..\..\other\secret.txt" passed the containment check below and then
// resolved outside the bucket, on read, write, delete and listing alike.
// cleanObjectPathKey is cleanObjectKey plus the rule that keeps two different
// keys from naming one file.
//
// An empty segment ("a//b") cannot leave the bucket, so cleanObjectKey lets it
// through, but path.Join collapses it: "a//b" and "a/b" resolved to the same
// file. A PUT of either overwrote the other, a GET of either returned the
// surviving bytes, a DELETE of either destroyed it and a copy from either read
// it - S3 keeps those as two objects and no client can tell them apart here.
// The file system is the limit, so the key is refused instead of being merged.
func cleanObjectPathKey(name string) error {
	if err := cleanObjectKey(name); err != nil {
		return err
	}
	if strings.Contains(name, "//") {
		return errObjectKeyEmptySeg
	}
	return nil
}

func cleanObjectKey(name string) error {
	if strings.ContainsRune(name, 0) || strings.ContainsRune(name, '\\') {
		return errObjectKeyUnwritable
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." {
			return errInvalidObjectKey
		}
	}
	return nil
}

// objectFSPath resolves an object key under a bucket root and reports an error
// when the result would not be inside that root.
//
// The containment check is redundant for a key that passed cleanObjectKey, and
// is kept deliberate: this is the function that decides which file a request
// touches, so it asserts the invariant itself instead of trusting its caller.
func objectFSPath(bucketPath, objectName string) (string, error) {
	if err := cleanObjectPathKey(objectName); err != nil {
		return "", err
	}
	fp := path.Join(bucketPath, objectName)
	if !pathWithin(bucketPath, fp) {
		return "", errObjectKeyEscapes
	}
	if err := confineToMount(fp); err != nil {
		return "", err
	}
	return fp, nil
}

// pathWithin reports whether the cleaned path fp is the bucket root itself or
// sits below it.
func pathWithin(bucketPath, fp string) bool {
	root := path.Clean("/" + strings.TrimPrefix(bucketPath, "/"))
	if root == "/" {
		// A bucket rooted at "/" owns the whole virtual filesystem. Bucket
		// parsing refuses that shape; this is the last line of defence for a
		// path that reaches here some other way.
		return false
	}
	// Both sides are normalised the same way so that a bucket path and a joined
	// path compare correctly whether or not either is absolute.
	target := path.Clean("/" + strings.TrimPrefix(fp, "/"))
	return target == root || strings.HasPrefix(target, root+"/")
}

// objectKeyGuard rejects requests whose path, listing prefix or copy source
// carries a "." or ".." segment. It runs before the authentication middleware:
// such a request is malformed, and answering it without touching a storage
// driver keeps the check out of every handler below.
func objectKeyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := checkRequestKey(r); err != nil {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument",
				bucketOnlyName(r.URL.Path), err.Error())
			return
		}
		// The key the backend eventually sees has been cleaned, and the clean
		// step drops the trailing slash that told a directory marker apart from
		// a plain prefix. Record the intent here, where the raw path is still
		// available.
		if strings.HasSuffix(r.URL.Path, "/") && strings.Contains(strings.Trim(r.URL.Path, "/"), "/") {
			r = r.WithContext(context.WithValue(r.Context(), dirMarkerRequestKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// dirMarkerRequestKey marks a request whose key was addressed with a trailing
// slash. The cleaned key the backend receives no longer carries it, and the
// difference matters: "prefix/" is a directory marker an S3 client wrote, while
// "prefix" is a plain prefix that must stay NoSuchKey.
type dirMarkerRequestKey struct{}

func isDirMarkerRequest(ctx context.Context) bool {
	isMarker, _ := ctx.Value(dirMarkerRequestKey{}).(bool)
	return isMarker
}

// checkRequestKey applies cleanObjectKey to every key a request can carry.
func checkRequestKey(r *http.Request) error {
	// The path holds the bucket and, for object requests, the key. The bucket
	// segment is checked too: no configured bucket may be named "." or "..".
	if err := cleanObjectPathKey(strings.Trim(r.URL.Path, "/")); err != nil {
		return err
	}
	// ListObjectsV1/V2 select the subtree with ?prefix=, which is turned into a
	// filesystem path by the listing code.
	if prefix := r.URL.Query().Get("prefix"); prefix != "" {
		if err := cleanObjectKey(prefix); err != nil {
			return err
		}
	}
	// A copy source is split into a bucket and a key by gofakes3 and by the
	// handlers in this package, all of which then index the second half without
	// checking that it exists. A value that names no key ("/bucket", an empty
	// header, "bucket?versionId=1") cannot be authorised against any key and
	// must be refused here: the authentication middleware only looks at the copy
	// source once credentials are configured, so this is the one check that also
	// covers a gateway running in its default open mode, and it answers 400
	// instead of letting the storage layer panic on a half-parsed source.
	if sourceBucket, sourceKey, present := copySourceRef(r); present {
		if sourceBucket == "" || sourceKey == "" {
			return errObjectKeyIncompleteCopySource
		}
		// The gateway streams an upload into a temporary key beside the real one
		// and renames it into place once it has been verified. Those temporaries
		// are reserved: a client that could address one would read another
		// tenant's half-written bytes, or overwrite them mid-upload.
		if isS3TempKey(sourceKey) {
			return errObjectKeyReserved
		}
		// The copy source is a key like any other: "a//b" names a different
		// object in S3 than "a/b" and must not be read as it.
		if err := cleanObjectPathKey(sourceKey); err != nil {
			return err
		}
	}
	for _, candidate := range []string{strings.Trim(r.URL.Path, "/"), r.URL.Query().Get("prefix")} {
		if candidate != "" && isS3TempKey(candidate) {
			return errObjectKeyReserved
		}
	}
	return nil
}

// errObjectKeyIncompleteCopySource marks an X-Amz-Copy-Source that does not name
// both a bucket and a key. S3 defines no such copy, and every consumer of the
// header assumes both halves are present.
var errObjectKeyIncompleteCopySource = errors.New(
	"the object key is not valid: X-Amz-Copy-Source must name a bucket and a key")

// errObjectKeyReserved marks a key that only the gateway may write.
var errObjectKeyReserved = errors.New("the object key is not valid: names whose last segment starts with " +
	strconv.Quote(s3TempKeyPrefix) + " are reserved for in-flight uploads")

// errObjectKeySymlink marks a key whose real location leaves its bucket.
var errObjectKeySymlink = errors.New("the object key is not valid: it resolves outside its bucket through a symbolic link")

// mountRootCache remembers the resolved filesystem root of a local mount, so
// that the per-request containment check does not pay for a mount lookup and a
// symlink resolution of the root every time.
type mountRootCacheEntry struct {
	root string
	at   time.Time
}

var (
	mountRootCache    sync.Map // mountPath -> mountRootCacheEntry
	mountRootCacheTTL = 10 * time.Second
)

// localMountRoot returns the resolved filesystem root of the mount that serves
// fsPath, or "" when the mount is not backed by the local filesystem (only the
// local driver can be escaped through a symlink) or cannot be resolved.
func localMountRoot(mountPath string, mountRoot string) string {
	if cached, ok := mountRootCache.Load(mountPath); ok {
		entry := cached.(mountRootCacheEntry)
		if time.Since(entry.at) < mountRootCacheTTL {
			return entry.root
		}
	}
	resolved := ""
	if mountRoot != "" {
		if real, err := filepath.EvalSymlinks(mountRoot); err == nil {
			resolved = real
		}
	}
	mountRootCache.Store(mountPath, mountRootCacheEntry{root: resolved, at: time.Now()})
	return resolved
}

// confineToMount refuses an object path whose real location, once symbolic
// links are resolved, is no longer inside the mount that serves it.
//
// The key itself cannot smuggle ".." past cleanObjectKey, but a symbolic link
// inside the storage tree can: a link such as bucket/link -> /elsewhere made
// every key below it read and write outside the bucket, and the gateway had no
// way to notice. Links that stay inside the mount keep working, which is what a
// storage tree assembled from linked directories expects.
func confineToMount(fp string) error {
	mountPath, err := op.GetStorageVirtualMountPath(fp)
	if err != nil {
		return nil
	}
	drv, err := op.GetStorageByMountPath(mountPath)
	if err != nil {
		return nil
	}
	loc, ok := drv.(*local.Local)
	if !ok {
		return nil
	}
	root := localMountRoot(mountPath, loc.RootFolderPath)
	if root == "" || root == string(filepath.Separator) {
		return nil
	}
	actual := strings.TrimPrefix(utils.FixAndCleanPath(fp), mountPath)
	return resolvesInside(root, filepath.Join(root, filepath.FromSlash(actual)))
}

// resolvesInside reports whether full, once symbolic links are resolved, is
// root itself or sits below it.
func resolvesInside(root, full string) error {
	// The object may not exist yet (a PUT), so resolve the deepest ancestor that
	// does and re-attach the missing tail to its resolved location.
	suffix := ""
	probe := full
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			real = filepath.Join(real, suffix)
			if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
				return errObjectKeySymlink
			}
			return nil
		}
		if !os.IsNotExist(err) && !errors.Is(err, os.ErrNotExist) {
			// Something else is wrong (permissions, a broken link): let the
			// driver decide instead of inventing an answer here.
			return nil
		}
		parent := filepath.Dir(probe)
		if parent == probe || len(parent) < len(root) {
			return nil
		}
		suffix = filepath.Join(filepath.Base(probe), suffix)
		probe = parent
	}
}

// asS3KeyError turns a key-validation error into the S3 error the client should
// see. Without it a rejected key surfaces as InternalError, which the SDKs
// treat as retryable: they would hammer the gateway with a request that can
// never succeed.
func asS3KeyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errInvalidObjectKey),
		errors.Is(err, errObjectKeyUnwritable),
		errors.Is(err, errObjectKeyEscapes),
		errors.Is(err, errObjectKeySymlink):
		return gofakes3.ErrorMessage(gofakes3.ErrInvalidArgument, err.Error())
	default:
		return err
	}
}

// asS3ErrorCode reports the S3 error code an error carries, looking through
// the fmt.Errorf("%w") chains this package builds. gofakes3 classifies errors
// with a type switch, so a wrapped code is reported as InternalError - a 500
// that the SDKs retry forever.
func asS3ErrorCode(err error) (gofakes3.ErrorCode, bool) {
	var code gofakes3.ErrorCode
	if errors.As(err, &code) {
		return code, true
	}
	// A ready-made ErrorResponse only implements error, so the interface check
	// above misses it even though its code is exactly what goes on the wire.
	var response *gofakes3.ErrorResponse
	if errors.As(err, &response) && response != nil && response.Code != "" {
		return response.Code, true
	}
	return "", false
}

// asS3WriteError maps a storage failure that is really about the key itself.
//
// A key whose last segment is longer than the filesystem allows produces
// ENAMETOOLONG from the driver, which gofakes3 would report as InternalError.
// The SDKs retry 5xx, so a client with a perfectly legal S3 key (up to 1024
// bytes, the limit gofakes3 already enforces) would hammer the gateway. The
// storage is what limits the length; name it.
func asS3WriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ENAMETOOLONG) || strings.Contains(err.Error(), "file name too long") {
		return gofakes3.ErrorMessage(gofakes3.ErrKeyTooLong, "the object key is longer than the storage backing this bucket allows")
	}
	// A Content-MD5 mismatch surfaces from the driver that copied the body as
	if errors.Is(err, errPayloadHashMismatch) {
		// gofakes3 has no status mapping for XAmzContentSHA256Mismatch, and
		// inventing the code would surface as 500 InternalError, which clients
		// retry. BadDigest is the 400 "the digest you declared is wrong" answer,
		// and the message names the header that disagreed.
		return gofakes3.ErrorMessage(gofakes3.ErrBadDigest, "The provided 'x-amz-content-sha256' header does not match what was computed.")
	}
	// gofakes3's ErrBadDigest; returning it untouched let gofakes3 classify the
	// unknown wrapper as InternalError, so a client that sent a wrong checksum
	// was told the server broke and retried the very same request.
	if errors.Is(err, gofakes3.ErrBadDigest) || strings.Contains(err.Error(), string(gofakes3.ErrBadDigest)) {
		return gofakes3.ErrorMessage(gofakes3.ErrBadDigest, "the Content-MD5 you specified did not match what we received")
	}
	return err
}
