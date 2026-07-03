-- 0002 outbound queue: 발송 큐 (Phase 2-3)
-- 수신자(rcpt) 단위로 한 행 — 재시도/실패 상태를 수신자별로 독립 추적한다.

CREATE TABLE outbound_queue (
    id               BIGSERIAL PRIMARY KEY,
    envelope_from    TEXT NOT NULL,             -- MAIL FROM (bounce 수신처)
    envelope_rcpt    TEXT NOT NULL,             -- RCPT TO (이 행의 목적지)
    raw              BYTEA NOT NULL,            -- RFC822 원문 (Received 포함)
    status           TEXT NOT NULL DEFAULT 'pending',  -- pending | sent | failed
    attempts         INT NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 워커의 due 스캔용 (pending만, next_attempt_at 순)
CREATE INDEX idx_outbound_queue_due
    ON outbound_queue (next_attempt_at)
    WHERE status = 'pending';
