-- 0004 alias: 추가 이메일 주소 (별칭) + 와일드카드 catch-all
--
-- 한 유저가 여러 주소로 메일을 받는다:
--   hello@krisam.in  → maro (정확 별칭)
--   *@kirby.so       → maro (catch-all — 그 도메인의 모든 미지정 주소)
--
-- 해석 우선순위 (ResolveAddress): 실제 유저 > 정확 별칭 > 와일드카드.
-- 발신(submission envelope from)도 별칭 주소 허용 (CanSendAs).

CREATE TABLE alias (
    id          BIGSERIAL PRIMARY KEY,
    domain_id   BIGINT NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part  TEXT NOT NULL,               -- 'hello' 또는 '*' (catch-all)
    account_id     BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);
CREATE INDEX idx_aliases_user ON alias(account_id);
