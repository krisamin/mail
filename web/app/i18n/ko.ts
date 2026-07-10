/** Korean dictionary — the master. Every key is defined here; en/ja mirror it 1:1. */
export const ko = {
  // ── common actions / states ─────────────────────────────
  "common.add": "추가",
  "common.save": "저장",
  "common.delete": "삭제",
  "common.issue": "발급",
  "common.assign": "지정",
  "common.retry": "재시도",
  "common.active": "활성",
  "common.inactive": "비활성",
  "common.ok": "정상",
  "common.error": "오류",
  "common.login": "로그인",
  "common.logout": "로그아웃",
  "common.unknownIntent": "알 수 없는 요청이에요.",
  "common.copy": "복사",
  "common.copied": "복사됨",
  "common.working": "처리 중…",
  "common.confirmDelete": "정말 삭제할까요? 되돌릴 수 없어요.",
  "common.confirmDkimClear": "DKIM 키를 해제할까요? DNS에 등록된 키와 어긋나 발송 서명이 즉시 깨져요.",
  "common.confirmRevoke": "이 앱 비밀번호를 해제할까요? 이 비밀번호를 쓰는 클라이언트는 로그인이 끊겨요.",
  "common.refreshedAt": "{{time}} 갱신",
  "common.invalidValue": "잘못된 값이에요.",

  // ── navigation ──────────────────────────────────────────
  "nav.dashboard": "대시보드",
  "nav.domain": "도메인",
  "nav.account": "계정",
  "nav.relay": "relay",
  "nav.queue": "발송 큐",
  "nav.system": "시스템",
  "nav.adminConsole": "관리 콘솔",
  "nav.myAccount": "내 계정",

  "home.tagline": "멀티테넌트 메일 서버",

  // ── error page ──────────────────────────────────────────
  "error.title": "오류",
  "error.unknown": "알 수 없는 오류가 발생했어요.",
  "error.notFound": "페이지를 찾을 수 없어요.",
  "error.home": "홈으로",

  // ── auth (server-rendered messages) ─────────────────────
  "auth.invalidResponse": "잘못된 인증 응답이에요.",
  "auth.provisionFailed": "로그인을 확인하지 못했어요: {{message}}",
  "auth.adminRequired": "관리자 권한이 필요해요.",

  // ── self-service account ────────────────────────────────
  "account.noAccount": "{{email}} 에 연결된 메일 계정이 아직 없어요.",
  "account.noAccountHint": "다시 로그인하면 자동으로 만들어져요.",
  "account.title": "내 메일 계정",
  "account.addressIntro": "내 메일 주소 — 이 주소들로 받고 보낼 수 있어요:",
  "account.addressAdminHint": "주소 추가는 관리자에게 요청해 주세요.",
  "account.appPasswordHint": "IMAP/SMTP 접속에는 아래에서 발급한 앱 비밀번호를 사용해요 (OIDC 비밀번호가 아니에요).",
  "account.secretIssued": "새 앱 비밀번호 — 지금만 표시돼요. 메일 앱에 바로 붙여넣어 주세요.",
  "account.appPassword": "앱 비밀번호",
  "account.labelPlaceholder": "라벨 (예: Thunderbird 노트북)",
  "account.noActivePassword": "활성 앱 비밀번호가 없어요. 위에서 발급해 주세요.",
  "account.revokedCount": "해제된 비밀번호 {{count}}개",

  // ── address / app password shared bits ──────────────────
  "mail.noAddress": "주소 없음",
  "mail.deleteAddress": "주소 삭제",
  "mail.noLabel": "(라벨 없음)",
  "mail.issuedAt": "발급 {{date}}",
  "mail.lastUsedAt": "마지막 사용 {{date}}",
  "mail.neverUsed": "미사용",
  "mail.revoke": "해제",

  // ── admin dashboard ─────────────────────────────────────
  "dashboard.title": "대시보드",
  "dashboard.activeDomain": "활성 도메인",
  "dashboard.account": "계정",
  "dashboard.queuePending": "발송 대기",
  "dashboard.queueFailed": "발송 실패",
  "dashboard.domain": "도메인",
  "dashboard.manage": "관리 →",
  "dashboard.noDomain": "도메인이 없어요.",

  // ── admin domain ────────────────────────────────────────
  "domain.title": "도메인",
  "domain.description": "메일을 받고 보낼 도메인을 관리해요. 주소·계정 연결은 계정 페이지에서 해요.",
  "domain.dkimIssued": "DKIM 키가 생성됐어요 — 아래 DNS TXT 레코드를 등록해 주세요:",
  "domain.dnsVerifyPrefix": "DNS 검증 —",
  "domain.expectedValue": "등록할 값",
  "domain.none": "도메인이 없어요.",
  "domain.dnsVerify": "DNS 검증",
  "domain.dkimClear": "해제",
  "domain.dkimCreate": "DKIM 키 생성",
  "domain.rsaCompat": "RSA-2048 (호환 ◎)",

  // ── admin account ───────────────────────────────────────
  "adminAccount.title": "계정",
  "adminAccount.description":
    "사람 계정은 첫 로그인 때 자동으로 만들어져요 (OIDC 신원 기준). 서비스 계정은 로그인 없이 주소와 앱 비밀번호만 갖는 시스템용이에요.",
  "adminAccount.secretIssued": "앱 비밀번호 — 지금만 표시돼요.",
  "adminAccount.createService": "서비스 계정 추가",
  "adminAccount.empty": "계정이 없어요 — 유저가 로그인하거나 서비스 계정을 만들면 여기 나타나요.",
  "adminAccount.service": "서비스",
  "adminAccount.address": "주소",
  "adminAccount.addressPlaceholder": "hello 또는 *",
  "adminAccount.appPassword": "앱 비밀번호",
  "adminAccount.labelPlaceholder": "라벨 (예: Thunderbird)",

  // ── admin relay ─────────────────────────────────────────
  "relay.title": "발송 relay",
  "relay.description":
    "외부 도메인으로 나가는 메일이 경유할 SMTP relay예요. 서버 내 도메인끼리는 relay 없이 내부 배달돼요. 도메인별 지정이 없으면 기본 relay를 사용해요.",
  "relay.new": "새 relay",
  "relay.namePlaceholder": "이름 (resend)",
  "relay.passwordPlaceholder": "password / API key",
  "relay.passwordKeep": "(설정됨 — 비우면 유지)",
  "relay.default": "기본",
  "relay.defaultRelay": "기본 relay",
  "relay.empty": "relay가 없어요 — 외부 발송은 큐에 쌓였다가 relay를 추가하면 나가요.",
  "relay.perDomain": "도메인별 발신 relay",
  "relay.defaultOption": "(기본 relay)",

  // ── admin queue ─────────────────────────────────────────
  "queue.title": "발송 큐",
  "queue.stat": "대기 {{pending}} · 완료 {{sent}} · 실패 {{failed}}",
  "queue.filterAll": "전체",
  "queue.filterPending": "대기",
  "queue.filterSent": "완료",
  "queue.filterFailed": "실패",
  "queue.empty": "항목이 없어요.",
  "queue.attemptCount": "시도 {{count}}회",

  // ── admin system ────────────────────────────────────────
  "system.title": "시스템 점검",
  "system.setting": "설정",
  "system.locale": "표시 언어",
  "system.localeDesc":
    "웹 UI의 표시 언어예요. 자동이면 방문자의 브라우저 언어를 따르고, 특정 언어로 고정하면 모든 사용자에게 그 언어로 보여요.",
  "system.localeAuto": "자동 (브라우저 언어)",
  "system.localeSaved": "표시 언어를 저장했어요.",
  "system.recheck": "다시 점검",
  "system.uptime": "가동 시간",
  "system.db": "데이터베이스",
  "system.queue": "발송 큐",
  "system.externalPrefix": "외부 도달성 —",
  "system.externalDesc":
    "공인 호스트네임의 표준 포트로 실제 접속해요 — 메일 클라이언트가 겪는 경로예요. LB·라우터 포워딩이 열려 있어야 성공해요. 헤어핀 NAT 미지원 라우터에선 오탐이 있을 수 있어요.",
  "system.checking": "점검 중… (차단된 포트는 타임아웃까지 몇 초 걸려요)",
  "system.reachable": "도달",
  "system.blocked": "차단",
  "system.listener": "내부 리스너",
  "system.listenerDesc":
    "데몬 자기 점검(self-dial)이에요 — 프로세스가 listen 중이고 프로토콜 응답이 정상인지만 확인해요. 외부 접속 가능 여부와는 별개예요.",
  "system.up": "정상",
  "system.down": "다운",
};
