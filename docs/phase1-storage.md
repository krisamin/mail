# Phase 1 — 저장 엔진 설계

목표: go-imap v2의 `imapserver.Session` 인터페이스를 우리 PostgreSQL 스토어로
구현한다. IMAP이 요구하는 개념(mailbox, UID, UIDVALIDITY, flags)이 그대로
스키마가 된다.

## IMAP 백엔드가 요구하는 것 (인터페이스에서 역산)

```
Login                                    → account (인증)
Select/Create/Delete/Rename/List/Status  → mailbox (폴더)
Subscribe/Unsubscribe                    → mailbox.subscribed
Append/Fetch/Store/Copy/Expunge/Search   → message
Poll/Idle                                → 변경 통지 (Phase 1에선 최소 구현)
```

## 핵심 IMAP 개념 → 스키마 매핑

- **UID**: 메일박스 내 메시지의 영구 식별자. 단조 증가, 재사용 안 함.
  → `message.uid` (메일박스 스코프). 삭제해도 다음 UID는 계속 커진다.
- **UIDNext**: 다음에 부여될 UID. → `mailbox.uid_next` (Append마다 ++).
- **UIDVALIDITY**: 메일박스 "세대" 번호. 메일박스가 삭제/재생성되면 바뀌어서
  클라이언트가 캐시를 무효화하게 함. → `mailbox.uid_validity` (생성 시 고정).
- **Flags**: `\Seen \Answered \Flagged \Deleted \Draft` + 커스텀.
  → `message_flag` (message_id, flag) — 다대다. 또는 messages에 배열.
- **EXISTS/UNSEEN**: 카운트. → message 집계 쿼리로 계산.

## 스키마 (초안)

```sql
-- 멀티테넌시 최상위: 메일 도메인
CREATE TABLE domain (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,        -- 예: krisam.in
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 계정 = 메일박스 소유자 (user@domain)
CREATE TABLE account (
    id            BIGSERIAL PRIMARY KEY,
    domain_id     BIGINT NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part    TEXT NOT NULL,             -- 'maro' (in maro@krisam.in)
    -- 인증: 사람은 OIDC(external), 메일앱은 앱 비밀번호(아래 테이블)
    oidc_subject  TEXT,                       -- OIDC sub 클레임 (사람 로그인)
    quota_bytes   BIGINT,                     -- NULL = 무제한
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);

-- 앱 비밀번호 (IMAP/SMTP 클라이언트 인증, OAuth로 발급/revoke)
CREATE TABLE app_password (
    id          BIGSERIAL PRIMARY KEY,
    account_id     BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,               -- 'Thunderbird 노트북'
    hash        TEXT NOT NULL,               -- argon2id
    scopes      TEXT[] NOT NULL DEFAULT '{imap,smtp}',
    last_used   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);

-- 메일박스 = IMAP 폴더 (INBOX, Sent, Drafts, ...)
CREATE TABLE mailbox (
    id            BIGSERIAL PRIMARY KEY,
    account_id       BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,             -- 'INBOX', 'Sent', 'Work/2026'
    uid_validity  BIGINT NOT NULL,           -- 생성 시 고정 (epoch 등)
    uid_next      BIGINT NOT NULL DEFAULT 1, -- 다음 부여할 UID
    subscribed    BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);

-- 메시지 메타데이터 (본문은 blob 참조)
CREATE TABLE message (
    id            BIGSERIAL PRIMARY KEY,
    mailbox_id    BIGINT NOT NULL REFERENCES mailbox(id) ON DELETE CASCADE,
    uid           BIGINT NOT NULL,           -- 메일박스 스코프 UID
    -- 본문 저장 참조 (Phase 1은 DB 인라인, 나중에 오브젝트 스토어로)
    blob_id       BIGINT REFERENCES message_blob(id),
    size_bytes    BIGINT NOT NULL,
    internal_date TIMESTAMPTZ NOT NULL DEFAULT now(),  -- IMAP INTERNALDATE
    -- 자주 쓰는 헤더 캐시 (SEARCH/정렬 최적화)
    subject       TEXT,
    from_addr     TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uid)
);

-- 메시지 플래그 (\Seen 등 + 커스텀)
CREATE TABLE message_flag (
    message_id  BIGINT NOT NULL REFERENCES message(id) ON DELETE CASCADE,
    flag        TEXT NOT NULL,               -- '\Seen', '\Flagged', ...
    PRIMARY KEY (message_id, flag)
);

-- 원문 본문 (Phase 1: DB 저장 / Phase 4: 오브젝트 스토어로 이동)
CREATE TABLE message_blob (
    id          BIGSERIAL PRIMARY KEY,
    content     BYTEA NOT NULL,              -- RFC822 raw
    sha256      BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## 설계 노트

- **본문 저장 전략**: Phase 1은 단순하게 `message_blob.content BYTEA`로 DB에 인라인.
  Phase 4에서 MinIO/S3로 분리 (blob_id는 이미 참조라 마이그레이션 쉬움).
- **UID 발급**: Append 시 `mailbox.uid_next`를 트랜잭션으로 읽고 ++. 동시성은
  row lock 또는 sequence.
- **flags 다대다 vs 배열**: 다대다(message_flag)로 시작. SEARCH 쿼리가 깔끔.
  성능 이슈 나면 `TEXT[]` + GIN 인덱스로 전환 검토.
- **헤더 캐시**: subject/from_addr는 SEARCH·정렬 자주 하니 messages에 비정규화.
  전체 헤더는 blob에서 파싱.
- **INBOX 자동 생성**: 유저 생성 시 INBOX 메일박스 자동 생성 (IMAP 필수 폴더).

## Phase 1 작업 순서

1. store 패키지: 인터페이스 정의 (`Store`, `Mailbox`, `Message` 도메인 타입)
2. Postgres 마이그레이션 (위 스키마) — golang-migrate 또는 순수 SQL
3. store의 Postgres 구현체
4. imapserver.Session을 store 위에서 구현 (Login/Select/List/Fetch/Append 우선)
5. 로컬 Postgres(도커) 띄우고 Thunderbird로 붙어서 INBOX 보기 검증
