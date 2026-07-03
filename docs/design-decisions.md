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
