-- 0001 initial schema: 멀티테넌트 메일 저장 엔진의 뼈대
-- IMAP(go-imap v2 imapserver.Session) 요구사항에서 역산한 스키마.

-- ── 멀티테넌시 최상위: 메일 도메인 ──────────────────────────
CREATE TABLE domain (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,         -- 예: krisam.in
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── 계정: local_part@domain ────────────────────────────────
-- 사람 로그인은 OIDC(oidc_subject), 메일앱은 app_passwords로 인증.
CREATE TABLE account (
    id            BIGSERIAL PRIMARY KEY,
    domain_id     BIGINT NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part    TEXT NOT NULL,              -- 'maro' (in maro@krisam.in)
    oidc_subject  TEXT,                        -- OIDC sub 클레임 (nullable)
    quota_bytes   BIGINT,                      -- NULL = 무제한
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);

-- ── 앱 비밀번호: IMAP/SMTP 클라이언트 인증 (OAuth로 발급/revoke) ──
CREATE TABLE app_password (
    id          BIGSERIAL PRIMARY KEY,
    account_id     BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,                -- 'Thunderbird 노트북'
    hash        TEXT NOT NULL,                -- argon2id
    scope_list      TEXT[] NOT NULL DEFAULT '{imap,smtp}',
    last_used   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX idx_app_passwords_user ON app_password(account_id) WHERE revoked_at IS NULL;

-- ── 원문 본문 (Phase 1: DB 인라인 / Phase 4: 오브젝트 스토어로 이동) ──
CREATE TABLE message_blob (
    id          BIGSERIAL PRIMARY KEY,
    content     BYTEA NOT NULL,               -- RFC822 raw
    sha256      BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_message_blobs_sha256 ON message_blob(sha256);

-- ── 메일박스 = IMAP 폴더 (INBOX, Sent, ...) ─────────────────
CREATE TABLE mailbox (
    id            BIGSERIAL PRIMARY KEY,
    account_id       BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,              -- 'INBOX', 'Sent', 'Work/2026'
    uid_validity  BIGINT NOT NULL,            -- 생성 시 고정. 재생성되면 바뀜
    uid_next      BIGINT NOT NULL DEFAULT 1,  -- 다음 부여할 UID
    subscribed    BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);

-- ── 메시지 메타데이터 (본문은 blob 참조) ────────────────────
CREATE TABLE message (
    id            BIGSERIAL PRIMARY KEY,
    mailbox_id    BIGINT NOT NULL REFERENCES mailbox(id) ON DELETE CASCADE,
    uid           BIGINT NOT NULL,            -- 메일박스 스코프 UID
    blob_id       BIGINT NOT NULL REFERENCES message_blob(id),
    size_bytes    BIGINT NOT NULL,
    internal_date TIMESTAMPTZ NOT NULL DEFAULT now(),  -- IMAP INTERNALDATE
    subject       TEXT,                        -- 헤더 캐시 (SEARCH/정렬용)
    from_addr     TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uid)
);
CREATE INDEX idx_messages_mailbox ON message(mailbox_id);

-- ── 메시지 플래그 (\Seen 등 + 커스텀) ──────────────────────
CREATE TABLE message_flag (
    message_id  BIGINT NOT NULL REFERENCES message(id) ON DELETE CASCADE,
    flag        TEXT NOT NULL,                -- '\Seen', '\Flagged', ...
    PRIMARY KEY (message_id, flag)
);
