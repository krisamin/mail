# mail

A self-hosted, multi-tenant mail server built around OAuth — from scratch, in Go.

Think mailcow, but OIDC-first: humans sign in with your identity provider
(Authentik, Keycloak, ...), accounts are provisioned on first login, and mail
clients authenticate with revocable app passwords. Protocol framing
(SMTP/IMAP) is delegated to battle-tested libraries; storage, queueing,
routing, multi-tenancy, and the management plane are implemented here.

## Features

- **Multi-domain, multi-tenant** — serve any number of domains and accounts
  from one deployment. Cross-domain mail between hosted domains is delivered
  internally, never leaving the server.
- **OIDC-first identity** — an account *is* an OIDC subject. First login
  provisions the account, its primary address, and an INBOX (JIT). No
  passwords stored for humans, ever.
- **App passwords** — mail clients (Thunderbird, Apple Mail, ...) authenticate
  IMAP/SMTP with per-device app passwords: generated once, shown once,
  revocable, argon2id-hashed.
- **Service accounts** — system accounts with no login (bots, notification
  senders). Address + app passwords only; a synthetic OIDC subject makes web
  login structurally impossible.
- **Addresses as first-class objects** — one account owns many addresses
  across domains, including `*@domain` catch-alls. Resolution priority:
  exact match > wildcard. Send-as is enforced for every owned address.
- **Outbound queue + relays** — per-recipient queue with exponential backoff,
  DB-managed SMTP relays (per-domain assignment, write-only credentials),
  no restart needed to change relays.
- **DKIM / SPF / DMARC** — per-domain DKIM signing (RSA-2048 / Ed25519, keys
  generated in the admin UI with the DNS TXT value handed to you), inbound
  SPF/DKIM/DMARC verification recorded as `Authentication-Results`, and
  optional DMARC policy enforcement (`p=reject` → 550, `p=quarantine` → Junk).
- **Hardened by default** — IP-based brute-force protection on IMAP/SMTP
  auth, HTTP timeouts, graceful shutdown (SIGTERM-safe for rolling updates),
  optional TLS on protocol ports, timing-safe argon2id app passwords.
- **DNS self-check** — one click queries public DNS (1.1.1.1) for
  MX/SPF/DKIM/DMARC and diffs against expected values.
- **Admin UI + self-service** — React Router v7 management console (domains,
  accounts, addresses, relays, queue) plus a self-service page where users
  manage their own app passwords.
- **Embedded migrations** — the daemon converges any database (empty or
  existing) to the current schema on boot. Single binary, k8s-friendly.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│ maild (Go, single binary)                                │
│                                                          │
│  :1143 IMAP    ← emersion/go-imap v2 backend             │
│  :2525 SMTP    ← emersion/go-smtp (MX, recipient-checked)│
│  :2587 SMTP    ← submission (SASL PLAIN = app password)  │
│  :8080 REST    ← admin + self-service API (OIDC JWKS)    │
│                                                          │
│  outbound queue worker (backoff, DKIM signing, relays)   │
└──────────────┬───────────────────────────────────────────┘
               │
        PostgreSQL (metadata + message blobs)

web/ — React Router v7 (Bun) UI: OIDC code flow, session cookie,
       calls the REST API server-side with the user's id_token
```

Two layers:

- **Protocol engine** — `emersion/go-smtp`, `go-imap`, `go-message`,
  `go-msgauth` handle RFC compliance; this repo implements their backends.
- **Management plane** — PostgreSQL storage, outbound queue, address
  resolution, multi-tenancy, OIDC auth, REST API, and the web UI are
  designed and implemented from scratch.

## Identity model

```
account  = one OIDC subject (or a service account)
└── address[]  — mail addresses owned by the account, across any domain
    ├── maro@example.com     (primary, auto-registered on first login)
    ├── hello@example.com    (added by an admin)
    └── *@other-domain.com   (catch-all)
```

| Who | How they authenticate |
|---|---|
| Humans (web UI) | OIDC authorization code flow |
| Mail clients (IMAP/SMTP) | App passwords issued via the web UI |
| Bots / services | Service account + app password (no web login) |

Login gate: the OIDC email's domain must be registered on the server —
first login then JIT-provisions the account (idempotent; re-login after IdP
user re-creation re-attaches by email). Address management is admin-only.

## Tech stack

| Layer | Choice |
|---|---|
| SMTP/IMAP framing | `emersion/go-smtp`, `emersion/go-imap` v2 |
| Message parsing | `emersion/go-message` |
| DKIM/DMARC/SPF | `emersion/go-msgauth`, `blitiri.com.ar/go/spf` |
| Metadata + blobs | PostgreSQL (`jackc/pgx`) |
| Admin/self-service API | Go `net/http` (1.22+ pattern routing), OIDC via `coreos/go-oidc` |
| Web UI | Bun + React Router v7 + Tailwind CSS v4 |
| Deployment | Docker images (distroless-style), Kubernetes-friendly |

## Development

Requirements: Go 1.26+, Bun 1.3+, Docker.

```bash
cp .env.example .env
make up          # dev infra: Postgres (+ Keycloak dev IdP)
make run         # maild on host — IMAP :1143, SMTP :2525/:2587, API :8080

cd web && bun install && bun run dev   # UI on :5573

make db-test     # integration tests (needs the compose Postgres)
make check       # build + vet before committing
make help        # everything else
```

The integration test suite covers the full protocol round trip: SMTP
delivery → IMAP read-back, submission auth, queue retries, DKIM
sign/verify, JIT provisioning, admin API authorization, and the address
model (priority, catch-all, send-as, last-address protection).

Tests share the dev database and truncate it — run with `-p 1` and re-seed
afterwards (`make seed-dev`).

## Configuration

Everything is environment variables — see [.env.example](.env.example).
The essentials:

| Variable | Purpose |
|---|---|
| `MAIL_DSN` | PostgreSQL connection string |
| `MAIL_HOSTNAME` | Server name for EHLO/Received headers |
| `MAIL_TLS_CERT` / `MAIL_TLS_KEY` | TLS for protocol ports (IMAP implicit TLS, SMTP STARTTLS). Empty = plaintext behind a proxy |
| `MAIL_DMARC_ENFORCE` | `true` = honor sender DMARC policy (reject → 550, quarantine → Junk) |
| `MAIL_OIDC_ISSUER` | OIDC issuer URL (empty = dev mode, no auth) |
| `MAIL_OIDC_CLIENT_ID` | Audience for token verification |
| `MAIL_ADMIN_GROUP` | OIDC group granting admin access (default `mail-admin`) |

Domains and outbound relays are managed entirely in the DB through the
admin UI — no bootstrap or relay env vars. Anyone can log in via OIDC;
accounts receive a mailbox only when their email domain is registered
(retroactively backfilled when the domain is added later).

## Project layout

```
cmd/maild/          # the daemon: wires every server together
internal/
  api/              # REST API (admin + self-service), OIDC verification
  auth/             # DKIM sign/verify, SPF, DMARC
  imap/             # go-imap v2 session backend
  queue/            # outbound queue worker, relay resolution, DKIM signing
  smtp/             # MX receive + submission backends
  store/            # domain types + Store/AdminStore interfaces
    migration/      # embedded SQL migrations (run on boot)
    postgres/       # pgx implementation
web/                # React Router v7 UI (Bun)
  app/components/   # atomic UI kit shared across routes
docs/               # design decision records
spikes/             # throwaway learning experiments
```

## Status

Actively developed and running in production for the author's domains.
Current focus (Phase 4): deliverability hardening, anti-spam, TLS on
protocol ports, backups. See commit history for the detailed changelog.

## License

MIT
