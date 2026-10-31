package op

import (
	"container/heap"
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
	add     map[string]pendingAdd
	del     map[string]time.Time
	version uint64
}

type pendingExpiry struct {
	expiresAt time.Time
	dirKey    string
	name      string
	isAdd     bool
}

type pendingExpiryHeap []pendingExpiry

func (h pendingExpiryHeap) Len() int           { return len(h) }
func (h pendingExpiryHeap) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h pendingExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *pendingExpiryHeap) Push(x any)        { *h = append(*h, x.(pendingExpiry)) }
func (h *pendingExpiryHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

type pendingStore struct {
	mu       sync.Mutex
	dirs     map[string]*dirPending
	expiries pendingExpiryHeap
	version  uint64
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

// gcAllLocked drops expired entries in every directory and deletes directory
// keys that are left empty. Expirations are kept in a min-heap so the common
// path only examines entries that are actually due. Caller must hold ps.mu.
func (ps *pendingStore) gcAllLocked(now time.Time) {
	for len(ps.expiries) > 0 && !ps.expiries[0].expiresAt.After(now) {
		expired := heap.Pop(&ps.expiries).(pendingExpiry)
		d, ok := ps.dirs[expired.dirKey]
		if !ok {
			continue
		}
		if expired.isAdd {
			e, ok := d.add[expired.name]
			if !ok || e.expiresAt.After(now) || !e.expiresAt.Equal(expired.expiresAt) {
				continue
			}
			delete(d.add, expired.name)
		} else {
			expiresAt, ok := d.del[expired.name]
			if !ok || expiresAt.After(now) || !expiresAt.Equal(expired.expiresAt) {
				continue
			}
			delete(d.del, expired.name)
		}
		ps.version++
		if len(d.add) == 0 && len(d.del) == 0 {
			delete(ps.dirs, expired.dirKey)
		}
	}
}

func (ps *pendingStore) addLocked(dirKey string, obj model.Obj, now time.Time) {
	d := ps.dirLocked(dirKey)
	name := obj.GetName()
	delete(d.del, name)
	expiresAt := now.Add(pendingTTL)
	d.add[name] = pendingAdd{obj: obj, expiresAt: expiresAt}
	d.version++
	ps.version++
	heap.Push(&ps.expiries, pendingExpiry{
		expiresAt: expiresAt,
		dirKey:    dirKey,
		name:      name,
		isAdd:     true,
	})
}

// addPending records obj as written into the directory dirKey.
func addPending(dirKey string, obj model.Obj) {
	if obj == nil {
		return
	}
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	pendings.addLocked(dirKey, obj, now)
}

// delPending records name as removed from the directory dirKey.
func delPending(dirKey, name string) {
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d := pendings.dirLocked(dirKey)
	delete(d.add, name)
	expiresAt := now.Add(pendingTTL)
	d.del[name] = expiresAt
	d.version++
	pendings.version++
	heap.Push(&pendings.expiries, pendingExpiry{
		expiresAt: expiresAt,
		dirKey:    dirKey,
		name:      name,
	})
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
			pendings.version++
		}
	}
}

func clearAllPending() {
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.dirs = make(map[string]*dirPending)
	pendings.expiries = nil
	pendings.version++
}

// mergePending applies pending entries of dirKey to a fresh remote listing.
// Names the remote listing already returns clear their pending-add; names it
// no longer returns clear their pending-delete. Remote objects win.
func mergePending(dirKey string, remote []model.Obj) []model.Obj {
	merged, _, _ := mergePendingState(dirKey, remote)
	return merged
}

// mergePendingState also returns the pending directory and version used to
// produce the result. The state lets the caller avoid caching a result that a
// concurrent pending update has already made stale.
func mergePendingState(dirKey string, remote []model.Obj) ([]model.Obj, *dirPending, uint64) {
	now := time.Now()
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	pendings.gcAllLocked(now)
	d, ok := pendings.dirs[dirKey]
	if !ok {
		return remote, nil, pendings.version
	}

	remoteNames := make(map[string]struct{}, len(remote))
	for _, o := range remote {
		remoteNames[o.GetName()] = struct{}{}
	}
	changed := false
	for name := range d.del {
		if _, ok := remoteNames[name]; !ok {
			delete(d.del, name)
			changed = true
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
			changed = true
			continue
		}
		merged = append(merged, e.obj)
	}

	if changed {
		d.version++
		pendings.version++
	}
	if len(d.add) == 0 && len(d.del) == 0 {
		delete(pendings.dirs, dirKey)
		return merged, nil, pendings.version
	}
	return merged, d, pendings.version
}

// cachePendingResult runs fn only if no pending change occurred after the
// result was merged. It prevents a concurrent Put/List from overwriting a
// newer cache state with an older result.
func cachePendingResult(dirKey string, d *dirPending, version uint64, fn func()) bool {
	pendings.mu.Lock()
	defer pendings.mu.Unlock()
	current, ok := pendings.dirs[dirKey]
	if pendings.version != version {
		return false
	}
	if d == nil {
		if ok {
			return false
		}
	} else if !ok || current != d {
		return false
	}
	fn()
	return true
}

// putPending records a written object and adds it to the directory cache if present.
func putPending(dirKey string, obj model.Obj) {
	addPending(dirKey, obj)
	if cache, exist := Cache.dirCache.Get(dirKey); exist {
		if model.ObjHasMask(obj, model.Temp) && cache.HasObject(obj.GetName()) {
			Cache.dirCache.Delete(dirKey)
			return
		}
		cache.UpdateObject(obj.GetName(), obj)
	}
}
