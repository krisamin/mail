// Package store는 메일 저장 엔진의 도메인 타입과 인터페이스를 정의한다.
//
// 여기엔 SQL도 IMAP도 없다 — 순수 도메인. Postgres 구현체(store/postgres)나
// IMAP 백엔드(internal/imap)가 이 인터페이스에 의존한다. 덕분에 나중에
// 본문 저장을 DB→S3로 바꾸거나 테스트용 in-memory 구현을 끼우기 쉽다.
package store

import (
	"context"
	"errors"
	"time"
)

// ── 에러 ────────────────────────────────────────────────────

// ErrNotFound는 조회 대상이 없을 때. 구현체가 이 sentinel을 반환해야
// 소비자(IMAP 백엔드 등)가 구현체 패키지를 import하지 않고 분기할 수 있다.
var ErrNotFound = errors.New("not found")

// ErrAuthFailed는 인증 실패.
var ErrAuthFailed = errors.New("authentication failed")

// ── 도메인 타입 ─────────────────────────────────────────────

// Domain은 메일 도메인 (멀티테넌시 최상위). 예: krisam.in
type Domain struct {
	ID        int64
	Name      string
	Active    bool
	CreatedAt time.Time
}

// User는 계정 (local_part@domain). 사람 로그인은 OIDC, 메일앱은 앱 비밀번호.
type User struct {
	ID          int64
	DomainID    int64
	LocalPart   string // 'maro' (in maro@krisam.in)
	OIDCSubject string // OIDC sub 클레임 (비어있을 수 있음)
	QuotaBytes  *int64 // nil = 무제한
	Active      bool
	CreatedAt   time.Time
}

// Mailbox는 IMAP 폴더 (INBOX, Sent, ...).
type Mailbox struct {
	ID          int64
	UserID      int64
	Name        string
	UIDValidity uint32 // 생성 시 고정. 재생성되면 바뀜 → 클라 캐시 무효화
	UIDNext     uint32 // 다음 부여할 UID
	Subscribed  bool
	CreatedAt   time.Time
}

// Message는 메일박스 내 메시지 메타데이터. 원문 본문은 BlobID로 참조.
type Message struct {
	ID           int64
	MailboxID    int64
	UID          uint32 // 메일박스 스코프. 단조 증가, 재사용 안 함
	BlobID       int64
	SizeBytes    int64
	InternalDate time.Time // IMAP INTERNALDATE
	Subject      string    // 헤더 캐시 (SEARCH/정렬용)
	FromAddr     string
	Flags        []string // '\Seen', '\Flagged', ...
	CreatedAt    time.Time
}

// AppPassword는 메일앱(IMAP/SMTP) 인증용 앱 비밀번호. OAuth로 발급/revoke.
type AppPassword struct {
	ID        int64
	UserID    int64
	Label     string // 'Thunderbird 노트북'
	Hash      string // argon2id
	Scopes    []string
	LastUsed  *time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

// OutboundStatus는 발송 큐 항목의 상태.
const (
	OutboundPending = "pending" // 발송 대기 (재시도 포함)
	OutboundSent    = "sent"    // 발송 완료
	OutboundFailed  = "failed"  // 영구 실패 (bounce 대상)
)

// OutboundMessage는 발송 큐의 한 항목. 수신자(rcpt) 단위 —
// 재시도/실패를 수신자별로 독립 추적한다.
type OutboundMessage struct {
	ID            int64
	EnvelopeFrom  string
	EnvelopeRcpt  string
	Raw           []byte
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// MailboxStatus는 SELECT/STATUS가 요구하는 집계값.
type MailboxStatus struct {
	NumMessages uint32
	NumUnseen   uint32
	NumRecent   uint32
	UIDNext     uint32
	UIDValidity uint32
}

// ── 인터페이스 ──────────────────────────────────────────────

// Store는 메일 저장 엔진의 최상위 인터페이스.
// Postgres 구현체가 이걸 만족한다. IMAP/SMTP 백엔드가 소비한다.
type Store interface {
	// 인증
	AuthenticateAppPassword(ctx context.Context, address, password string) (*User, error)
	FindUserByAddress(ctx context.Context, address string) (*User, error)

	// 도메인
	// FindDomain은 활성 도메인을 이름으로 찾는다. 수신/제출 시
	// "우리 도메인인가"(로컬 배달 대상) 판단에 쓴다.
	FindDomain(ctx context.Context, name string) (*Domain, error)

	// 메일박스
	ListMailboxes(ctx context.Context, userID int64) ([]*Mailbox, error)
	GetMailbox(ctx context.Context, userID int64, name string) (*Mailbox, error)
	CreateMailbox(ctx context.Context, userID int64, name string) (*Mailbox, error)
	DeleteMailbox(ctx context.Context, userID int64, name string) error
	RenameMailbox(ctx context.Context, userID int64, name, newName string) error
	SetSubscribed(ctx context.Context, mailboxID int64, subscribed bool) error
	MailboxStatus(ctx context.Context, mailboxID int64) (*MailboxStatus, error)

	// 메시지
	AppendMessage(ctx context.Context, mailboxID int64, raw []byte, flags []string, internalDate time.Time) (*Message, error)
	ListMessages(ctx context.Context, mailboxID int64) ([]*Message, error)
	GetMessageBlob(ctx context.Context, messageID int64) ([]byte, error)
	SetFlags(ctx context.Context, messageID int64, flags []string) error
	// ExpungeDeleted는 \Deleted 메시지를 실제 삭제한다.
	// uids가 nil이면 전체, 아니면 해당 UID들만 (IMAP UID EXPUNGE 대응).
	ExpungeDeleted(ctx context.Context, mailboxID int64, uids []uint32) ([]uint32, error)
	CopyMessage(ctx context.Context, messageID, destMailboxID int64) (*Message, error)

	// 발송 큐 (Phase 2-3)
	// EnqueueOutbound는 수신자별로 발송 항목을 큐에 넣는다.
	EnqueueOutbound(ctx context.Context, from string, rcpts []string, raw []byte) error
	// DueOutbound는 발송 시각이 지난 pending 항목을 최대 limit개 가져온다.
	// FOR UPDATE SKIP LOCKED 의미론 — 여러 워커가 떠도 같은 행을 안 잡는다.
	DueOutbound(ctx context.Context, limit int) ([]*OutboundMessage, error)
	// MarkOutboundSent는 발송 성공 처리.
	MarkOutboundSent(ctx context.Context, id int64) error
	// MarkOutboundRetry는 실패 기록 + 다음 시도 시각 설정. attempts는 증가.
	MarkOutboundRetry(ctx context.Context, id int64, errMsg string, nextAttempt time.Time) error
	// MarkOutboundFailed는 영구 실패 처리 (재시도 소진).
	MarkOutboundFailed(ctx context.Context, id int64, errMsg string) error
}
