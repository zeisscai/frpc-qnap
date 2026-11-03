// Package server 提供管理器的 HTTP 服务：静态前端 + REST API + 会话认证。
package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie = "frpcqnap_session"
	sessionTTL    = 12 * time.Hour
	pbkdf2Rounds  = 120_000
	loginMaxFails = 5
	loginLockTime = 5 * time.Minute
)

// Auth 负责口令校验与会话管理。口令用 PBKDF2-HMAC-SHA256 加盐存储，
// 会话只存在内存里（重启即全部失效，这对 NAS 场景是合理的安全默认值）。
type Auth struct {
	mu        sync.Mutex
	hash      string
	sessions  map[string]time.Time
	fails     int
	lockUntil time.Time
}

// NewAuth 用已存储的口令哈希初始化。hash 为空表示尚未设置口令。
func NewAuth(hash string) *Auth {
	return &Auth{hash: hash, sessions: make(map[string]time.Time)}
}

// HasPassword 表示是否已设置口令。
func (a *Auth) HasPassword() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hash != ""
}

// SetPassword 设置新口令（首次初始化或用户改密）。
func (a *Auth) SetPassword(plain string) (string, error) {
	if len(plain) < 8 {
		return "", errors.New("口令至少 8 位")
	}
	hash, err := HashPassword(plain)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.hash = hash
	a.mu.Unlock()
	return hash, nil
}

// Login 校验口令并返回会话令牌。
func (a *Auth) Login(plain string) (string, error) {
	a.mu.Lock()
	if time.Now().Before(a.lockUntil) {
		left := time.Until(a.lockUntil).Round(time.Second)
		a.mu.Unlock()
		return "", fmt.Errorf("尝试次数过多，请 %s 后再试", left)
	}
	hash := a.hash
	a.mu.Unlock()

	if hash == "" {
		return "", errors.New("尚未设置口令")
	}
	if !VerifyPassword(hash, plain) {
		a.mu.Lock()
		a.fails++
		if a.fails >= loginMaxFails {
			a.lockUntil = time.Now().Add(loginLockTime)
			a.fails = 0
		}
		a.mu.Unlock()
		return "", errors.New("口令错误")
	}

	a.mu.Lock()
	a.fails = 0
	a.mu.Unlock()

	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.sessions[token] = time.Now().Add(sessionTTL)
	a.mu.Unlock()
	return token, nil
}

// Valid 判断会话是否有效，并顺带续期。
func (a *Auth) Valid(token string) bool {
	if token == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.sessions, token)
		return false
	}
	a.sessions[token] = time.Now().Add(sessionTTL)
	return true
}

// Logout 注销会话。
func (a *Auth) Logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// HashPassword 生成 pbkdf2$rounds$salt$hash 格式的字符串。
func HashPassword(plain string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(plain), salt, pbkdf2Rounds, 32)
	return fmt.Sprintf("pbkdf2$%d$%s$%s",
		pbkdf2Rounds,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword 校验明文与哈希是否匹配，比较使用常量时间。
func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	rounds, err := strconv.Atoi(parts[1])
	if err != nil || rounds <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(plain), salt, rounds, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// pbkdf2SHA256 是 RFC 8018 PBKDF2 的最小实现，避免引入外部依赖。
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen

	dk := make([]byte, 0, blocks*hashLen)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)
	var idx [4]byte

	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(idx[:], uint32(block))
		prf.Write(idx[:])
		u = prf.Sum(u[:0])
		copy(t, u)

		for i := 1; i < iter; i++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range t {
				t[j] ^= u[j]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

// randomToken 生成 URL 安全的随机字符串。
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomPassword 生成便于手抄的随机口令（去掉了易混淆字符）。
func RandomPassword(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "admin12345"
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
