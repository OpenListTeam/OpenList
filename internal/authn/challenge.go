package authn

import (
	"container/heap"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

const (
	ChallengeTTL                 = 5 * time.Minute
	maxChallengeCount            = 10000
	maxPublicLoginChallenges     = 9000
	maxChallengesPerAdmissionKey = 5
)

var (
	ErrChallengeInvalid   = errors.New("passkey challenge is invalid, expired, or already used")
	ErrChallengeAdmission = errors.New("too many pending passkey challenges")
	ErrChallengeCapacity  = errors.New("passkey challenge store is at capacity")
	challenges            = newChallengeStore()
)

type Ceremony string

const (
	CeremonyLogin        Ceremony = "login"
	CeremonyRegistration Ceremony = "registration"
)

type Challenge struct {
	Session  webauthn.SessionData
	Ceremony Ceremony
	UserID   uint
	Name     string
	Expires  time.Time
}

type challengeEntry struct {
	key          string
	admissionKey string
	challenge    Challenge
	index        int
}

type challengeExpiryHeap []*challengeEntry

func (h challengeExpiryHeap) Len() int {
	return len(h)
}

func (h challengeExpiryHeap) Less(i, j int) bool {
	return h[i].challenge.Expires.Before(h[j].challenge.Expires)
}

func (h challengeExpiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *challengeExpiryHeap) Push(value any) {
	entry := value.(*challengeEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *challengeExpiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.index = -1
	*h = old[:last]
	return entry
}

type challengeStore struct {
	mu              sync.Mutex
	items           map[string]*challengeEntry
	admissionCounts map[string]int
	ceremonyCounts  map[Ceremony]int
	expirations     challengeExpiryHeap
	now             func() time.Time
}

func newChallengeStore() *challengeStore {
	return &challengeStore{
		items:           make(map[string]*challengeEntry),
		admissionCounts: make(map[string]int),
		ceremonyCounts:  make(map[Ceremony]int),
		now:             time.Now,
	}
}

func (s *challengeStore) Put(challenge Challenge, admissionKey string) (string, error) {
	if admissionKey == "" {
		return "", errors.New("passkey challenge admission key is required")
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(id)
	now := s.now()
	challenge.Expires = now.Add(ChallengeTTL)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.removeExpired(now)
	if s.admissionCounts[admissionKey] >= maxChallengesPerAdmissionKey {
		return "", ErrChallengeAdmission
	}
	if challenge.Ceremony == CeremonyLogin && s.ceremonyCounts[CeremonyLogin] >= maxPublicLoginChallenges {
		return "", ErrChallengeCapacity
	}
	if len(s.items) >= maxChallengeCount {
		return "", ErrChallengeCapacity
	}
	if _, exists := s.items[key]; exists {
		return "", errors.New("passkey challenge identifier collision")
	}

	entry := &challengeEntry{
		key:          key,
		admissionKey: admissionKey,
		challenge:    challenge,
	}
	s.items[key] = entry
	s.admissionCounts[admissionKey]++
	s.ceremonyCounts[challenge.Ceremony]++
	heap.Push(&s.expirations, entry)
	return key, nil
}

func (s *challengeStore) Consume(key string, ceremony Ceremony, userID uint) (Challenge, error) {
	s.mu.Lock()
	entry, ok := s.items[key]
	if ok {
		s.remove(entry)
	}
	s.mu.Unlock()

	if !ok {
		return Challenge{}, ErrChallengeInvalid
	}
	challenge := entry.challenge
	if challenge.Ceremony != ceremony || challenge.UserID != userID || !s.now().Before(challenge.Expires) {
		return Challenge{}, ErrChallengeInvalid
	}
	return challenge, nil
}

func (s *challengeStore) removeExpired(now time.Time) {
	for s.expirations.Len() > 0 && !now.Before(s.expirations[0].challenge.Expires) {
		s.remove(s.expirations[0])
	}
}

func (s *challengeStore) remove(entry *challengeEntry) {
	delete(s.items, entry.key)
	heap.Remove(&s.expirations, entry.index)
	s.admissionCounts[entry.admissionKey]--
	s.ceremonyCounts[entry.challenge.Ceremony]--
	if s.admissionCounts[entry.admissionKey] == 0 {
		delete(s.admissionCounts, entry.admissionKey)
	}
	if s.ceremonyCounts[entry.challenge.Ceremony] == 0 {
		delete(s.ceremonyCounts, entry.challenge.Ceremony)
	}
}

func StoreChallenge(challenge Challenge, admissionKey string) (string, error) {
	return challenges.Put(challenge, admissionKey)
}

func ConsumeChallenge(key string, ceremony Ceremony, userID uint) (Challenge, error) {
	return challenges.Consume(key, ceremony, userID)
}
