// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package webdav

import (
	"container/heap"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrConfirmationFailed is returned by a LockSystem's Confirm method.
	ErrConfirmationFailed = errors.New("webdav: confirmation failed")
	// ErrForbidden is returned by a LockSystem's Unlock method.
	ErrForbidden = errors.New("webdav: forbidden")
	// ErrLocked is returned by a LockSystem's Create, Refresh and Unlock methods.
	ErrLocked = errors.New("webdav: locked")
	// ErrNoSuchLock is returned by a LockSystem's Refresh and Unlock methods.
	ErrNoSuchLock = errors.New("webdav: no such lock")
)

// Condition can match a WebDAV resource, based on a token or ETag.
// Exactly one of Token and ETag should be non-empty.
type Condition struct {
	Not   bool
	Token string
	ETag  string
}

// LockSystem manages access to a collection of named resources. The elements
// in a lock name are separated by slash ('/', U+002F) characters, regardless
// of host operating system convention.
type LockSystem interface {
	// Confirm confirms that the caller can claim all of the locks specified by
	// the given conditions, and that holding the union of all of those locks
	// gives exclusive access to all of the named resources. Up to two resources
	// can be named. Empty names are ignored.
	//
	// Exactly one of release and err will be non-nil. If release is non-nil,
	// all of the requested locks are held until release is called. Calling
	// release does not unlock the lock, in the WebDAV UNLOCK sense, but once
	// Confirm has confirmed that a lock claim is valid, that lock cannot be
	// Confirmed again until it has been released.
	//
	// If Confirm returns ErrConfirmationFailed then the Handler will continue
	// to try any other set of locks presented (a WebDAV HTTP request can
	// present more than one set of locks). If it returns any other non-nil
	// error, the Handler will write a "500 Internal Server Error" HTTP status.
	Confirm(now time.Time, name0, name1 string, conditions ...Condition) (release func(), err error)

	// Create creates a lock with the given depth, duration, owner and root
	// (name). The depth will either be negative (meaning infinite) or zero.
	//
	// If Create returns ErrLocked then the Handler will write a "423 Locked"
	// HTTP status. If it returns any other non-nil error, the Handler will
	// write a "500 Internal Server Error" HTTP status.
	//
	// See http://www.webdav.org/specs/rfc4918.html#rfc.section.9.10.6 for
	// when to use each error.
	//
	// The token returned identifies the created lock. It should be an absolute
	// URI as defined by RFC 3986, Section 4.3. In particular, it should not
	// contain whitespace.
	Create(now time.Time, details LockDetails) (token string, err error)

	// Refresh refreshes the lock with the given token.
	//
	// If Refresh returns ErrLocked then the Handler will write a "423 Locked"
	// HTTP Status. If Refresh returns ErrNoSuchLock then the Handler will write
	// a "412 Precondition Failed" HTTP Status. If it returns any other non-nil
	// error, the Handler will write a "500 Internal Server Error" HTTP status.
	//
	// See http://www.webdav.org/specs/rfc4918.html#rfc.section.9.10.6 for
	// when to use each error.
	Refresh(now time.Time, token string, duration time.Duration) (LockDetails, error)

	// Unlock unlocks the lock with the given token.
	//
	// If Unlock returns ErrForbidden then the Handler will write a "403
	// Forbidden" HTTP Status. If Unlock returns ErrLocked then the Handler
	// will write a "423 Locked" HTTP status. If Unlock returns ErrNoSuchLock
	// then the Handler will write a "409 Conflict" HTTP Status. If it returns
	// any other non-nil error, the Handler will write a "500 Internal Server
	// Error" HTTP status.
	//
	// See http://www.webdav.org/specs/rfc4918.html#rfc.section.9.11.1 for
	// when to use each error.
	Unlock(now time.Time, token string) error
}

// LockDetails are a lock's metadata.
type LockDetails struct {
	// Root is the root resource name being locked. For a zero-depth lock, the
	// root is the only resource being locked.
	Root string
	// Duration is the lock timeout. A negative duration means infinite.
	Duration time.Duration
	// OwnerXML is the verbatim <owner> XML given in a LOCK HTTP request.
	//
	// TODO: does the "verbatim" nature play well with XML namespaces?
	// Does the OwnerXML field need to have more structure? See
	// https://codereview.appspot.com/175140043/#msg2
	OwnerXML string
	// ZeroDepth is whether the lock has zero depth. If it does not have zero
	// depth, it has infinite depth.
	ZeroDepth bool
	// Shared is whether the lock may coexist with other shared locks on Root.
	Shared bool
}

// NewMemLS returns a new in-memory LockSystem.
func NewMemLS() LockSystem {
	return &memLS{
		byName:  make(map[string]*memLSNode),
		byToken: make(map[string]*memLSLock),
		gen:     uint64(time.Now().Unix()),
	}
}

type memLS struct {
	mu      sync.Mutex
	byName  map[string]*memLSNode
	byToken map[string]*memLSLock
	gen     uint64
	// byExpiry only contains those nodes whose LockDetails have a finite
	// Duration and are yet to expire.
	byExpiry byExpiry
}

func (m *memLS) nextToken() string {
	m.gen++
	return strconv.FormatUint(m.gen, 10)
}

func (m *memLS) collectExpiredNodes(now time.Time) {
	for len(m.byExpiry) > 0 {
		if now.Before(m.byExpiry[0].expiry) {
			break
		}
		m.remove(m.byExpiry[0])
	}
}

func (m *memLS) Confirm(now time.Time, name0, name1 string, conditions ...Condition) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectExpiredNodes(now)

	var n0, n1 *memLSLock
	if name0 != "" {
		if n0 = m.lookup(slashClean(name0), conditions...); n0 == nil {
			return nil, ErrConfirmationFailed
		}
	}
	if name1 != "" {
		if n1 = m.lookup(slashClean(name1), conditions...); n1 == nil {
			return nil, ErrConfirmationFailed
		}
	}

	// Don't hold the same node twice.
	if n1 == n0 {
		n1 = nil
	}

	if n0 != nil {
		m.hold(n0)
	}
	if n1 != nil {
		m.hold(n1)
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if n1 != nil {
			m.unhold(n1)
		}
		if n0 != nil {
			m.unhold(n0)
		}
	}, nil
}

func (m *memLS) lookup(name string, conditions ...Condition) *memLSLock {
	// TODO: support Condition.Not and Condition.ETag.
	for _, c := range conditions {
		lock := m.byToken[c.Token]
		if lock == nil || lock.held {
			continue
		}
		if name == lock.details.Root {
			return lock
		}
		if lock.details.ZeroDepth {
			continue
		}
		if lock.details.Root == "/" || strings.HasPrefix(name, lock.details.Root+"/") {
			return lock
		}
	}
	return nil
}

func (m *memLS) hold(lock *memLSLock) {
	if lock.held {
		panic("webdav: memLS inconsistent held state")
	}
	lock.held = true
	if lock.details.Duration >= 0 && lock.byExpiryIndex >= 0 {
		heap.Remove(&m.byExpiry, lock.byExpiryIndex)
	}
}

func (m *memLS) unhold(lock *memLSLock) {
	if !lock.held {
		panic("webdav: memLS inconsistent held state")
	}
	lock.held = false
	if lock.details.Duration >= 0 {
		heap.Push(&m.byExpiry, lock)
	}
}

func (m *memLS) Create(now time.Time, details LockDetails) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectExpiredNodes(now)
	details.Root = slashClean(details.Root)

	if !m.canCreate(details.Root, details.ZeroDepth, details.Shared) {
		return "", ErrLocked
	}
	node := m.create(details.Root)
	lock := &memLSLock{
		token:         m.nextToken(),
		details:       details,
		node:          node,
		byExpiryIndex: -1,
	}
	node.locks[lock.token] = lock
	m.byToken[lock.token] = lock
	if lock.details.Duration >= 0 {
		lock.expiry = now.Add(lock.details.Duration)
		heap.Push(&m.byExpiry, lock)
	}
	return lock.token, nil
}

func (m *memLS) Refresh(now time.Time, token string, duration time.Duration) (LockDetails, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectExpiredNodes(now)

	lock := m.byToken[token]
	if lock == nil {
		return LockDetails{}, ErrNoSuchLock
	}
	if lock.held {
		return LockDetails{}, ErrLocked
	}
	if lock.byExpiryIndex >= 0 {
		heap.Remove(&m.byExpiry, lock.byExpiryIndex)
	}
	lock.details.Duration = duration
	if lock.details.Duration >= 0 {
		lock.expiry = now.Add(lock.details.Duration)
		heap.Push(&m.byExpiry, lock)
	}
	return lock.details, nil
}

func (m *memLS) Unlock(now time.Time, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.collectExpiredNodes(now)

	lock := m.byToken[token]
	if lock == nil {
		return ErrNoSuchLock
	}
	if lock.held {
		return ErrLocked
	}
	m.remove(lock)
	return nil
}

func (m *memLS) canCreate(name string, zeroDepth bool, shared ...bool) bool {
	wantShared := len(shared) > 0 && shared[0]
	return walkToRoot(name, func(name0 string, first bool) bool {
		n := m.byName[name0]
		if n == nil {
			return true
		}
		if first {
			if len(n.locks) != 0 {
				if !wantShared || (!zeroDepth && n.refCount != len(n.locks)) {
					return false
				}
				for _, lock := range n.locks {
					if !lock.details.Shared || lock.details.ZeroDepth != zeroDepth {
						return false
					}
				}
				return true
			}
			if !zeroDepth && n.refCount > 0 {
				// The requested lock depth is infinite, and the fact that n exists
				// (n != nil) means that a descendent of the target node is locked.
				return false
			}
		} else if n.hasInfiniteLock() {
			// An ancestor of the target node is locked with infinite depth.
			return false
		}
		return true
	})
}

func (m *memLS) create(name string) (ret *memLSNode) {
	walkToRoot(name, func(name0 string, first bool) bool {
		n := m.byName[name0]
		if n == nil {
			n = &memLSNode{
				details: LockDetails{Root: name0},
				locks:   make(map[string]*memLSLock),
			}
			m.byName[name0] = n
		}
		n.refCount++
		if first {
			ret = n
		}
		return true
	})
	return ret
}

func (m *memLS) remove(lock *memLSLock) {
	delete(m.byToken, lock.token)
	delete(lock.node.locks, lock.token)
	walkToRoot(lock.details.Root, func(name0 string, first bool) bool {
		x := m.byName[name0]
		x.refCount--
		if x.refCount == 0 {
			delete(m.byName, name0)
		}
		return true
	})
	if lock.byExpiryIndex >= 0 {
		heap.Remove(&m.byExpiry, lock.byExpiryIndex)
	}
}

func walkToRoot(name string, f func(name0 string, first bool) bool) bool {
	for first := true; ; first = false {
		if !f(name, first) {
			return false
		}
		if name == "/" {
			break
		}
		name = name[:strings.LastIndex(name, "/")]
		if name == "" {
			name = "/"
		}
	}
	return true
}

type memLSNode struct {
	// details identifies the resource path represented by this node.
	details LockDetails
	// locks contains the independent lock instances rooted at this resource.
	locks map[string]*memLSLock
	// refCount is the number of self-or-descendent lock instances.
	refCount int
}

type memLSLock struct {
	token         string
	details       LockDetails
	node          *memLSNode
	expiry        time.Time
	byExpiryIndex int
	held          bool
}

func (n *memLSNode) hasInfiniteLock() bool {
	for _, lock := range n.locks {
		if !lock.details.ZeroDepth {
			return true
		}
	}
	return false
}

type byExpiry []*memLSLock

func (b *byExpiry) Len() int {
	return len(*b)
}

func (b *byExpiry) Less(i, j int) bool {
	return (*b)[i].expiry.Before((*b)[j].expiry)
}

func (b *byExpiry) Swap(i, j int) {
	(*b)[i], (*b)[j] = (*b)[j], (*b)[i]
	(*b)[i].byExpiryIndex = i
	(*b)[j].byExpiryIndex = j
}

func (b *byExpiry) Push(x interface{}) {
	lock := x.(*memLSLock)
	lock.byExpiryIndex = len(*b)
	*b = append(*b, lock)
}

func (b *byExpiry) Pop() interface{} {
	i := len(*b) - 1
	lock := (*b)[i]
	(*b)[i] = nil
	lock.byExpiryIndex = -1
	*b = (*b)[:i]
	return lock
}

const infiniteTimeout = -1

// parseTimeout parses the Timeout HTTP header, as per section 10.7. If s is
// empty, an infiniteTimeout is returned.
func parseTimeout(s string) (time.Duration, error) {
	if s == "" {
		return infiniteTimeout, nil
	}
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "Infinite" {
		return infiniteTimeout, nil
	}
	const pre = "Second-"
	if !strings.HasPrefix(s, pre) {
		return 0, errInvalidTimeout
	}
	s = s[len(pre):]
	if s == "" || s[0] < '0' || '9' < s[0] {
		return 0, errInvalidTimeout
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || 1<<32-1 < n {
		return 0, errInvalidTimeout
	}
	return time.Duration(n) * time.Second, nil
}
