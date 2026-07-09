-- 0007 account kind: 사람(user) vs 서비스(service) 계정 구분
--
-- 서비스 계정 = OAuth 로그인 없는 시스템 계정. 주소 + 앱 비밀번호만 갖고
-- IMAP/SMTP로만 쓴다 (봇, 알림 발송기 등). oidc_subject는 'service:<email>'
-- 합성값 — 실제 IdP sub와 충돌하지 않아 웹 로그인 경로가 원천 차단된다.

ALTER TABLE account ADD COLUMN kind TEXT NOT NULL DEFAULT 'user';
