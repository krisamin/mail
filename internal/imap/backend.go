// Package imap은 store.Store 위에서 go-imap v2 imapserver.Session을 구현한다.
//
// 프로토콜 상태머신(명령 파싱, 리터럴, 응답 인코딩)은 go-imap이 담당하고,
// 여기서는 "어떤 데이터를 돌려줄 것인가"만 채운다 (DD-01 2계층 아키텍처).
//
// ★Phase 1 동시성 모델 — 세션 스냅샷:
// SELECT 시점에 메일박스의 UID 목록을 세션 메모리에 스냅샷으로 뜬다.
// 시퀀스 번호 = 스냅샷 인덱스+1. 다른 세션이 만든 변경(신규 메일, expunge)은
// Poll/Idle에서 스냅샷과 DB를 비교해 반영한다. 세션 간 실시간 push는
// Phase 2+에서 Postgres LISTEN/NOTIFY로 붙일 예정.
package imap

import (
	"context"
	"net"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/krisamin/mail/internal/guard"
	"github.com/krisamin/mail/internal/store"
)

// opTimeout은 IMAP 명령 하나가 store에 접근할 때의 상한.
const opTimeout = 30 * time.Second

// Backend는 store를 감싸는 IMAP 세션 팩토리.
type Backend struct {
	store   store.Store
	limiter *guard.Limiter // 인증 브루트포스 방어 (IP 단위)
}

// NewBackend는 store 위에 IMAP 백엔드를 만든다.
func NewBackend(st store.Store) *Backend {
	return &Backend{store: st, limiter: guard.NewLimiter()}
}

// NewSession은 imapserver.Options.NewSession에 꽂는 콜백.
func (b *Backend) NewSession(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
	remoteIP := ""
	if c != nil && c.NetConn() != nil {
		if host, _, err := net.SplitHostPort(c.NetConn().RemoteAddr().String()); err == nil {
			remoteIP = host
		}
	}
	return &Session{backend: b, remoteIP: remoteIP}, nil, nil
}

// opCtx는 명령 단위 컨텍스트를 만든다.
func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}
