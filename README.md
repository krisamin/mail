# mail

OAuth 기반으로 작동하는, 직접 구현하는 멀티테넌트 메일 서버.

mailcow처럼 여러 도메인·계정을 관리할 수 있고, 나만 쓰는 게 아니라 남에게도
계정을 열어줄 수 있는 수준을 목표로 한다. 프로토콜(SMTP/IMAP)은 검증된
Go 라이브러리의 백엔드로 구현하고, 저장·큐·라우팅·멀티테넌시·인증(OAuth)은
직접 설계·구현한다.

## 왜 만드나

- **학습**: 메일 서버가 실제로 어떻게 도는지 밑바닥부터 이해한다.
- **OAuth 우선**: 사람 로그인(관리 UI/웹메일)은 OIDC/OAuth. 메일 앱(IMAP/SMTP)은
  OAuth로 발급한 앱 비밀번호로 인증한다 (Fastmail/Migadu 방식).
- **멀티테넌시**: 도메인 → 유저 → 메일박스 3계층. 공개 서비스 수준.

## 아키텍처 (2계층)

```
A. 프로토콜 엔진   ← emersion/go-smtp, go-imap 백엔드로 구현 (RFC 준수는 검증된 코드에)
B. 관리 플레인     ← 저장·큐·라우팅·멀티테넌시·OAuth·Admin API·프론트 (직접 설계)
```

- **A**: SMTP(송수신), IMAP(저장 접근), 메시지 파싱, DKIM/SPF/DMARC
- **B**: PostgreSQL 메타 + 오브젝트 스토어 본문, 발송 큐, OIDC 인증,
  Go Admin REST API, React Router v7(Bun) 관리 UI

## 인증 설계

| 대상 | 방식 |
|---|---|
| 사람 (관리 UI / 웹메일) | OIDC/OAuth 로그인 |
| 메일 앱 (Thunderbird 등, IMAP/SMTP) | OAuth로 발급한 앱 비밀번호 (revoke·스코프 제한 가능) |
| 자작 웹메일 | OAUTHBEARER 순정 OAuth 가능 |

> Thunderbird/Apple Mail/Outlook은 OAuth 제공자 목록을 하드코딩해서
> 커스텀 서버에는 순정 OAuth를 못 쓴다 → 앱 비밀번호가 현실적 정답.

## 기술 스택

| 층 | 선택 |
|---|---|
| SMTP/IMAP 프레임 | `emersion/go-smtp`, `emersion/go-imap` (백엔드 직접 구현) |
| 메시지 파싱 | `emersion/go-message` |
| DKIM/DMARC | `emersion/go-msgauth` |
| SASL/OAuth | `emersion/go-sasl` + OIDC IdP (Authentik/Keycloak) |
| 메타 DB | PostgreSQL |
| 본문 저장 | 오브젝트 스토어(MinIO/S3) 또는 PV |
| 관리 백엔드 | Go |
| 프론트 | Bun + React Router v7 + Tailwind |
| 배포 | Kubernetes (개발), outbound는 SMTP relay 경유 |

## 로드맵

- [x] **Phase 0** — 프로토콜 감 잡기 (go-smtp 수신 스파이크) → `spikes/smtp-recv`
- [x] **Phase 1** — 저장 엔진 (Postgres 스키마 + IMAP 백엔드)
  - [x] store 도메인 타입 + 인터페이스 (`internal/store`)
  - [x] Postgres 스키마 마이그레이션 (`internal/store/migration`, up/down 검증)
  - [x] Postgres 구현체 (인증/메일박스/메시지) + 통합 테스트 PASS
  - [x] go-imap v2 `imapserver.Session`을 store 위에서 구현 (`internal/imap`, DD-06 세션 스냅샷)
        — imapclient 통합테스트로 LOGIN→LIST→SELECT→APPEND→FETCH→STORE→SEARCH→COPY→EXPUNGE 왕복 PASS
  - [x] `cmd/maild`에 IMAP 서버 조립 (`MAIL_DSN`/`MAIL_IMAP_ADDR`, dev 기본 :1143)
  - [ ] Thunderbird로 붙어서 INBOX 검증 *(마로 데스크톱 필요 — 보류)*
- [ ] **Phase 2** — 발송 큐 + DKIM 서명 + OAuth/SASL 인증 — *진행중*
  - [x] **2-1. SMTP 수신 배달** (`internal/smtp`) — RCPT 단계 수신자 검증(550, backscatter
        방지, 오픈 릴레이 아님) + 수신자별 Received 헤더 + INBOX 자동 생성 배달.
        e2e 테스트: SMTP 발사→IMAP 읽기 왕복, NOOP이 새 메일 감지까지 PASS
  - [x] **2-2. SMTP AUTH + submission** (`internal/smtp/submission.go`, dev :2587) —
        SASL PLAIN(앱 비밀번호) 필수, envelope from=인증 계정 강제(위조 553),
        로컬 배달, 외부 도메인은 발송 큐 전까지 550. 테스트 5종 PASS
  - [x] **2-3. 발송 큐** (`internal/queue` + 마이그레이션 0002) — outbound_queue
        rcpt 단위 적재, 워커 폴링(FOR UPDATE SKIP LOCKED), 지수 백오프 재시도
        (1m→2m→…, 기본 6회), 영구 오류(5xx) 즉시 failed. Sender 인터페이스 뒤에
        RelaySender(DD-04, STARTTLS+PLAIN) — relay 계정만 채우면 됨(`MAIL_RELAY_*`).
        테스트 5종: 성공/백오프/영구오류/소진/제출→큐→relay 실발송 왕복. bounce DSN은 TODO
  - [x] **2-4. DKIM 서명 + 수신 SPF/DKIM/DMARC 검증** (`internal/auth` + 마이그레이션 0003)
        — 발송: 도메인별 DKIM 키(domains.dkim_*)로 워커에서 서명(relaxed/relaxed,
        RSA/Ed25519, best-effort). 수신: SPF(blitiri)+DKIM+DMARC(relaxed alignment)
        검증 후 Authentication-Results 헤더 기록 (거절/격리는 Phase 4).
        테스트 7종: 서명↔검증 왕복(RSA/Ed25519)/변조 감지/DMARC 정렬/store 키 훅
- [ ] **Phase 3** — Admin REST API + React Router v7 관리 UI — *진행중*
  - [x] **Go Admin API** (`internal/api`, :8080) — OIDC Bearer 검증(JWKS) +
        groups claim 인가(mail-admin). 도메인/DKIM(Ed25519 생성→DNS TXT 반환)/
        유저/앱비번(평문 1회 노출)/발송 큐 관리. 테스트 4종 PASS
  - [x] **dev IdP** — compose에 Keycloak 26 (realm 자동 임포트: mail-admin 그룹,
        maro/guest 유저, groups claim 매퍼)
  - [x] **RR7 관리 UI** (`web/`, bun, :5573) — OIDC code flow 로그인(세션 쿠키),
        `/admin` 레이아웃 가드(미로그인→login, 그룹 없음→403). 대시보드/도메인
        (생성·활성·DKIM)/유저(생성·앱비번 발급·revoke)/발송 큐(필터·재시도).
        e2e: maro→admin 접근+기능 왕복, guest 403, 발급 앱비번으로 IMAP LOGIN OK
  - [x] **일반 유저 셀프서비스** — `/api/me/*` (RequireUser: 토큰만, 그룹 불필요) +
        `/account` 페이지. email 클레임→메일 계정 매핑, 본인 앱비번 발급(평문
        1회)/revoke(소유권 검증=IDOR 방지), 미개설 계정 안내. e2e: guest UI
        발급 앱비번으로 IMAP LOGIN OK, maro의 타인 비번 revoke 404
  - [x] **별칭 + 와일드카드** (마이그레이션 0004) — `hello@dom→user` 정확 별칭,
        `*@dom` catch-all. 해석 우선순위: 실제 유저 > 정확 별칭 > 와일드카드.
        수신(MX)/submission 모두 ResolveAddress로 배달, 발신은 CanSendAs로
        본인 별칭 envelope from 허용(타인 별칭 553). admin UI(유저 페이지
        별칭 섹션) + /account에 내 수신 주소 표시. 실기동 e2e 검증
  - [x] **멀티도메인 내부 라우팅** — 서버에 있는 도메인끼리(krisam.in↔kirby.so)는
        발송 큐/relay를 안 거치고 직접 배달 (TestInternalRoutingTwoDomains,
        큐 0건 검증)
  - [x] **로그인 게이트** — OIDC email의 도메인이 서버에 등록돼 있어야 로그인
        (콜백에서 /api/me/gate 판정, 미등록 도메인 403 + 세션 미생성. outsider
        e2e 검증). 도메인은 있고 계정만 없으면 로그인 허용 + 미개설 안내
  - [x] **relay 결정: Resend** — smtp.resend.com:587, user='resend', pw=API키
        (.env.example 참고). Resend 대시보드 도메인 검증 후 활성화
  - [x] **relay DB 관리** (마이그레이션 0005) — relay 여러 개 등록 + 도메인별
        지정. 해석: 도메인 지정 → default → env MAIL_RELAY_* fallback.
        password는 쓰기 전용(API 응답에 hasPassword만, 빈 문자열=기존 유지).
        admin UI `/admin/relay` (CRUD + 도메인별 지정 셀렉트).
        발송 시점 해석이라 relay 변경에 재기동 불필요.
        e2e: DB relay(Resend)로 mail-tester 10/10
  - [x] **DNS 검증** — `/api/admin/domain/{id}/dns`가 MX/SPF/DKIM/DMARC를
        공용 DNS(1.1.1.1)로 실조회 → 도메인 페이지 "DNS 검증" 버튼이
        ✓/!/✗ 배지 + 등록할 기대값 표시. DKIM은 DB 개인키에서 공개키를
        재계산해 DNS 값과 일치까지 비교 (스플릿 DNS 회피를 위해 공용 DNS 직결)
  - [x] **네이밍 컨벤션** — 복수형 s 금지: DB 테이블 단수(domain/account/alias/
        relay/...), 컬렉션 변수는 xxxList, API 경로 단수. users→account는
        Postgres 예약어(user) 회피 겸
- [ ] **Phase 4** — 프로덕션화 (deliverability, 안티스팸, k8s, 백업)

## 개발

```bash
# Go 1.26+, Bun 1.3+, Docker

# 1) dev 인프라 (Postgres) — 첫 기동 시 스키마 자동 생성
cp .env.example .env
make up               # docker compose up -d
make db-test          # 통합 테스트 (compose DB에 연결)

# 스파이크
make spike-smtp       # Phase 0 SMTP 수신 서버 (:2525)
go run ./spikes/smtp-recv/testclient   # 테스트 메일 한 통 전송

# 기타
make help             # 전체 명령 목록
make reset-db         # DB 볼륨 초기화 + 마이그레이션 재적용
make check            # 커밋 전 검증 (build + vet)
```

> dev 환경은 지금 Postgres만 compose로 띄운다. 앱(maild)은 아직 호스트에서
> `go run`으로 돈다. Phase 2에서 valkey(발송 큐), Phase 3에서 backend/frontend를
> compose에 추가한다.

## 구조

```
cmd/maild/          # 메인 데몬 (예정)
internal/
  smtp/             # SMTP 백엔드
  store/            # 메일박스 저장 엔진
  config/           # 설정
spikes/             # 버리는 학습용 실험 코드
  smtp-recv/        # Phase 0: SMTP 수신 흐름 관찰
web/                # React Router v7 프론트 (예정)
docs/               # 설계 문서
```

## 라이선스

미정 (공개 예정).
