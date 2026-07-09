-- dev 시드 — 테스트(TRUNCATE)가 dev DB를 비운 뒤 복원용.
-- 사용: make seed-dev  (또는 docker exec로 직접)
-- ★DKIM 개인키는 여기 없음 — ~/.mail-keys/*.pem에서 별도 UPDATE (make seed-dev가 처리).
-- 0006 모델: account = OIDC 신원, address = 계정 소유 주소.

INSERT INTO domain (name) VALUES ('krisam.in'), ('kirby.so')
ON CONFLICT (name) DO NOTHING;

-- 계정 (dev Keycloak maro 유저의 sub는 로그인 시 갱신되므로 시드에선 test: prefix)
INSERT INTO account (oidc_subject, oidc_email)
VALUES ('test:maro@krisam.in', 'maro@krisam.in')
ON CONFLICT (oidc_subject) DO NOTHING;

INSERT INTO account (oidc_subject, oidc_email)
VALUES ('test:guest@krisam.in', 'guest@krisam.in')
ON CONFLICT (oidc_subject) DO NOTHING;

-- 주소: primary 2개 + hello@krisam.in → maro, *@kirby.so → maro (catch-all)
INSERT INTO address (domain_id, local_part, account_id)
SELECT d.id, 'maro', a.id FROM domain d, account a
WHERE d.name = 'krisam.in' AND a.oidc_email = 'maro@krisam.in'
ON CONFLICT DO NOTHING;

INSERT INTO address (domain_id, local_part, account_id)
SELECT d.id, 'guest', a.id FROM domain d, account a
WHERE d.name = 'krisam.in' AND a.oidc_email = 'guest@krisam.in'
ON CONFLICT DO NOTHING;

INSERT INTO address (domain_id, local_part, account_id)
SELECT d.id, 'hello', a.id FROM domain d, account a
WHERE d.name = 'krisam.in' AND a.oidc_email = 'maro@krisam.in'
ON CONFLICT DO NOTHING;

INSERT INTO address (domain_id, local_part, account_id)
SELECT d.id, '*', a.id FROM domain d, account a
WHERE d.name = 'kirby.so' AND a.oidc_email = 'maro@krisam.in'
ON CONFLICT DO NOTHING;

SELECT 'domain' AS t, count(*) FROM domain
UNION ALL SELECT 'account', count(*) FROM account
UNION ALL SELECT 'address', count(*) FROM address
UNION ALL SELECT 'relay', count(*) FROM relay;
