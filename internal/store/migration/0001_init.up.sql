-- 0001 initial schema (UUID edition) — the full final schema in one file.
--
-- History note: the schema originally grew through migrations 0001~0010 with
-- BIGSERIAL keys. When switching every entity key to UUID (2026-07) the data
-- was discarded by decision, so the steps were squashed into this single
-- fresh-start migration. Old step-by-step files live in git history.
--
-- Key conventions:
--   * every entity PK: uuid DEFAULT gen_random_uuid() (built-in since PG13)
--   * IMAP message UIDs stay integers on purpose — RFC 3501 mandates 32-bit
--     mailbox-scoped UIDs (uid, uid_next, uid_validity are protocol values,
--     not entity keys)
--   * greylist/message_flag/setting keep natural composite/text keys

-- ── multi-tenancy top level: mail domain ─────────────────────
CREATE TABLE domain (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL UNIQUE,        -- e.g. krisam.in
    active           BOOLEAN NOT NULL DEFAULT true,
    -- DKIM signing: public key is published at <selector>._domainkey.<name>.
    -- NULL selector = do not sign.
    dkim_selector    TEXT,
    dkim_private_key TEXT,                        -- PKCS#8 PEM (RSA or Ed25519)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── account = OIDC identity ──────────────────────────────────
-- Humans log in via OIDC (JIT provisioning keyed by sub); mail apps use app
-- passwords. Service accounts use a synthetic sub 'service:<email>' so the
-- web login path is structurally impossible.
CREATE TABLE account (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    oidc_subject  TEXT NOT NULL UNIQUE,           -- OIDC sub claim (identity key)
    oidc_email    TEXT NOT NULL,                  -- IdP email (display, refreshed on login)
    kind          TEXT NOT NULL DEFAULT 'user',   -- 'user' | 'service'
    quota_bytes   BIGINT,                         -- NULL = unlimited
    active        BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── address: account-owned mail addresses ────────────────────
-- local_part '*' is the domain catch-all. Resolution priority:
-- exact address > wildcard (*@domain).
CREATE TABLE address (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_id   UUID NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part  TEXT NOT NULL,                    -- 'krisamin' or '*' (catch-all)
    account_id  UUID NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);
CREATE INDEX idx_address_account ON address(account_id);

-- ── app passwords: IMAP/SMTP client auth (issued/revoked via web) ──
CREATE TABLE app_password (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id  UUID NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,                    -- 'Thunderbird laptop'
    hash        TEXT NOT NULL,                    -- argon2id
    scope_list  TEXT[] NOT NULL DEFAULT '{imap,smtp}',
    last_used   TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX idx_app_password_account ON app_password(account_id) WHERE revoked_at IS NULL;

-- ── raw bodies (inline in DB; object store is a future phase) ──
CREATE TABLE message_blob (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    content     BYTEA NOT NULL,                   -- RFC822 raw
    sha256      BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_message_blob_sha256 ON message_blob(sha256);

-- ── mailbox = IMAP folder (INBOX, Sent, ...) ─────────────────
CREATE TABLE mailbox (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    UUID NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,                  -- 'INBOX', 'Sent', 'Work/2026'
    uid_validity  BIGINT NOT NULL,                -- fixed at creation; changes on recreation
    uid_next      BIGINT NOT NULL DEFAULT 1,      -- next IMAP UID to assign
    subscribed    BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);

-- ── message metadata (raw body via blob reference) ───────────
CREATE TABLE message (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    mailbox_id    UUID NOT NULL REFERENCES mailbox(id) ON DELETE CASCADE,
    uid           BIGINT NOT NULL,                -- mailbox-scoped IMAP UID (RFC 3501)
    blob_id       UUID NOT NULL REFERENCES message_blob(id),
    size_bytes    BIGINT NOT NULL,
    internal_date TIMESTAMPTZ NOT NULL DEFAULT now(),  -- IMAP INTERNALDATE
    subject       TEXT,                           -- header cache (SEARCH/sorting)
    from_addr     TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uid)
);
CREATE INDEX idx_message_mailbox ON message(mailbox_id);

-- ── message flags (\Seen etc. + custom) ──────────────────────
CREATE TABLE message_flag (
    message_id  UUID NOT NULL REFERENCES message(id) ON DELETE CASCADE,
    flag        TEXT NOT NULL,                    -- '\Seen', '\Flagged', ...
    PRIMARY KEY (message_id, flag)
);

-- ── outbound queue: one row per recipient ────────────────────
-- Retries/failures are tracked independently per rcpt.
CREATE TABLE outbound_queue (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    envelope_from    TEXT NOT NULL,               -- MAIL FROM (bounce target)
    envelope_rcpt    TEXT NOT NULL,               -- RCPT TO (this row's destination)
    raw              BYTEA NOT NULL,              -- RFC822 raw (incl. Received)
    status           TEXT NOT NULL DEFAULT 'pending', -- pending|sent|failed|canceled
    attempt_count    INT NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_outbound_queue_due
    ON outbound_queue (next_attempt_at)
    WHERE status = 'pending';

-- ── outbound relay (Resend, SES, ...) ────────────────────────
-- Resolution: per-domain assignment → default relay → env fallback.
-- password is plaintext (single-server homelab; write-only via API).
CREATE TABLE relay (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,             -- 'resend', 'ses-backup', ...
    host        TEXT NOT NULL,                    -- smtp.resend.com
    port        INT NOT NULL DEFAULT 587,
    username    TEXT NOT NULL DEFAULT '',
    password    TEXT NOT NULL DEFAULT '',
    starttls    BOOLEAN NOT NULL DEFAULT true,
    is_default  BOOLEAN NOT NULL DEFAULT false,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_relay_default ON relay (is_default) WHERE is_default;

-- per-domain outbound relay assignment (NULL = default relay)
ALTER TABLE domain
    ADD COLUMN relay_id UUID REFERENCES relay(id) ON DELETE SET NULL;

-- ── global settings (key-value) ──────────────────────────────
-- First use: web display language (key='locale', value='auto|ko|en|ja').
CREATE TABLE setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── per-account mail filter rules ────────────────────────────
-- Evaluated in position order on INBOX-bound delivery (spam/DMARC quarantine
-- wins first); the first matching active rule applies its action.
CREATE TABLE filter_rule (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id     UUID NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    position       INT NOT NULL,
    name           TEXT NOT NULL,
    active         BOOLEAN NOT NULL DEFAULT true,
    -- condition: field(+header_name) match_type pattern (case-insensitive)
    field          TEXT NOT NULL,                 -- 'from'|'to'|'subject'|'header'
    header_name    TEXT NOT NULL DEFAULT '',      -- when field='header'
    match_type     TEXT NOT NULL,                 -- 'contains'|'equals'|'prefix'|'suffix'
    pattern        TEXT NOT NULL,
    -- action
    action         TEXT NOT NULL,                 -- 'move'|'markSeen'|'flag'|'discard'
    action_mailbox TEXT NOT NULL DEFAULT '',      -- when action='move'
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_filter_rule_account ON filter_rule (account_id, position);

-- ── greylisting triplets (natural composite key) ─────────────
-- First contact gets 451; a retry after the delay passes and the triplet is
-- then trusted (last_seen keeps refreshing). Stale rows are pruned inline.
CREATE TABLE greylist (
    source_net    TEXT NOT NULL,  -- /24 (IPv4) or /64 (IPv6) of the client
    envelope_from TEXT NOT NULL,  -- lowercased ('' = bounce sender <>)
    envelope_rcpt TEXT NOT NULL,  -- lowercased
    first_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen     TIMESTAMPTZ NOT NULL DEFAULT now(),
    pass_count    BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (source_net, envelope_from, envelope_rcpt)
);
CREATE INDEX idx_greylist_last_seen ON greylist (last_seen);
