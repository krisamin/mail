-- 0007 down: kind 컬럼 제거 (서비스 계정 행 자체는 남는다 — 로그인만 불가)

ALTER TABLE account DROP COLUMN kind;
