-- 0005 relay: 발송 relay를 DB로 관리 (env 하드코딩 탈피)
--
-- relay 여러 개 등록 + 도메인별 지정:
--   domain.relay_id 지정   → 그 도메인 발신은 해당 relay 사용
--   domain.relay_id NULL   → is_default relay 사용
--   default도 없으면       → env MAIL_RELAY_* fallback (마이그레이션 편의)
--
-- 내부 도메인끼리는 relay를 안 거친다 (내부 직접 배달 — 0004 참고).
-- password는 평문 저장 (홈랩 단일서버 — 마스터키도 같은 서버라 암호화는
-- 자물쇠 그림만 좋아지는 수준. API로는 절대 노출 안 함 = 쓰기 전용).

CREATE TABLE relay (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,        -- 'resend', 'ses-backup' 등 표시명
    host        TEXT NOT NULL,               -- smtp.resend.com
    port        INT NOT NULL DEFAULT 587,
    username    TEXT NOT NULL DEFAULT '',
    password    TEXT NOT NULL DEFAULT '',
    starttls    BOOLEAN NOT NULL DEFAULT true,
    is_default  BOOLEAN NOT NULL DEFAULT false,
    active      BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- default는 하나만
CREATE UNIQUE INDEX idx_relay_default ON relay (is_default) WHERE is_default;

-- 도메인별 발신 relay 지정 (NULL = default relay)
ALTER TABLE domain
    ADD COLUMN relay_id BIGINT REFERENCES relay(id) ON DELETE SET NULL;
