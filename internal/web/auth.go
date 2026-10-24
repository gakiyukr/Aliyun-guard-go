// 網路面板認證層：PBKDF2-SHA256 密碼雜湊、HttpOnly Session（12 小時滑動過期、
// 附 CSRF Token）、登入失敗限流（5 分鐘內 5 次）。對應 web_panel.py 的
// hash_password / verify_password / create_session / login_allowed 系列。
package web

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionSeconds     = 12 * 60 * 60
	passwordIterations = 260000
	loginWindow        = 5 * time.Minute
	loginMaxAttempts   = 5
)

// hashPassword 產生 pbkdf2_sha256$<iter>$<salt hex>$<digest hex> 格式，
// 與 Python 版互通（至少 8 字元）。
func hashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("网页面板密码至少需要 8 个字符")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", passwordIterations, hex.EncodeToString(salt), hex.EncodeToString(digest)), nil
}

// verifyPassword 驗證密碼與儲存的雜湊（支援任意迭代數，恆定時間比較）。
func verifyPassword(password, encoded string) bool {
	parts := strings.SplitN(encoded, "$", 4)
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[1], "%d", &iterations); err != nil || iterations <= 0 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[2])
	expected, err2 := hex.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return constantTimeEqual(actual, expected)
}

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func randomToken(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

type sessionData struct {
	CSRF    string
	Expires time.Time
}

// sessionStore 為記憶體內 Session 儲存（滑動過期）。
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]sessionData
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]sessionData{}}
}

func (s *sessionStore) create() (string, sessionData) {
	data := sessionData{CSRF: randomToken(18), Expires: time.Now().Add(sessionSeconds * time.Second)}
	id := randomToken(32)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, value := range s.sessions {
		if value.Expires.Before(now) {
			delete(s.sessions, key)
		}
	}
	s.sessions[id] = data
	return id, data
}

func (s *sessionStore) get(id string) (sessionData, bool) {
	if id == "" {
		return sessionData{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.sessions[id]
	if !ok {
		return sessionData{}, false
	}
	if data.Expires.Before(time.Now()) {
		delete(s.sessions, id)
		return sessionData{}, false
	}
	data.Expires = time.Now().Add(sessionSeconds * time.Second)
	s.sessions[id] = data
	return data, true
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

// loginLimiter 對應 login_allowed/record_login_failure：5 分鐘滑動視窗計數。
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}}
}

func (l *loginLimiter) allowed(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	kept := l.attempts[address][:0]
	for _, at := range l.attempts[address] {
		if now.Sub(at) < loginWindow {
			kept = append(kept, at)
		}
	}
	l.attempts[address] = kept
	return len(kept) < loginMaxAttempts
}

func (l *loginLimiter) record(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[address] = append(l.attempts[address], time.Now())
}

func (l *loginLimiter) clear(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, address)
}

const sessionCookieName = "ag_session"

func (s *PanelServer) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.web["cookie_secure"] == true,
		MaxAge:   maxAge,
	}
}

func (s *PanelServer) sessionID(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// authenticated 校驗 Session；requireCSRF 時另驗 X-CSRF-Token 標頭。
// 驗證失敗時直接寫入 401/403 回應並回傳 false。
func (s *PanelServer) authenticated(w http.ResponseWriter, r *http.Request, requireCSRF bool) (string, bool) {
	id := s.sessionID(r)
	data, ok := s.sessions.get(id)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "登录已过期，请重新登录"})
		return "", false
	}
	if requireCSRF {
		if r.Header.Get("X-CSRF-Token") != data.CSRF {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "CSRF 校验失败，请刷新页面"})
			return "", false
		}
	}
	return id, true
}
