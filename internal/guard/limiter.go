// Package guard는 인증 브루트포스 방어용 in-memory rate limiter다.
//
// IP 단위 슬라이딩 윈도우: 윈도우 안에서 실패가 임계치를 넘으면
// 일정 시간 차단한다. 성공하면 해당 IP의 기록을 지운다.
// 단일 인스턴스 메모리 기반 — 멀티 레플리카에선 인스턴스별로 독립
// 카운트되지만, 방어 목적(무한 시도 차단)에는 충분하다.
package guard

import (
	"sync"
	"time"
)

// Limiter는 키(보통 IP)별 인증 실패를 추적한다.
type Limiter struct {
	mu    sync.Mutex
	entry map[string]*entry

	// MaxFailure는 윈도우 내 허용 실패 횟수 (초과 시 차단).
	MaxFailure int
	// Window는 실패 카운트 윈도우.
	Window time.Duration
	// BlockFor는 차단 지속 시간.
	BlockFor time.Duration

	timeNow func() time.Time // 테스트 오버라이드
}

type entry struct {
	failureCount int
	windowStart  time.Time
	blockedUntil time.Time
}

// NewLimiter는 기본 파라미터의 limiter를 만든다:
// 15분 안에 10회 실패 → 15분 차단.
func NewLimiter() *Limiter {
	return &Limiter{
		entry:      map[string]*entry{},
		MaxFailure: 10,
		Window:     15 * time.Minute,
		BlockFor:   15 * time.Minute,
		timeNow:    time.Now,
	}
}

// Allow는 키가 현재 인증 시도를 해도 되는지 판단한다.
func (l *Limiter) Allow(key string) bool {
	if key == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entry[key]
	if !ok {
		return true
	}
	now := l.timeNow()
	if now.Before(e.blockedUntil) {
		return false
	}
	// 윈도우 만료 → 리셋
	if now.Sub(e.windowStart) > l.Window {
		delete(l.entry, key)
	}
	return true
}

// Fail은 인증 실패를 기록한다. 임계치 도달 시 차단 시작.
func (l *Limiter) Fail(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.timeNow()
	e, ok := l.entry[key]
	if !ok || now.Sub(e.windowStart) > l.Window {
		e = &entry{windowStart: now}
		l.entry[key] = e
	}
	e.failureCount++
	if e.failureCount >= l.MaxFailure {
		e.blockedUntil = now.Add(l.BlockFor)
		// 차단 시작하면 윈도우도 새로 (차단 해제 후 재시도 카운트 리셋)
		e.failureCount = 0
		e.windowStart = now
	}

	// 게으른 청소 — 맵이 커지는 것 방지 (만료 엔트리 제거)
	if len(l.entry) > 10000 {
		for k, v := range l.entry {
			if now.Sub(v.windowStart) > l.Window && now.After(v.blockedUntil) {
				delete(l.entry, k)
			}
		}
	}
}

// Success는 인증 성공 — 해당 키의 실패 기록을 지운다.
func (l *Limiter) Success(key string) {
	if key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entry, key)
}
