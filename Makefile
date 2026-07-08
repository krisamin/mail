# mail 개발 진입점. `make help`로 목록 확인.
.DEFAULT_GOAL := help
SHELL := /bin/bash

# .env 로드 (있으면)
-include .env
export

.PHONY: help
help: ## 명령 목록
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

## ── 인프라 ──────────────────────────────────────────────
.PHONY: up
up: ## dev 인프라 기동 (Postgres, 첫 기동 시 스키마 자동 생성)
	docker compose up -d
	@echo "Postgres → localhost:$(POSTGRES_PORT)"

.PHONY: down
down: ## dev 인프라 중지
	docker compose down

.PHONY: reset-db
reset-db: ## DB 볼륨 삭제 후 재생성 (마이그레이션 재적용)
	docker compose down -v
	docker compose up -d
	@echo "볼륨 초기화 + 스키마 재적용 완료"

.PHONY: psql
psql: ## dev DB에 psql 접속
	docker compose exec postgres psql -U $(POSTGRES_USER) -d $(POSTGRES_DB)

## ── 빌드/테스트 ─────────────────────────────────────────
.PHONY: build
build: ## 전체 빌드
	go build ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## 유닛 테스트 (DB 불필요한 것만)
	go test ./... -short

.PHONY: db-test
db-test: ## 통합 테스트 (dev DB 필요 — store/imap/smtp e2e)
	# -p 1 필수: 테스트 패키지들이 같은 dev DB를 TRUNCATE하므로 병렬 실행 시 서로 밟음
	MAIL_TEST_DSN="$(MAIL_TEST_DSN)" go test -p 1 ./internal/... -v

.PHONY: run
run: ## maild 실행 (IMAP :1143, dev DB 필요)
	MAIL_DSN="$(MAIL_DSN)" go run ./cmd/maild

.PHONY: seed-dev
seed-dev: ## dev DB 시드 복원 (db-test가 TRUNCATE한 뒤 실행)
	docker cp dev/seed.sql mail-postgres-1:/tmp/seed.sql
	docker exec mail-postgres-1 sh -c 'PGPASSWORD=$(POSTGRES_PASSWORD) psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -f /tmp/seed.sql'
	@# DKIM 키 복원 (로컬 백업이 있으면)
	@if [ -f ~/.mail-keys/kirby.so-mail-dkim.pem ]; then \
		python3 -c "import pathlib; key = pathlib.Path.home().joinpath('.mail-keys/kirby.so-mail-dkim.pem').read_text(); print(f\"UPDATE domain SET dkim_selector='mail', dkim_private_key='{key}' WHERE name='kirby.so';\")" > /tmp/dkim-restore.sql; \
		docker cp /tmp/dkim-restore.sql mail-postgres-1:/tmp/; \
		docker exec mail-postgres-1 sh -c 'PGPASSWORD=$(POSTGRES_PASSWORD) psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) -f /tmp/dkim-restore.sql'; \
		rm /tmp/dkim-restore.sql; \
		echo "kirby.so DKIM 키 복원됨"; \
	fi

.PHONY: check
check: build vet ## 커밋 전 검증 (빌드 + vet)

## ── 스파이크 ────────────────────────────────────────────
.PHONY: spike-smtp
spike-smtp: ## Phase 0 SMTP 수신 스파이크 서버 실행
	go run ./spikes/smtp-recv
