-- 0008: 전역 설정 key-value 저장소.
-- 첫 용도: 웹 UI 표시 언어 (key='locale', value='auto|ko|en|ja').
CREATE TABLE setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
