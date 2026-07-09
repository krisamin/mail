-- 0006 down: account를 local_part@domain 모델로 되돌린다 (best-effort).
-- 각 계정의 가장 오래된 비-와일드카드 주소를 계정 주소로 복원한다.

ALTER TABLE account ADD COLUMN domain_id BIGINT REFERENCES domain(id) ON DELETE CASCADE;
ALTER TABLE account ADD COLUMN local_part TEXT;

UPDATE account a
SET domain_id = pick.domain_id, local_part = pick.local_part
FROM (
    SELECT DISTINCT ON (account_id) account_id, domain_id, local_part
    FROM address WHERE local_part <> '*'
    ORDER BY account_id, created_at ASC
) pick
WHERE pick.account_id = a.id;

-- 주소가 하나도 없던 계정은 복원 불가 → 삭제
DELETE FROM account WHERE domain_id IS NULL OR local_part IS NULL;

ALTER TABLE account ALTER COLUMN domain_id SET NOT NULL;
ALTER TABLE account ALTER COLUMN local_part SET NOT NULL;
ALTER TABLE account ADD CONSTRAINT account_domain_id_local_part_key UNIQUE (domain_id, local_part);

-- alias 복원: 계정 주소로 쓰인 행 제외 나머지
CREATE TABLE alias (
    id          BIGSERIAL PRIMARY KEY,
    domain_id   BIGINT NOT NULL REFERENCES domain(id) ON DELETE CASCADE,
    local_part  TEXT NOT NULL,
    account_id  BIGINT NOT NULL REFERENCES account(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (domain_id, local_part)
);
CREATE INDEX idx_aliases_user ON alias(account_id);

INSERT INTO alias (domain_id, local_part, account_id, created_at)
SELECT ad.domain_id, ad.local_part, ad.account_id, ad.created_at
FROM address ad
JOIN account a ON a.id = ad.account_id
WHERE NOT (ad.domain_id = a.domain_id AND ad.local_part = a.local_part);

DROP TABLE address;

ALTER TABLE account DROP CONSTRAINT account_oidc_subject_key;
ALTER TABLE account ALTER COLUMN oidc_subject DROP NOT NULL;
ALTER TABLE account DROP COLUMN oidc_email;
