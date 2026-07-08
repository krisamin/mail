-- 0003 dkim keys: 도메인별 DKIM 서명 키 (Phase 2-4)
-- 발송 시 발신 도메인의 키로 서명한다. 키가 없으면 서명 없이 발송.
-- 공개키는 DNS TXT(<selector>._domainkey.<domain>)에 게시해야 한다.

ALTER TABLE domain
    ADD COLUMN dkim_selector TEXT,          -- 예: 'mail' (NULL = 서명 안 함)
    ADD COLUMN dkim_private_key TEXT;       -- PKCS#8 PEM (RSA 또는 Ed25519)
