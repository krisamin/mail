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

	// DKIM 서명 (Phase 2-4). Selector가 비어있으면 서명 안 함.
	// 공개키는 <selector>._domainkey.<name> TXT로 게시.
	DKIMSelector   string
	DKIMPrivateKey string // PKCS#8 PEM

	// 발신 relay 지정 (0005). nil = default relay 사용.
	RelayID *int64
}

// Relay는 외부 발송용 SMTP relay (Resend, SES, ...).
// 도메인별 지정(domain.relay_id) → default → env fallback 순으로 해석.
type Relay struct {
	ID        int64
	Name      string // 'resend' 등 표시명
	Host      string
	Port      int
	Username  string
	Password  string // 평문 (API로는 노출 금지 — 쓰기 전용)
	StartTLS  bool
	IsDefault bool
	Active    bool
	CreatedAt time.Time
}

// AccountKind 값 — account.kind 컬럼 (0007).
const (
	AccountKindUser    = "user"    // 사람 — OIDC 로그인 (JIT 프로비저닝)
	AccountKindService = "service" // 시스템 — 로그인 불가, 주소+앱비밀번호만
)

// Account는 유저 = OIDC 신원 (0006). 주소는 address 테이블에 별도.
// 사람 로그인은 OIDC(sub 기준 JIT 프로비저닝), 메일앱은 앱 비밀번호.
// 서비스 계정(0007)은 sub가 'service:<email>' 합성값이라 웹 로그인 불가.
type Account struct {
	ID          int64
	OIDCSubject string // OIDC sub 클레임 (유니크 — 진짜 신원 키)
	OIDCEmail   string // IdP가 내려준 email (참고/표시용, 로그인 시 갱신)
	Kind        string // AccountKindUser | AccountKindService
	QuotaBytes  *int64 // nil = 무제한
	Active      bool
	CreatedAt   time.Time
}

// Mailbox는 IMAP 폴더 (INBOX, Sent, ...).
type Mailbox struct {
	ID          int64
	AccountID   int64
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
	AccountID int64
	Label     string // 'Thunderbird 노트북'
	Hash      string // argon2id
	ScopeList []string
	LastUsed  *time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

// Address는 계정 소유의 메일 주소. local_part '*'는 도메인 catch-all.
// 유저의 모든 수신/발신 주소가 여기 산다 (0006 — 기존 계정주소+별칭 통합).
type Address struct {
	ID        int64
	DomainID  int64
	LocalPart string // '*' = 와일드카드 (그 도메인의 모든 미지정 주소)
	AccountID int64
	CreatedAt time.Time

	// 조회 편의 필드 (JOIN으로 채움)
	DomainName   string // 주소의 도메인 이름
	AccountEmail string // 소유 계정의 oidc_email (표시용)
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
	AttemptCount  int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
}

// MailboxStatus는 SELECT/STATUS가 요구하는 집계값.
type MailboxStatus struct {
	MessageCount uint32
	UnseenCount  uint32
	NumRecent    uint32
	UIDNext      uint32
	UIDValidity  uint32
}

// ── 인터페이스 ──────────────────────────────────────────────

// Store는 메일 저장 엔진의 최상위 인터페이스.
// Postgres 구현체가 이걸 만족한다. IMAP/SMTP 백엔드가 소비한다.
type Store interface {
	// 인증
	AuthenticateAppPassword(ctx context.Context, address, password string) (*Account, error)
	// FindAccountByAddress는 주소를 소유한 활성 계정을 찾는다 (정확 매칭만 —
	// 와일드카드 제외. IMAP/SMTP 로그인과 셀프서비스 매핑용).
	FindAccountByAddress(ctx context.Context, address string) (*Account, error)
	// FindAccountBySubject는 OIDC sub로 활성 계정을 찾는다 (웹 로그인 신원).
	FindAccountBySubject(ctx context.Context, subject string) (*Account, error)
	// ResolveAddress는 배달 대상 계정을 찾는다.
	// 우선순위: 정확 주소 > 와일드카드(*@domain).
	// SMTP 수신/submission의 로컬 배달이 이걸 쓴다.
	ResolveAddress(ctx context.Context, address string) (*Account, error)
	// CanSendAs는 계정이 해당 주소로 발신 가능한지 (소유 주소 —
	// 와일드카드 주소 포함).
	CanSendAs(ctx context.Context, accountID int64, address string) (bool, error)

	// 도메인
	// FindDomain은 활성 도메인을 이름으로 찾는다. 수신/제출 시
	// "우리 도메인인가"(로컬 배달 대상) 판단에 쓴다.
	FindDomain(ctx context.Context, name string) (*Domain, error)

	// 메일박스
	ListMailbox(ctx context.Context, accountID int64) ([]*Mailbox, error)
	GetMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error)
	CreateMailbox(ctx context.Context, accountID int64, name string) (*Mailbox, error)
	DeleteMailbox(ctx context.Context, accountID int64, name string) error
	RenameMailbox(ctx context.Context, accountID int64, name, newName string) error
	SetSubscribed(ctx context.Context, mailboxID int64, subscribed bool) error
	MailboxStatus(ctx context.Context, mailboxID int64) (*MailboxStatus, error)

	// 메시지
	AppendMessage(ctx context.Context, mailboxID int64, raw []byte, flagList []string, internalDate time.Time) (*Message, error)
	ListMessage(ctx context.Context, mailboxID int64) ([]*Message, error)
	GetMessageBlob(ctx context.Context, messageID int64) ([]byte, error)
	SetFlag(ctx context.Context, messageID int64, flagList []string) error
	// ExpungeDeleted는 \Deleted 메시지를 실제 삭제한다.
	// uids가 nil이면 전체, 아니면 해당 UID들만 (IMAP UID EXPUNGE 대응).
	ExpungeDeleted(ctx context.Context, mailboxID int64, uids []uint32) ([]uint32, error)
	CopyMessage(ctx context.Context, messageID, destMailboxID int64) (*Message, error)

	// 발송 큐 (Phase 2-3)
	// EnqueueOutbound는 수신자별로 발송 항목을 큐에 넣는다.
	EnqueueOutbound(ctx context.Context, from string, rcptList []string, raw []byte) error
	// DueOutbound는 발송 시각이 지난 pending 항목을 최대 limit개 가져온다.
	// FOR UPDATE SKIP LOCKED 의미론 — 여러 워커가 떠도 같은 행을 안 잡는다.
	DueOutbound(ctx context.Context, limit int) ([]*OutboundMessage, error)
	// MarkOutboundSent는 발송 성공 처리.
	MarkOutboundSent(ctx context.Context, id int64) error
	// MarkOutboundRetry는 실패 기록 + 다음 시도 시각 설정. attempts는 증가.
	MarkOutboundRetry(ctx context.Context, id int64, errMsg string, nextAttempt time.Time) error
	// MarkOutboundFailed는 영구 실패 처리 (재시도 소진).
	MarkOutboundFailed(ctx context.Context, id int64, errMsg string) error

	// ResolveRelay는 발신 도메인 이름으로 사용할 relay를 찾는다.
	// 도메인 지정 relay → default relay → ErrNotFound (호출자가 env fallback).
	// 비활성 relay는 무시한다.
	ResolveRelay(ctx context.Context, senderDomain string) (*Relay, error)
}

// AdminStore는 관리 플레인(Admin API)이 쓰는 확장 인터페이스 (Phase 3).
// 프로토콜 경로(Store)와 분리해 서로의 표면적을 좁게 유지한다.
type AdminStore interface {
	Store

	// 도메인
	ListDomain(ctx context.Context) ([]*Domain, error)
	CreateDomain(ctx context.Context, name string) (*Domain, error)
	// BackfillDomainAddress는 oidc_email이 이 도메인인 기존 사람 계정에
	// primary 주소+INBOX를 소급 생성한다 (멱등). 생성 수 반환.
	BackfillDomainAddress(ctx context.Context, domainID int64) (int, error)
	SetDomainActive(ctx context.Context, id int64, active bool) error
	// SetDomainDKIM은 DKIM selector/개인키를 설정한다 (빈 문자열 = 해제).
	SetDomainDKIM(ctx context.Context, id int64, selector, privateKeyPEM string) error

	// 계정 (유저 = OIDC 신원. 사람 계정 생성은 JIT 프로비저닝만)
	ListAccount(ctx context.Context) ([]*Account, error)
	// ProvisionAccount는 OIDC sub 기준 JIT 프로비저닝 — 계정이 없으면
	// 만들고, 있으면 oidc_email만 갱신해 돌려준다 (멱등).
	// email 도메인이 등록돼 있으면 primary 주소+INBOX까지, 미등록이면
	// 계정만 생성 (주소 없음 = 메일 사용 불가, 도메인 추가 시 backfill).
	ProvisionAccount(ctx context.Context, subject, email string) (*Account, error)
	// CreateServiceAccount는 서비스 계정을 만든다 (admin 전용) —
	// 로그인 불가, 주소+앱비밀번호만. email 주소가 primary로 등록된다.
	CreateServiceAccount(ctx context.Context, email string) (*Account, error)
	SetAccountActive(ctx context.Context, id int64, active bool) error

	// 앱 비밀번호 (DD-02: OAuth 로그인 후 발급)
	ListAppPassword(ctx context.Context, accountID int64) ([]*AppPassword, error)
	// CreateAppPassword는 해시를 저장하고 레코드를 돌려준다.
	// 평문 생성은 호출자(API 레이어) 책임 — 발급 시 1회만 노출.
	CreateAppPassword(ctx context.Context, accountID int64, label, hash string) (*AppPassword, error)
	RevokeAppPassword(ctx context.Context, id int64) error

	// 주소 (계정 소유 메일 주소 + 와일드카드 — admin만 추가/삭제)
	ListAddress(ctx context.Context, domainID int64) ([]*Address, error)
	ListAccountAddress(ctx context.Context, accountID int64) ([]*Address, error)
	// CreateAddress는 localPart '*'를 catch-all로 취급한다.
	CreateAddress(ctx context.Context, domainID int64, localPart string, accountID int64) (*Address, error)
	// DeleteAddress는 주소를 지운다. 계정의 마지막 일반 주소는 지울 수 없다
	// (수신/로그인 매핑이 사라지는 것 방지).
	DeleteAddress(ctx context.Context, id int64) error

	// 발송 큐 관리
	ListOutbound(ctx context.Context, status string, limit int) ([]*OutboundMessage, error)
	// RetryOutbound는 failed 항목을 pending으로 되돌린다 (즉시 due).
	RetryOutbound(ctx context.Context, id int64) error
	// OutboundStat는 상태별 건수.
	OutboundStat(ctx context.Context) (map[string]int64, error)

	// relay (0005) — password는 쓰기 전용 (List가 돌려주는 값도 API 레이어에서 마스킹)
	ListRelay(ctx context.Context) ([]*Relay, error)
	CreateRelay(ctx context.Context, r *Relay) (*Relay, error)
	// UpdateRelay는 password가 빈 문자열이면 기존 값 유지.
	UpdateRelay(ctx context.Context, r *Relay) (*Relay, error)
	DeleteRelay(ctx context.Context, id int64) error
	// SetDomainRelay는 도메인 발신 relay 지정 (nil = default 사용).
	SetDomainRelay(ctx context.Context, domainID int64, relayID *int64) error

	// 전역 설정 (0008) — key-value. 첫 용도는 웹 표시 언어(key='locale').
	// GetSetting은 없는 키에 store.ErrNotFound를 돌려준다.
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}
