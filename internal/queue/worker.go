// Package queue는 발송 큐 워커를 구현한다 (Phase 2-3).
//
// submission이 외부 도메인 수신자를 store의 outbound_queue에 넣으면,
// 워커가 주기적으로 due 항목을 꺼내 Sender로 발송한다.
//
// ★발송 정책 (DD-04): 직접 MX 발송은 하지 않는다. Sender 구현은
// SMTP relay(SES/Postmark 등) 경유가 기본 — relay 선택은 아직 미정이라
// Sender 인터페이스 뒤로 추상화해뒀다. 설정만 채우면 붙는다.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/krisamin/mail/internal/store"
)

// Sender는 한 통을 실제로 내보내는 책임. 구현:
//   - RelaySender: SMTP relay 경유 (프로덕션 기본)
//   - 테스트: mock
type Sender interface {
	// Send는 envelope from/rcpt와 원문으로 발송한다.
	// PermanentError를 돌려주면 재시도 없이 즉시 실패 처리된다.
	Send(ctx context.Context, from, rcpt string, raw []byte) error
}

// PermanentError는 재시도해도 소용없는 실패 (5xx 등).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Config는 워커 동작 파라미터.
type Config struct {
	// PollInterval은 due 스캔 주기. 기본 10초.
	PollInterval time.Duration
	// BatchSize는 한 번에 가져올 최대 항목 수. 기본 10.
	BatchSize int
	// MaxAttempts를 넘으면 영구 실패. 기본 6 (백오프 합계 ≈ 2시간).
	MaxAttempts int
	// BaseBackoff는 지수 백오프의 밑. 기본 1분: 1m→2m→4m→8m→16m→32m.
	BaseBackoff time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = 10 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 10
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 6
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Minute
	}
	return c
}

// Worker는 발송 큐를 소비한다.
type Worker struct {
	store  store.Store
	sender Sender
	cfg    Config
}

// NewWorker는 워커를 만든다.
func NewWorker(st store.Store, sender Sender, cfg Config) *Worker {
	return &Worker{store: st, sender: sender, cfg: cfg.withDefaults()}
}

// Run은 ctx가 취소될 때까지 주기적으로 큐를 처리한다.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	log.Printf("queue: 발송 워커 시작 (poll=%s batch=%d maxAttempts=%d)",
		w.cfg.PollInterval, w.cfg.BatchSize, w.cfg.MaxAttempts)
	for {
		select {
		case <-ctx.Done():
			log.Printf("queue: 발송 워커 종료")
			return
		case <-ticker.C:
			if n, err := w.ProcessOnce(ctx); err != nil {
				log.Printf("queue: 처리 오류: %v", err)
			} else if n > 0 {
				log.Printf("queue: %d건 처리", n)
			}
		}
	}
}

// ProcessOnce는 due 항목 한 배치를 처리하고 처리 건수를 돌려준다.
// (테스트와 Run 루프가 공유하는 단위)
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	due, err := w.store.DueOutbound(ctx, w.cfg.BatchSize)
	if err != nil {
		return 0, fmt.Errorf("due 조회: %w", err)
	}

	for _, m := range due {
		w.processMessage(ctx, m)
	}
	return len(due), nil
}

func (w *Worker) processMessage(ctx context.Context, m *store.OutboundMessage) {
	err := w.sender.Send(ctx, m.EnvelopeFrom, m.EnvelopeRcpt, m.Raw)
	if err == nil {
		if err := w.store.MarkOutboundSent(ctx, m.ID); err != nil {
			log.Printf("queue: sent 마킹 실패 id=%d: %v", m.ID, err)
		}
		log.Printf("queue: 발송 완료 id=%d to=%s (attempt %d)", m.ID, m.EnvelopeRcpt, m.Attempts+1)
		return
	}

	// 영구 오류 또는 재시도 소진 → failed
	var perm *PermanentError
	if errors.As(err, &perm) || m.Attempts+1 >= w.cfg.MaxAttempts {
		if merr := w.store.MarkOutboundFailed(ctx, m.ID, err.Error()); merr != nil {
			log.Printf("queue: failed 마킹 실패 id=%d: %v", m.ID, merr)
		}
		log.Printf("queue: 영구 실패 id=%d to=%s: %v (attempt %d/%d)",
			m.ID, m.EnvelopeRcpt, err, m.Attempts+1, w.cfg.MaxAttempts)
		// TODO(Phase 2-3 후속): 발신자에게 bounce(DSN) 메일 생성
		return
	}

	// 일시 오류 → 지수 백오프 재시도
	backoff := w.cfg.BaseBackoff << m.Attempts // 1m, 2m, 4m, ...
	next := time.Now().Add(backoff)
	if merr := w.store.MarkOutboundRetry(ctx, m.ID, err.Error(), next); merr != nil {
		log.Printf("queue: retry 마킹 실패 id=%d: %v", m.ID, merr)
	}
	log.Printf("queue: 재시도 예약 id=%d to=%s in %s: %v (attempt %d/%d)",
		m.ID, m.EnvelopeRcpt, backoff, err, m.Attempts+1, w.cfg.MaxAttempts)
}
