package postgres

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"
)

// Notifier — Postgres LISTEN/NOTIFY 기반 메일박스 변경 알림 허브.
//
// AppendMessage/ExpungeDeleted가 커밋 시 pg_notify('mailbox_change', <id>)를
// 쏘고, Notifier가 전용 커넥션으로 LISTEN해서 메일박스별 구독자(IMAP IDLE
// 세션)에게 브로드캐스트한다. 15초 폴링 대신 즉시 push — 폴링은 구독이
// 없거나 알림이 유실된 경우의 폴백으로만 남는다.
//
// 단일 레플리카 전제(현 배포 구조)라 프로세스 내 허브로 충분하지만,
// LISTEN/NOTIFY는 DB를 경유하므로 멀티 레플리카가 돼도 그대로 동작한다.

const notifyChannel = "mailbox_change"

// Notifier는 메일박스 변경 구독 허브.
type Notifier struct {
	store *Store

	mu     sync.Mutex
	subMap map[int64]map[chan struct{}]bool // mailboxID → 구독자 집합
}

// NewNotifier는 허브를 만든다. Run을 별도 고루틴으로 돌려야 동작한다.
func NewNotifier(st *Store) *Notifier {
	return &Notifier{store: st, subMap: map[int64]map[chan struct{}]bool{}}
}

// Subscribe는 메일박스 변경 채널을 돌려준다. 두 번째 반환값은 해지 함수 —
// IDLE 종료 시 반드시 호출할 것 (누수 방지).
func (n *Notifier) Subscribe(mailboxID int64) (<-chan struct{}, func()) {
	// 버퍼 1 — 알림 폭주 시 coalesce (IDLE은 "변했다" 신호만 필요)
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	if n.subMap[mailboxID] == nil {
		n.subMap[mailboxID] = map[chan struct{}]bool{}
	}
	n.subMap[mailboxID][ch] = true
	n.mu.Unlock()

	cancel := func() {
		n.mu.Lock()
		delete(n.subMap[mailboxID], ch)
		if len(n.subMap[mailboxID]) == 0 {
			delete(n.subMap, mailboxID)
		}
		n.mu.Unlock()
	}
	return ch, cancel
}

// dispatch는 해당 메일박스 구독자 전원에게 non-blocking 신호를 보낸다.
func (n *Notifier) dispatch(mailboxID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subMap[mailboxID] {
		select {
		case ch <- struct{}{}:
		default: // 이미 신호 대기 중 — coalesce
		}
	}
}

// Run은 ctx가 취소될 때까지 LISTEN 루프를 돈다. 커넥션이 끊기면
// 백오프 후 재연결한다 (그 사이 알림 유실은 IDLE 폴백 폴링이 흡수).
func (n *Notifier) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := n.listenOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("notify: LISTEN 끊김 (재연결 %s 후): %v", backoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (n *Notifier) listenOnce(ctx context.Context) error {
	// LISTEN은 커넥션 단위 — 풀에서 한 개를 점유 유지한다.
	conn, err := n.store.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		return err
	}
	log.Printf("notify: LISTEN %s 시작", notifyChannel)

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		id, err := strconv.ParseInt(notification.Payload, 10, 64)
		if err != nil {
			continue
		}
		n.dispatch(id)
	}
}
