package authn

import (
	"container/list"
	"errors"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	challengeAdmissionInterval        = time.Minute
	challengeAdmissionBurst           = 5
	maxLoginChallengeAdmissionKeys    = maxPublicLoginChallenges
	maxRegisterChallengeAdmissionKeys = maxChallengeCount - maxPublicLoginChallenges
)

var (
	loginAdmissions        = newAdmissionLimiter(maxLoginChallengeAdmissionKeys)
	registrationAdmissions = newAdmissionLimiter(maxRegisterChallengeAdmissionKeys)
)

type admissionEntry struct {
	key      string
	limiter  *rate.Limiter
	lastSeen time.Time
	element  *list.Element
}

type admissionLimiter struct {
	mu      sync.Mutex
	entries map[string]*admissionEntry
	order   *list.List
	now     func() time.Time
	maxKeys int
}

func newAdmissionLimiter(maxKeys int) *admissionLimiter {
	return &admissionLimiter{
		entries: make(map[string]*admissionEntry),
		order:   list.New(),
		now:     time.Now,
		maxKeys: maxKeys,
	}
}

func (l *admissionLimiter) Allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.removeExpired(now)
	if entry, ok := l.entries[key]; ok {
		entry.lastSeen = now
		l.order.MoveToBack(entry.element)
		return entry.limiter.AllowN(now, 1)
	}
	if len(l.entries) >= l.maxKeys {
		return false
	}

	entry := &admissionEntry{
		key:      key,
		limiter:  rate.NewLimiter(rate.Every(challengeAdmissionInterval), challengeAdmissionBurst),
		lastSeen: now,
	}
	entry.element = l.order.PushBack(entry)
	l.entries[key] = entry
	return entry.limiter.AllowN(now, 1)
}

func (l *admissionLimiter) removeExpired(now time.Time) {
	for {
		element := l.order.Front()
		if element == nil {
			return
		}
		entry := element.Value.(*admissionEntry)
		if now.Sub(entry.lastSeen) < ChallengeTTL {
			return
		}
		delete(l.entries, entry.key)
		l.order.Remove(element)
	}
}

func AdmitChallenge(ceremony Ceremony, admissionKey string) error {
	if admissionKey == "" {
		return errors.New("passkey challenge admission key is required")
	}
	var limiter *admissionLimiter
	switch ceremony {
	case CeremonyLogin:
		limiter = loginAdmissions
	case CeremonyRegistration:
		limiter = registrationAdmissions
	default:
		return errors.New("passkey challenge ceremony is invalid")
	}
	if !limiter.Allow(admissionKey) {
		return ErrChallengeAdmission
	}
	return nil
}
