-- 0006 account identity: 유저 = OIDC 신원, 주소는 유저 아래 목록
--
-- 기존 모델: account = local_part@domain (주소가 곧 계정), alias가 부속.
-- 새 모델:   account = OIDC 신원(sub), address가 계정 아래 주소 목록.
--
--   account (oidc_subject 유니크)
--   └─ address[]  ← 기존 account 주소 + alias + catch-all(*) 전부 통합
--
-- 해석 우선순위 (ResolveAddress): 정확 주소 > 와일드카드(*@domain).
-- 계정 생성은 JIT 프로비저닝(첫 OIDC 로그인)으로만 — admin은 주소만 관리.

-- 1) account에 신원 컬럼 추가
ALTER TABLE account ADD COLUMN oidc_email TEXT;

-- 2) address 테이블 (alias 승격)
CREATE TABLE address (
    id          BIGSERIAL PRIMARY KEY,
    domain_id   BIGINT NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part  TEXT NOT NULL,               -- 'krisamin' 또는 '*' (catch-all)
    account_id  BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);
CREATE INDEX idx_address_account ON address(account_id);

-- 3) 기존 account의 주소를 address 행으로 이관
INSERT INTO address (domain_id, local_part, account_id, created_at)
SELECT domain_id, local_part, id, created_at FROM account;

-- 4) alias 이관 (account 주소와 겹치면 account 쪽 우선 — 기존에도 겹침 금지였음)
INSERT INTO address (domain_id, local_part, account_id, created_at)
SELECT domain_id, local_part, account_id, created_at FROM alias
ON CONFLICT (domain_id, local_part) DO NOTHING;

DROP TABLE alias;

-- 5) oidc_email 백필 (기존 주소 기반), oidc_subject 백필 후 NOT NULL 강제
UPDATE account a
SET oidc_email = a.local_part || '@' || d.name
FROM domain d WHERE d.id = a.domain_id AND a.oidc_email IS NULL;

-- 기존 행 중 sub 없는 계정: 'legacy:<email>' 대체 키 (JIT 로그인 시 도달 불가 —
-- 수신/발신은 되지만 웹 로그인은 새 계정으로 잡힌다. prod는 리셋 예정이라 무해)
UPDATE account SET oidc_subject = 'legacy:' || oidc_email WHERE oidc_subject IS NULL OR oidc_subject = '';

ALTER TABLE account ALTER COLUMN oidc_subject SET NOT NULL;
ALTER TABLE account ALTER COLUMN oidc_email SET NOT NULL;
ALTER TABLE account ADD CONSTRAINT account_oidc_subject_key UNIQUE (oidc_subject);

-- 6) 주소 컬럼 제거 (신원과 주소 분리 완성)
ALTER TABLE account DROP COLUMN local_part;
ALTER TABLE account DROP COLUMN domain_id;
