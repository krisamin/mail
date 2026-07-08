-- dev 시드 — 테스트(TRUNCATE)가 dev DB를 비운 뒤 복원용.
-- 사용: make seed-dev  (또는 docker exec로 직접)
-- ★DKIM 개인키는 여기 없음 — ~/.mail-keys/*.pem에서 별도 UPDATE (make seed-dev가 처리).

INSERT INTO domain (name) VALUES ('krisam.in'), ('kirby.so')
ON CONFLICT (name) DO NOTHING;

INSERT INTO account (domain_id, local_part)
SELECT id, 'maro' FROM domain WHERE name = 'krisam.in'
ON CONFLICT DO NOTHING;

INSERT INTO account (domain_id, local_part)
SELECT id, 'guest' FROM domain WHERE name = 'krisam.in'
ON CONFLICT DO NOTHING;

-- 별칭: hello@krisam.in → maro, *@kirby.so → maro (catch-all)
INSERT INTO alias (domain_id, local_part, account_id)
SELECT d.id, 'hello', a.id
FROM domain d, account a JOIN domain ad ON ad.id = a.domain_id
WHERE d.name = 'krisam.in' AND a.local_part = 'maro' AND ad.name = 'krisam.in'
ON CONFLICT DO NOTHING;

INSERT INTO alias (domain_id, local_part, account_id)
SELECT d.id, '*', a.id
FROM domain d, account a JOIN domain ad ON ad.id = a.domain_id
WHERE d.name = 'kirby.so' AND a.local_part = 'maro' AND ad.name = 'krisam.in'
ON CONFLICT DO NOTHING;

SELECT 'domain' AS t, count(*) FROM domain
UNION ALL SELECT 'account', count(*) FROM account
UNION ALL SELECT 'alias', count(*) FROM alias
UNION ALL SELECT 'relay', count(*) FROM relay;
