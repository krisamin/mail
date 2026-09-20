# 설계 결정 기록 (Design Decisions)

이 프로젝트의 방향을 가른 핵심 결정들. 나중에 "왜 이렇게 했지?" 할 때 본다.

## DD-01. 2계층 아키텍처 (프로토콜 엔진 / 관리 플레인)

메일 서버 = 두 층으로 쪼갠다.

- **A. 프로토콜 엔진**: SMTP 송수신, IMAP 저장 접근, 메시지 파싱, DKIM/SPF/DMARC.
  → 밑바닥부터 RFC 파서를 손으로 짜지 않는다. `emersion/go-smtp`, `go-imap`의
    **백엔드 인터페이스를 구현**하는 방식. 프로토콜 상태머신·RFC 준수는 검증된
    코드에 맡기고, 우리는 콜백(Mail/Rcpt/Data, IMAP fetch 등)만 채운다.
- **B. 관리 플레인**: 저장, 발송 큐, 라우팅, 멀티테넌시, 인증(OAuth), Admin API, 프론트.
  → **여기가 프로젝트의 본체.** 라이브러리가 안 주는 부분이고, 여기서 배울 게 제일 많다.

근거: RFC 3501(IMAP)을 손으로 파싱하면 파서에만 몇 달 쓰고 정작 "서버가 어떻게
도는가"는 못 배운다. 저장/큐/전달 로직이 진짜 서버의 뇌.

## DD-02. 인증 — OAuth 우선, 앱 비밀번호로 클라이언트 호환

목표: "OAuth 기반으로 작동" + "다른 메일 앱에서도 로그인".

**벽**: Thunderbird/Apple Mail/Outlook은 OAuth 제공자를 앱에 하드코딩
(MozillaWiki: "you cannot use OAuth2 for your own server"). 서버가
`AUTH=OAUTHBEARER`를 완벽히 광고해도 클라이언트 UI가 커스텀 도메인엔 OAuth를
안 띄운다. → 프로토콜 문제가 아니라 클라이언트 정책 벽.

**결정**:

| 대상 | 방식 |
|---|---|
| 사람 (관리 UI / 웹메일) | 진짜 OIDC/OAuth 로그인 |
| 메일 앱 (IMAP/SMTP) | OAuth 로그인 후 발급하는 앱 비밀번호 (revoke·스코프 제한) |
| 자작 웹메일 | OAUTHBEARER 순정 OAuth |

업계 표준(Fastmail, Migadu, Proton Bridge)과 동일.

## DD-03. 저장 — 메타/본문 분리

- 메타데이터(도메인/유저/메일박스/UID/flags) → **PostgreSQL**
- 메시지 raw 본문 → **오브젝트 스토어(MinIO/S3) 또는 PV**

maildir 개념 참고하되 k8s 환경이라 오브젝트 스토어가 깔끔. IMAP 백엔드는 이
스토어를 구현한다.

## DD-04. 배포 — outbound는 relay 경유

자체 호스팅 메일의 진짜 난이도는 코드가 아니라 **deliverability**:

- OCI 등 클라우드는 outbound TCP 25를 기본 차단 → 직접 발송 불가한 경우 많음.
- 신생 IP는 Gmail/Outlook이 스팸 처리 → IP 워밍업 수 주~개월.
- 공개 서비스면 한 유저의 스팸이 IP 전체 블랙리스트 위험.

**결정**: 개발/수신은 자체 k8s. 발송(outbound)은 초기엔 **SMTP relay(SES/Postmark 등)**
경유를 기본으로 두고, 자체 발송은 PTR/rDNS 확보 + 워밍업 후 선택적으로.

## DD-05. 스택

Go(백엔드/프로토콜) + Bun·React Router v7(프론트) + PostgreSQL + OIDC IdP.
emersion 생태계(go-smtp/go-imap/go-message/go-msgauth/go-sasl)를 프로토콜
기반으로 채택.

## DD-06. IMAP 세션 동시성 — Phase 1은 세션 스냅샷

go-imap의 참조 구현(imapmemserver)은 in-memory tracker로 세션 간 실시간
업데이트(EXPUNGE/EXISTS 브로드캐스트)를 처리하지만, 우리는 상태가 Postgres에
있으므로 그대로 못 쓴다.

**결정 (Phase 1)**: SELECT 시 메일박스의 (msgID, UID) 목록을 세션 메모리에
스냅샷으로 뜬다. 시퀀스 번호 = 스냅샷 인덱스+1. 다른 세션이 만든 변경은
Poll(NOOP 등 명령 사이)과 Idle(15초 주기 폴링)에서 스냅샷↔DB 비교로 반영:
사라진 UID → EXPUNGE 응답, 새 UID → 스냅샷 뒤에 추가 + EXISTS 응답.

- 장점: store 인터페이스 변경 없음. RFC 3501의 "seqnum은 세션 내 일관"
  요구를 스냅샷이 자연스럽게 만족.
- 한계: 실시간 push가 아니라 폴링. IDLE 알림이 최대 15초 지연.
- **Phase 2+**: Postgres LISTEN/NOTIFY로 tracker를 만들어 폴링 제거 예정.

플래그 등 가변 메타는 스냅샷에 넣지 않고 명령마다 DB에서 읽는다
(스냅샷은 신원(msgID/UID)만 고정, 상태는 항상 최신).

## DD-07. 수신 인증 검증은 기록만 (Phase 2), 정책 판단은 Phase 4

수신 메일의 SPF/DKIM/DMARC 검증 결과는 Authentication-Results 헤더로
**기록만** 하고, 거절/격리 같은 정책 집행은 하지 않는다.

근거: DMARC p=reject를 곧이곧대로 집행하면 포워딩/메일링리스트 경유 정상
메일이 대량으로 죽는다 (SPF는 포워딩에서 반드시 깨짐). 제대로 하려면
ARC(RFC 8617)나 화이트리스트가 필요한데 이는 Phase 4 안티스팸(Rspamd 검토)
범위. 그때까지는 검증 데이터를 쌓으면서 클라이언트(웹메일/필터)가 헤더를
참고할 수 있게만 한다.

DKIM 발송 서명도 best-effort — 서명 실패가 발송을 막지 않는다 (로그만).
키는 도메인 단위(domains.dkim_selector/dkim_private_key, PKCS#8 PEM),
공개키 DNS 게시(<selector>._domainkey.<domain> TXT)는 운영자 몫.

## DD-08. 웹은 셸 하나, 영역 셋 (2026-09)

`/`(런처) · `/account` · `/mail` · `/admin`이 각자 헤더를 따로 구현하던 구조를
버리고, 레일 하나에 **메일 · 설정 · 관리** 세 영역을 다는 구조로 바꿨다.

근거: 같은 앱인데 로고 문구가 "mail box / mail account / mail admin"으로
셋이었고, 설정이 세 군데(주소·앱 비밀번호는 /account, 필터는 웹메일
사이드바 말단, 표시 언어는 UI 없음)로 흩어져 있었다. 위계를 화면 구조로
고정하면 새 기능이 들어올 자리가 자명해진다 — 개인 설정이면 설정 영역,
서버 운영이면 관리 영역.

관리 영역은 평평한 탭 6개 대신 **메일 운영 / 발송 / 서버**로 묶는다.

## DD-09. HTML 본문은 정화 + 샌드박스 + CSP로 보여준다

이전에는 HTML 메일을 아예 렌더하지 않고 텍스트 대안만 보여줬다. 요즘 메일은
대부분 HTML이라 "빈 화면에 안내문 한 줄"이 되어 메일함 구실을 못 했다.

세 겹으로 막고 보여준다:

1. 서버가 bluemonday로 정화 (script/handler/object/form 제거)
2. `sandbox` iframe에 srcdoc으로 주입 — 스크립트 실행·폼 제출·상위 접근 불가
3. 프레임 안 CSP가 원격 로드를 막는다 (`img-src data: cid:`) → 추적 픽셀이
   조용히 열람을 알리지 못하고, 읽는 사람이 "이미지 표시"를 누를 때만
   `img-src https:`로 다시 만들어 붙인다

`allow-same-origin`은 유지한다 — 스크립트가 없으면 실행될 것이 없고, 부모가
`scrollHeight`를 읽어 프레임 높이를 본문에 맞출 수 있다.

## DD-10. 목록 미리보기·수신자는 지연 캐시

`message.preview` / `message.to_addr`는 NULL로 두고, **웹메일이 처음 목록에
올릴 때** 본문을 파싱해 채운다(0003).

근거: 배달 경로(SMTP 수신)에 MIME 파싱 비용을 얹으면 받는 속도가 파싱에
묶인다. 반대로 매 조회 때 파싱하면 목록이 blob 수만큼 느려진다. 한 번만
계산해 저장하면 기존 메일도 처음 열람될 때 자동으로 채워진다.

## DD-11. 외형·표시 언어는 계정에 저장한다

테마(system/dark/light)와 표시 언어는 `account.pref_theme` /
`account.pref_locale`에 저장한다. 쿠키는 첫 페인트용 거울일 뿐이다.

근거: 브라우저 로컬 저장이면 기기를 바꿀 때마다 다시 고르게 된다. 전역
설정(관리자)은 "고르지 않은 사람"의 기본값으로 남긴다 — 개인 설정이 있으면
그쪽이 이긴다.
