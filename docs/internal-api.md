# Internal API (`/internal/v1`) and mail event webhook

A service-to-service surface for one trusted backend (the ara server's maild
adapter). It is not for browsers or people, and it is off unless configured.

## Configuration

| Variable | Purpose |
|---|---|
| `MAIL_INTERNAL_TOKEN` | Service token. Empty = the `/internal/v1` routes are **not registered** (404). |
| `MAIL_EVENT_URL` | Webhook endpoint for mail events. |
| `MAIL_EVENT_SECRET` | HMAC-SHA256 key for the webhook signature. The webhook runs only when both URL and secret are set. |

The routes live on the API listener (`MAIL_API_ADDR`). Call them from inside
the cluster; there is no reason to route `/internal/` through the public
gateway.

## Authentication and account scope

- `Authorization: Bearer <MAIL_INTERNAL_TOKEN>` on every call, compared in
  constant time. Missing or wrong: `401`.
- Every call except the two account-management ones names its target account
  in `X-Mail-Account: <account uuid>`. Missing/malformed: `400`, unknown
  account: `404`.
- The handlers are the webmail (`/api/me`) ones; every store query carries the
  account in its SQL `JOIN`, so another account's message id is a `404` at
  the query level.

## Routes

| Method & path | Body / query | Response |
|---|---|---|
| `POST /internal/v1/account` | `{subject, email, name?}` | `200 {accountId, addressList:[{id,address}]}`. JIT provisioning with the same rules as first OIDC login (kind `user`, `oidc_subject = subject` verbatim, primary address + INBOX when the domain is registered, bare account with `addressList: []` otherwise). Idempotent. `name` is accepted but not stored (accounts have no display name). |
| `POST /internal/v1/account/{id}/address` | `{localPart, domain}` | `201 {id, address}`; taken `409`, unknown domain `400`, `*` (catch-all, admin-only) `400`, unknown account `404`. |
| `GET /internal/v1/mailbox` | | `[{name, messageCount, unseenCount, uidValidity, uidNext}]` (INBOX first; INBOX is created if missing). |
| `GET /internal/v1/sync` | `mailbox=INBOX&sinceUid=0&limit=200` (limit up to 500) | `{uidValidity, uidNext, hasMore, messageList:[{id, uid, messageId, inReplyTo, referenceList, from{name,address}, toList[{name,address}], ccList, subject, date, internalDate, flagList, size, preview, hasAttachment}]}`, UID ascending, `uid > sinceUid`. Unknown mailbox `404`. |
| `GET /internal/v1/flags` | `mailbox=INBOX&fromUid=1` | `{uidValidity, uidNext, truncated, flagList:[{uid, flagList}]}` for every message with `uid >= fromUid`, at most 5000 rows (`truncated` tells). A UID missing from a full listing was expunged. |
| `GET /internal/v1/message/{id}` | | Webmail detail shape (text, sanitized HTML, attachment list). Does **not** set `\Seen`. |
| `GET /internal/v1/message/{id}/raw` | | RFC 822 original (`message/rfc822`). |
| `GET /internal/v1/message/{id}/attachment/{index}` | | One attachment, as the webmail route. |
| `POST /internal/v1/message/batch` | `{idList, action: seen\|unseen\|flag\|unflag\|move\|delete, mailbox?}` | `{done, failed}`, same as `/api/me/message/batch` (delete = to Trash, physical only from Trash). |
| `POST /internal/v1/send` | `{raw: base64 RFC 822, envelopeFrom?, recipientList?}` | `{messageId, queued, delivered}` |

### Send rules

- The bytes are sent as given: no re-encoding, no body change, so an iMIP
  invitation (`text/calendar; method=REQUEST`) arrives intact. DKIM signing
  happens later in the queue worker over the final bytes, as for every
  outbound message.
- Only `Message-ID` and `Date` are **prepended** when missing (existing bytes
  untouched). The response's `messageId` is the one in the message, without
  angle brackets.
- The header `From` must be exactly one address owned by the account
  (`CanSendAs`), else `403`. `envelopeFrom` (default: the header From) must
  be owned too.
- Recipients: `recipientList` if given, else the message's `To` + `Cc`
  (deduplicated, max 50). A `Bcc` header in `raw` is refused (`400`) because
  it would be delivered verbatim; put Bcc recipients in `recipientList`.
- Then the shared delivery path: permission groups and domain switches
  (external send is denied by the default group), the daily limit, local
  direct delivery (filters, quota), external queueing, and a `\Seen` copy in
  the account's Sent. Metrics use `path="internal"`.

## Mail event webhook

When `MAIL_EVENT_URL` and `MAIL_EVENT_SECRET` are set, maild POSTs one JSON
event per change:

```json
{"eventId":"<uuid v7>","kind":"mail.new","accountId":"<uuid>","mailbox":"INBOX",
 "uidValidity":1793785363,"uid":12,"occurredAt":"2026-11-04T09:42:43.745Z"}
```

| kind | when | uid |
|---|---|---|
| `mail.new` | delivery, IMAP APPEND/COPY, Sent/Drafts copies, move into a mailbox | the new UID |
| `mail.flags` | any flag change (IMAP STORE, webmail, batch) | the message UID |
| `mail.expunge` | IMAP EXPUNGE, physical delete, move out of a mailbox | the UID when exactly one message went, omitted otherwise |

Headers: `X-Mail-Signature: sha256=<hex HMAC-SHA256(secret, raw body)>` and
`X-Mail-Event-Id` (= `eventId`). Verify the signature over the exact bytes
received, in constant time.

**An event is a poke, not a record.** It says "this mailbox changed, come
and look"; the receiver pulls the truth with `/sync` and `/flags` from its own
UID cursor. Events can be lost (the LISTEN connection drops, the queue of 1024
overflows, the endpoint stays down past the retry budget) and nothing breaks:
the next event or a periodic sync catches up. Delivery is best-effort: up to
4 attempts with 1s/2s/4s backoff on network errors, 5xx, 408 and 429; other
4xx answers are final. Outcomes are counted in
`mail_event_webhook_total{outcome="delivered|retry|dropped|overflow"}`.

Source: Postgres `pg_notify('mail_event', ...)` fired inside the same
transaction as the change (so a rolled-back change emits nothing), received
by the existing LISTEN Notifier next to `mailbox_change`.

Mailbox deletion and rename emit no event; a consumer notices them through
`/mailbox` (a UIDVALIDITY change or a missing name means "start over").
