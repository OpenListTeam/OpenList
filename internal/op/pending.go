package op

import (
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// Pending entries give read-after-write visibility for writes made through
// OpenList when the backend List is eventually consistent or a List that was
// started before the write finishes after it.
//
// A pending-add keeps a written object visible until a remote List returns its
// name (pending-until-seen); a pending-delete hides a removed name until a
// remote List stops returning it. Both are merged into List results before
// they are cached, and expire after pendingTTL so that changes made outside
// OpenList are eventually reflected.
const pendingTTL = 5 * time.Minute

type pendingAdd struct {
	obj       model.Obj
	expiresAt time.Time
}

type dirPending struct {
	add map[string]pendingAdd
	del map[string]time.Time
}

type pendingStore struct {
	mu   sync.Mutex
	dirs map[string]*dirPending
}

var pendings = &pendingStore{dirs: make(map[string]*dirPending)}

func (ps *pendingStore) dirLocked(key string) *dirPending {
	d, ok := ps.dirs[key]
	if !ok {
		d = &dirPending{
			add: make(map[string]pendingAdd),
			del: make(map[string]time.Time),
		}
		ps.dirs[key] = d
	}
	return d
}

// gcLocked drops expired entries and reports whether d is now empty.
func (ps *pendingStore) gcLocked(d *dirPending, now time.Time) bool {
	for n, e := range d.add {
		if now.After(e.expiresAt) {
			delete(d.add, n)
		}
	}
	for n, exp := range d.del {
		if now.After(exp) {
			delete(d.del, n)
		}
	}
	return len(d.add) == 0 && len(d.del) == 0
}

// gcAllLocked drops expired entries in every directory and deletes directory
// keys that are left empty. Caller must hold ps.mu.
//
// Run this before dirLocked or any other lookup of dirKey. It may delete
// that key, so a *dirPending taken earlier would no longer be in the map.
func (ps *pendingStore) gcAllLocked(now time.Time) {
	for key, d := range ps.dirs {
		if ps.gcLocked(d, now) {
			delete(ps.dirs, key)
		}
	}
}

// addPending records obj as written into the directory dirKey.
func addPending(dirKey string, obj model.Obj) {
	if obj == nil {
		return
	}
	name := obj.GetName()
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d := pendings.dirLocked(dirKey)
	delete(d.del, name)
	d.add[name] = pendingAdd{obj: obj, expiresAt: now.Add(pendingTTL)}
}

// delPending records name as removed from the directory dirKey.
func delPending(dirKey, name string) {
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d := pendings.dirLocked(dirKey)
	delete(d.add, name)
	d.del[name] = now.Add(pendingTTL)
}

// getPending returns the pending-add object for name in dirKey, if any.
func getPending(dirKey, name string) (model.Obj, bool) {
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d, ok := pendings.dirs[dirKey]
	if !ok {
		return nil, false
	}
	e, ok := d.add[name]
	if !ok {
		return nil, false
	}
	return e.obj, true
}

// clearPendingTree drops pending entries of dirKey and all its descendants.
func clearPendingTree(dirKey string) {
	prefix := strings.TrimSuffix(dirKey, "/") + "/"
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	for k := range pendings.dirs {
		if k == dirKey || strings.HasPrefix(k, prefix) {
			delete(pendings.dirs, k)
		}
	}
}

func clearAllPending() {
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.dirs = make(map[string]*dirPending)
}

// mergePending applies pending entries of dirKey to a fresh remote listing.
// Names the remote listing already returns clear their pending-add; names it
// no longer returns clear their pending-delete. Remote objects win.
func mergePending(dirKey string, remote []model.Obj) []model.Obj {
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d, ok := pendings.dirs[dirKey]
	if !ok {
		return remote
	}

	remoteNames := make(map[string]struct{}, len(remote))
	for _, o := range remote {
		remoteNames[o.GetName()] = struct{}{}
	}
	for name := range d.del {
		if _, ok := remoteNames[name]; !ok {
			delete(d.del, name)
		}
	}

	merged := make([]model.Obj, 0, len(remote)+len(d.add))
	for _, o := range remote {
		if _, deleted := d.del[o.GetName()]; !deleted {
			merged = append(merged, o)
		}
	}
	for name, e := range d.add {
		if _, ok := remoteNames[name]; ok {
			delete(d.add, name)
			continue
		}
		merged = append(merged, e.obj)
	}

	if len(d.add) == 0 && len(d.del) == 0 {
		delete(pendings.dirs, dirKey)
	}
	return merged
}

// putPending records a written object and adds it to the directory cache if present.
func putPending(dirKey string, obj model.Obj) {
	addPending(dirKey, obj)
	if cache, exist := Cache.dirCache.Get(dirKey); exist {
		cache.UpdateObject(obj.GetName(), obj)
	}
}
