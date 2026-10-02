package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"
)

// Admin browser-session policy. The values follow the OWASP Session
// Management Cheat Sheet ranges for an internal console that is used for
// working stretches rather than one-off actions:
//
//   - SessionIdleTimeout: 30m, the top of OWASP's 15-30m band for
//     low-to-moderate risk apps. The admin port is private by contract, and
//     shorter values would log operators out mid-task between uploads.
//   - SessionAbsoluteTTL: 8h, OWASP's upper bound for a workday-long app. It
//     caps a stolen-but-active token even if the dashboard keeps polling.
//   - sessionCap: 1024 live sessions. Only an authenticated admin can create
//     one, so this is never reached by legitimate use; it exists so memory
//     stays bounded no matter how often credentials are replayed.
const (
	SessionCookieName  = "bb_admin_session"
	SessionIdleTimeout = 30 * time.Minute
	SessionAbsoluteTTL = 8 * time.Hour
	sessionCap         = 1024
	sessionTokenBytes  = 32
)

// sessionTokenLen is the base64url length of a sessionTokenBytes token; any
// other length cannot be one of ours and is rejected before hashing.
var sessionTokenLen = base64.RawURLEncoding.EncodedLen(sessionTokenBytes)

type session struct {
	accessKey string
	created   time.Time
	lastSeen  time.Time
}

// sessionStore keeps admin sessions in memory, keyed by the SHA-256 digest of
// the token so a heap dump or debug read never yields a usable cookie. Lookup
// by digest needs no constant-time compare: an attacker cannot steer the
// digest of a 256-bit random token toward a stored one. Sessions do not
// survive a restart, which simply forces a fresh login.
type sessionStore struct {
	mu       sync.Mutex
	byDigest map[[sha256.Size]byte]*session
	capacity int
	idle     time.Duration
	absolute time.Duration
	now      func() time.Time
}

func newSessionStore(capacity int, idle, absolute time.Duration, now func() time.Time) *sessionStore {
	return &sessionStore{
		byDigest: make(map[[sha256.Size]byte]*session, capacity),
		capacity: capacity,
		idle:     idle,
		absolute: absolute,
		now:      now,
	}
}

// sessions is the process-wide admin session store.
var sessions = newSessionStore(sessionCap, SessionIdleTimeout, SessionAbsoluteTTL, time.Now)

// create issues a fresh random token bound to accessKey and returns it. Only
// its digest is retained. crypto/rand.Read is documented to never return an
// error (it aborts the process instead), so there is no failure to surface.
func (s *sessionStore) create(accessKey string) string {
	raw := make([]byte, sessionTokenBytes)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.byDigest) >= s.capacity {
		s.pruneLocked(now)
	}
	if len(s.byDigest) >= s.capacity {
		s.evictLRULocked()
	}
	s.byDigest[sha256.Sum256([]byte(token))] = &session{accessKey: accessKey, created: now, lastSeen: now}
	return token
}

// lookup returns the access key bound to token and slides its idle window.
// An expired session is deleted on sight.
func (s *sessionStore) lookup(token string) (string, bool) {
	if len(token) != sessionTokenLen {
		return "", false
	}
	key := sha256.Sum256([]byte(token))

	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byDigest[key]
	if !ok {
		return "", false
	}
	now := s.now()
	if s.expired(sess, now) {
		delete(s.byDigest, key)
		return "", false
	}
	sess.lastSeen = now
	return sess.accessKey, true
}

// revoke deletes the session for token, if any.
func (s *sessionStore) revoke(token string) {
	key := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byDigest, key)
}

func (s *sessionStore) expired(sess *session, now time.Time) bool {
	return now.Sub(sess.lastSeen) >= s.idle || now.Sub(sess.created) >= s.absolute
}

// pruneLocked drops every expired session. Bounded by capacity.
func (s *sessionStore) pruneLocked(now time.Time) {
	for k, sess := range s.byDigest {
		if s.expired(sess, now) {
			delete(s.byDigest, k)
		}
	}
}

// evictLRULocked drops the least recently used session to make room. Bounded
// by capacity.
func (s *sessionStore) evictLRULocked() {
	var oldestKey [sha256.Size]byte
	var oldest *session
	for k, sess := range s.byDigest {
		if oldest == nil || sess.lastSeen.Before(oldest.lastSeen) {
			oldestKey, oldest = k, sess
		}
	}
	if oldest != nil {
		delete(s.byDigest, oldestKey)
	}
}
