-- 0009: per-account mail filter rules.
-- Evaluated in position order on delivery to INBOX (spam/DMARC quarantine
-- wins first) — the first matching active rule applies its action.
CREATE TABLE filter_rule (
    id             BIGSERIAL PRIMARY KEY,
    account_id     BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    position       INT NOT NULL,
    name           TEXT NOT NULL,
    active         BOOLEAN NOT NULL DEFAULT true,
    -- condition: field(+header_name) match_type pattern (case-insensitive)
    field          TEXT NOT NULL,             -- 'from' | 'to' | 'subject' | 'header'
    header_name    TEXT NOT NULL DEFAULT '',  -- when field='header'
    match_type     TEXT NOT NULL,             -- 'contains' | 'equals' | 'prefix' | 'suffix'
    pattern        TEXT NOT NULL,
    -- action
    action         TEXT NOT NULL,             -- 'move' | 'markSeen' | 'flag' | 'discard'
    action_mailbox TEXT NOT NULL DEFAULT '',  -- when action='move'
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX filter_rule_account_idx ON filter_rule (account_id, position);
