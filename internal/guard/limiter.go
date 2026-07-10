// Package guard는 인증 브루트포스 방어용 in-memory rate limiter다.
//
// 키 단위 슬라이딩 윈도우: 윈도우 안에서 실패가 임계치를 넘으면
// 일정 시간 차단한다. 반복 차단 시 차단 시간이 지수적으로 늘어난다
// (BlockFor → 2x → 4x … MaxBlock 상한). 성공하면 해당 키의 기록을 지운다.
//
// 키는 보통 IP지만, 분산 브루트포스(여러 IP → 한 계정) 방어를 위해
// 계정 키를 함께 쓴다 (호출부에서 "ip:"/"acct:" prefix로 구분).
// IPv6는 인터페이스 ID 로테이션 우회를 막기 위해 /64 prefix로 정규화한다.
//
// 단일 인스턴스 메모리 기반 — 멀티 레플리카에선 인스턴스별로 독립
// 카운트되지만, 방어 목적(무한 시도 차단)에는 충분하다.
package guard

import (
	"net"
	"sync"
	"time"
)

// Limiter는 키별 인증 실패를 추적한다.
type Limiter struct {
	mu    sync.Mutex
	entry map[string]*entry

	// MaxFailure는 윈도우 내 허용 실패 횟수 (초과 시 차단).
	MaxFailure int
	// Window는 실패 카운트 윈도우.
	Window time.Duration
	// BlockFor는 최초 차단 지속 시간 (반복 차단 시 지수 증가).
	BlockFor time.Duration
	// MaxBlock은 지수 증가의 상한.
	MaxBlock time.Duration

	timeNow func() time.Time // 테스트 오버라이드
}

type entry struct {
	failureCount int
	windowStart  time.Time
	blockedUntil time.Time
	blockCount   int // 누적 차단 횟수 — 지수 백오프 지수
}

// NewLimiter는 기본 파라미터의 limiter를 만든다:
// 15분 안에 10회 실패 → 15분 차단, 반복 시 30분→1h→2h→4h(상한).
func NewLimiter() *Limiter {
	return &Limiter{
		entry:      map[string]*entry{},
		MaxFailure: 10,
		Window:     15 * time.Minute,
		BlockFor:   15 * time.Minute,
		MaxBlock:   4 * time.Hour,
		timeNow:    time.Now,
	}
}

// KeyForIP는 IP 문자열을 limiter 키로 정규화한다.
// IPv6는 /64 prefix로 뭉갠다 — 단일 회선이 인터페이스 ID만 바꿔가며
// 카운트를 우회하는 것을 방지. IPv4(및 4-in-6)는 그대로.
func KeyForIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ipStr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
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
	// 윈도우 만료 + 차단 이력 없음 → 완전 리셋
	// (차단 이력이 있으면 blockCount 유지를 위해 엔트리를 남긴다)
	if now.Sub(e.windowStart) > l.Window && e.blockCount == 0 {
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
	if !ok {
		e = &entry{windowStart: now}
		l.entry[key] = e
	} else if now.Sub(e.windowStart) > l.Window {
		// 윈도우만 리셋 — blockCount(지수)는 유지
		e.failureCount = 0
		e.windowStart = now
	}
	e.failureCount++
	if e.failureCount >= l.MaxFailure {
		e.blockedUntil = now.Add(l.blockDuration(e.blockCount))
		e.blockCount++
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

// blockDuration은 n번째(0-base) 차단의 지속 시간 — BlockFor * 2^n, MaxBlock 상한.
func (l *Limiter) blockDuration(n int) time.Duration {
	d := l.BlockFor
	for i := 0; i < n; i++ {
		d *= 2
		if l.MaxBlock > 0 && d >= l.MaxBlock {
			return l.MaxBlock
		}
	}
	if l.MaxBlock > 0 && d > l.MaxBlock {
		return l.MaxBlock
	}
	return d
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
