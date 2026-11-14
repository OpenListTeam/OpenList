package driver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
)

// An upload that must leave its destination alone until it is complete writes
// a staging object next to it and moves it into place at the end, so a failed
// or canceled upload only ever removes its own staging object. A staging name
// records when the object was created and carries a tag that only this server
// can compute for this storage. That lets a storage reclaim what a killed
// process left behind while leaving everything else alone: a user's own
// look-alike names, and the staging objects of other storages, such as a chunk
// storage layered on this one.
const (
	StagingPrefix = ".openlist-upload-"
	// a staging object that neither started nor changed for this long belongs
	// to an upload that can no longer finish
	StaleStagingAge = 24 * time.Hour
)

// creation time (unix seconds, hex), nonce, tag
var stagingRe = regexp.MustCompile(`^\.openlist-upload-([0-9a-f]{8})([0-9A-Za-z]{8})([0-9a-f]{16})$`)

var (
	fallbackSecretOnce sync.Once
	fallbackSecret     string
)

func stagingKey(storage *model.Storage) []byte {
	secret := ""
	if conf.Conf != nil {
		secret = conf.Conf.JwtSecret
	}
	if secret == "" {
		// no server secret (tests): names are only recognized by this process
		fallbackSecretOnce.Do(func() { fallbackSecret = random.String(32) })
		secret = fallbackSecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("openlist upload staging\x00"))
	mac.Write([]byte(strconv.FormatUint(uint64(storage.ID), 10)))
	return mac.Sum(nil)
}

func stagingTag(key []byte, stamp, nonce string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(stamp))
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil)[:8])
}

// StagingName returns a new staging name for an object of storage.
func StagingName(storage *model.Storage) string {
	return StagingNameAt(storage, time.Now())
}

// StagingNameAt returns a staging name for an object of storage created at t.
func StagingNameAt(storage *model.Storage, t time.Time) string {
	stamp := fmt.Sprintf("%08x", uint32(t.Unix()))
	nonce := random.String(8)
	return StagingPrefix + stamp + nonce + stagingTag(stagingKey(storage), stamp, nonce)
}

// StagingCreated reports whether name is a staging name of storage and, if
// so, when that staging object was created.
func StagingCreated(storage *model.Storage, name string) (time.Time, bool) {
	m := stagingRe.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(m[3]), []byte(stagingTag(stagingKey(storage), m[1], m[2]))) {
		return time.Time{}, false
	}
	sec, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(sec), 0), true
}

// StagingInUse tracks the paths a storage is writing right now: its staging
// objects, and whatever else it keeps out of sight until an upload is done.
// The zero value is ready to use.
type StagingInUse struct{ paths sync.Map }

// Hold marks path as in use until release is called.
func (u *StagingInUse) Hold(path string) (release func()) {
	u.paths.Store(path, struct{}{})
	return func() { u.paths.Delete(path) }
}

// Held reports whether path is in use.
func (u *StagingInUse) Held(path string) bool {
	_, ok := u.paths.Load(path)
	return ok
}

// Reclaimable reports whether the staging object at path, created at created
// and last modified at modified, belongs to an upload that can no longer
// finish: nothing here writes it, and it neither started nor changed within
// StaleStagingAge. The modification time alone proves nothing, since some
// storages report one that never changes, but a recent one means another
// process sharing the storage may still be writing the object.
func (u *StagingInUse) Reclaimable(path string, created, modified time.Time) bool {
	if u.Held(path) {
		return false
	}
	last := created
	if modified.After(last) {
		last = modified
	}
	return time.Since(last) > StaleStagingAge
}

// KeyedMutex runs work on one key at a time, such as the uploads of one
// destination. Keys that differ only in case are one key: many storages (SMB
// shares, the default Windows and macOS file systems) take such names for one
// file, and two files that are not one only take turns. The zero value is
// ready to use.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	held chan struct{} // holds a token while the lock is taken
	refs int
}

// Lock takes the lock of key. It gives up with ctx's error once ctx ends, also
// when ctx ends just as the lock comes free: the work of a canceled upload
// must not start.
func (k *KeyedMutex) Lock(ctx context.Context, key string) (unlock func(), err error) {
	key = strings.ToLower(key)
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{held: make(chan struct{}, 1)}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	drop := func() {
		k.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
	select {
	case l.held <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
	unlock = func() {
		<-l.held
		drop()
	}
	if err := ctx.Err(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}
