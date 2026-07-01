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
- [ ] **Phase 1** — 저장 엔진 (Postgres 스키마 + IMAP 백엔드) — *진행중*
  - [x] store 도메인 타입 + 인터페이스 (`internal/store`)
  - [x] Postgres 스키마 마이그레이션 (`internal/store/migrations`, up/down 검증)
  - [x] Postgres 구현체 (인증/메일박스/메시지) + 통합 테스트 PASS
  - [ ] go-imap v2 `imapserver.Session`을 store 위에서 구현
  - [ ] Thunderbird로 붙어서 INBOX 검증
- [ ] **Phase 2** — 발송 큐 + DKIM 서명 + OAuth/SASL 인증
- [ ] **Phase 3** — Admin REST API + React Router v7 관리 UI
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
