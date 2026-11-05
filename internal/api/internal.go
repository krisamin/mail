package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	gomail "github.com/emersion/go-message/mail"

	"github.com/krisamin/mail/internal/store"
)

// Internal API (/internal/v1/*) — the service-to-service surface the ara
// server's maild adapter calls. It is not for browsers and not for people.
//
// Authentication is one static service token (MAIL_INTERNAL_TOKEN) sent as
// "Authorization: Bearer <token>" and compared in constant time. With the env
// empty the routes are not registered at all (404, not 401), so a deployment
// that never opted in exposes nothing.
//
// Account scope: except for provisioning, every call names its target account
// in the X-Mail-Account header (account uuid). The middleware resolves it and
// the handlers reuse the /api/me ones, whose store queries carry the account
// in the SQL JOIN — a message id of another account is a 404 at the query,
// not at a handler check that could be forgotten.

// internalHeaderAccount names the target account of an internal call.
const internalHeaderAccount = "X-Mail-Account"

// Internal limits.
const (
	syncDefaultLimit = 200
	syncMaxLimit     = 500
	flagMaxLimit     = 5000
	// raw send: 25MB message (the SMTP cap) as base64 inside JSON
	internalSendMaxBody  = 36 << 20
	internalSendMaxBytes = 25 << 20
	internalMaxRcpt      = 50
)

type internalCtxKey struct{}

// internalAccountFrom returns the account an internal call acts for (nil
// outside /internal/v1). resolveMe consults it first, which is how the
// /api/me handlers serve the internal routes unchanged.
func internalAccountFrom(ctx context.Context) *store.Account {
	u, _ := ctx.Value(internalCtxKey{}).(*store.Account)
	return u
}

// WithInternal registers /internal/v1/* behind the service token.
// An empty token registers nothing.
func (s *Server) WithInternal(token string) *Server {
	if token == "" {
		return s
	}
	scoped := http.NewServeMux()
	scoped.HandleFunc("GET /internal/v1/mailbox", s.handleInternalMailbox)
	scoped.HandleFunc("GET /internal/v1/sync", s.handleInternalSync)
	scoped.HandleFunc("GET /internal/v1/flags", s.handleInternalFlag)
	scoped.HandleFunc("GET /internal/v1/message/{id}", s.handleInternalMessageDetail)
	scoped.HandleFunc("GET /internal/v1/message/{id}/raw", s.handleMeMessageRaw)
	scoped.HandleFunc("GET /internal/v1/message/{id}/attachment/{index}", s.handleMeAttachment)
	scoped.HandleFunc("POST /internal/v1/message/batch", s.handleMeBatch)
	scoped.HandleFunc("POST /internal/v1/send", s.handleInternalSend)

	in := http.NewServeMux()
	// account management names the account in the body/path, not the header
	in.HandleFunc("POST /internal/v1/account", s.handleInternalProvision)
	in.HandleFunc("POST /internal/v1/account/{id}/address", s.handleInternalAddAddress)
	// admin console (ara /admin/mail): same handlers as /api/admin/*
	in.Handle(internalAdminPrefix, s.internalAdmin())
	in.Handle("/internal/v1/", s.requireInternalAccount(scoped))

	s.mux.Handle("/internal/", requireServiceToken(token, in))
	return s
}

// requireServiceToken admits only requests bearing the service token.
func requireServiceToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(bearerToken(r))
		if len(got) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid service token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireInternalAccount resolves X-Mail-Account into the request context.
// Missing/malformed → 400, unknown → 404.
func (s *Server) requireInternalAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get(internalHeaderAccount))
		if raw == "" {
			writeError(w, http.StatusBadRequest, internalHeaderAccount+" header required")
			return
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid "+internalHeaderAccount)
			return
		}
		u, err := s.store.FindAccountByID(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "account not found")
				return
			}
			mapStoreErr(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), internalCtxKey{}, u)))
	})
}

// ── Accounts ────────────────────────────────────────────────

type internalAddressDTO struct {
	ID      uuid.UUID `json:"id"`
	Address string    `json:"address"`
}

func toInternalAddressDTO(a *store.Address) internalAddressDTO {
	return internalAddressDTO{ID: a.ID, Address: a.LocalPart + "@" + a.DomainName}
}

// handleInternalProvision is JIT provisioning on behalf of ara: same store
// path as /api/me/provision (kind user, primary address + INBOX when the
// email's domain is registered, bare account otherwise; idempotent).
// body: {subject, email, name?} — ara sends subject "ara:<memberId>".
// name is accepted for forward compatibility; accounts have no display name yet.
func (s *Server) handleInternalProvision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject string `json:"subject"`
		Email   string `json:"email"`
		Name    string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	subject := strings.TrimSpace(req.Subject)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if subject == "" || !strings.Contains(email, "@") {
		writeError(w, http.StatusBadRequest, "invalid body (subject and email required)")
		return
	}
	u, err := s.store.ProvisionAccount(r.Context(), subject, email)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	addressList, err := s.store.ListAccountAddress(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]internalAddressDTO, 0, len(addressList))
	for _, a := range addressList {
		out = append(out, toInternalAddressDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"accountId": u.ID, "addressList": out})
}

// handleInternalAddAddress attaches another address to an account.
// body: {localPart, domain}. Unknown domain 400, taken address 409.
// The catch-all marker '*' is admin-only and refused here.
func (s *Server) handleInternalAddAddress(w http.ResponseWriter, r *http.Request) {
	accountID, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		LocalPart string `json:"localPart"`
		Domain    string `json:"domain"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.LocalPart) == "*" {
		writeError(w, http.StatusBadRequest, "invalid address: catch-all is admin-only")
		return
	}
	if _, err := s.store.FindAccountByID(r.Context(), accountID); err != nil {
		mapStoreErr(w, err)
		return
	}
	d, err := s.store.FindDomain(r.Context(), strings.ToLower(strings.TrimSpace(req.Domain)))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "domain not registered: "+req.Domain)
			return
		}
		mapStoreErr(w, err)
		return
	}
	a, err := s.store.CreateAddress(r.Context(), d.ID, req.LocalPart, accountID)
	if err != nil {
		mapStoreErr(w, err) // 23505 → 409
		return
	}
	writeJSON(w, http.StatusCreated, toInternalAddressDTO(a))
}

// ── Mailboxes ───────────────────────────────────────────────

type internalMailboxDTO struct {
	mailboxSummaryDTO
	UIDValidity uint32 `json:"uidValidity"`
	UIDNext     uint32 `json:"uidNext"`
}

// handleInternalMailbox is /api/me/mailbox plus the UID state of each folder.
// INBOX is made real (not synthesised) so its UIDVALIDITY is meaningful.
func (s *Server) handleInternalMailbox(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	if _, err := s.store.EnsureMailbox(r.Context(), u.ID, mailboxInbox); err != nil {
		mapStoreErr(w, err)
		return
	}
	summaryList, err := s.store.ListMailboxSummary(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]internalMailboxDTO, 0, len(summaryList))
	for _, m := range summaryList {
		out = append(out, internalMailboxDTO{
			mailboxSummaryDTO: mailboxSummaryDTO{
				Name: m.Name, MessageCount: m.MessageCount, UnseenCount: m.UnseenCount,
			},
			UIDValidity: m.UIDValidity, UIDNext: m.UIDNext,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Sync ────────────────────────────────────────────────────

type syncAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// syncMeta is the message.sync_meta cache (0006). V lets a later change of
// the parse invalidate old rows (rows with an older V are re-parsed).
type syncMeta struct {
	V             int           `json:"v"`
	MessageID     string        `json:"messageId"`
	InReplyTo     string        `json:"inReplyTo"`
	ReferenceList []string      `json:"referenceList"`
	From          syncAddress   `json:"from"`
	ToList        []syncAddress `json:"toList"`
	CcList        []syncAddress `json:"ccList"`
	Subject       string        `json:"subject"`
	Date          string        `json:"date"`
	HasAttachment bool          `json:"hasAttachment"`
}

const syncMetaVersion = 1

type syncMessageDTO struct {
	ID            uuid.UUID     `json:"id"`
	UID           uint32        `json:"uid"`
	MessageID     string        `json:"messageId"`
	InReplyTo     string        `json:"inReplyTo"`
	ReferenceList []string      `json:"referenceList"`
	From          syncAddress   `json:"from"`
	ToList        []syncAddress `json:"toList"`
	CcList        []syncAddress `json:"ccList"`
	Subject       string        `json:"subject"`
	// Date is the Date header (RFC3339, UTC); the delivery time when absent.
	Date          string   `json:"date"`
	InternalDate  string   `json:"internalDate"`
	FlagList      []string `json:"flagList"`
	Size          int64    `json:"size"`
	Preview       string   `json:"preview"`
	HasAttachment bool     `json:"hasAttachment"`
}

func toSyncAddressList(list []*gomail.Address) []syncAddress {
	out := make([]syncAddress, 0, len(list))
	for _, a := range list {
		out = append(out, syncAddress{Name: a.Name, Address: strings.ToLower(a.Address)})
	}
	return out
}

// parseSyncMeta extracts the sync facts from a raw message. Best-effort: a
// broken message yields whatever could be read, never an error (the row is
// cached either way so a spam mail is not re-parsed on every sync).
func parseSyncMeta(raw []byte) syncMeta {
	meta := syncMeta{V: syncMetaVersion, ReferenceList: []string{}, ToList: []syncAddress{}, CcList: []syncAddress{}}
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return meta
	}
	h := mr.Header
	if id, err := h.MessageID(); err == nil {
		meta.MessageID = id
	}
	if list, err := h.MsgIDList("In-Reply-To"); err == nil && len(list) > 0 {
		meta.InReplyTo = list[0]
	}
	if list, err := h.MsgIDList("References"); err == nil {
		meta.ReferenceList = append(meta.ReferenceList, list...)
	}
	if list, err := h.AddressList("From"); err == nil && len(list) > 0 {
		meta.From = syncAddress{Name: list[0].Name, Address: strings.ToLower(list[0].Address)}
	}
	if list, err := h.AddressList("To"); err == nil {
		meta.ToList = toSyncAddressList(list)
	}
	if list, err := h.AddressList("Cc"); err == nil {
		meta.CcList = toSyncAddressList(list)
	}
	if subject, err := h.Subject(); err == nil {
		meta.Subject = subject
	}
	if d, err := h.Date(); err == nil && !d.IsZero() {
		meta.Date = d.UTC().Format(time.RFC3339)
	}
	for {
		part, err := mr.NextPart()
		if err != nil { // io.EOF or a broken part — stop either way
			break
		}
		if _, ok := part.Header.(*gomail.AttachmentHeader); ok {
			meta.HasAttachment = true
			break
		}
		_, _ = io.Copy(io.Discard, part.Body)
	}
	return meta
}

// syncMetaOf returns the cached meta or parses (and caches) it now.
func (s *Server) syncMetaOf(r *http.Request, accountID uuid.UUID, m *store.Message) syncMeta {
	if len(m.SyncMeta) > 0 {
		var meta syncMeta
		if err := json.Unmarshal(m.SyncMeta, &meta); err == nil && meta.V == syncMetaVersion {
			return meta
		}
	}
	raw, err := s.store.GetMessageBlob(r.Context(), m.ID)
	if err != nil {
		return syncMeta{ReferenceList: []string{}, ToList: []syncAddress{}, CcList: []syncAddress{},
			From: syncAddress{Address: m.FromAddr}, Subject: m.Subject}
	}
	meta := parseSyncMeta(raw)
	preview, toAddr := previewOf(raw)
	if m.Preview == nil {
		m.Preview = &preview
	}
	if encoded, err := json.Marshal(meta); err == nil {
		if err := s.store.SetMessageSyncMeta(r.Context(), accountID, m.ID, encoded, preview, toAddr); err != nil {
			log.Printf("api: sync cache write failed message=%s: %v", m.ID, err)
		}
	}
	return meta
}

// handleInternalSync lists messages after a UID cursor, oldest first.
// ?mailbox=INBOX&sinceUid=0&limit=200 — the caller stores the last uid it
// saw and asks again with it; uidValidity changing means "start over".
func (s *Server) handleInternalSync(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	q := r.URL.Query()
	mailboxName := q.Get("mailbox")
	if mailboxName == "" {
		mailboxName = mailboxInbox
	}
	sinceUID, err := parseUintParam(q.Get("sinceUid"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid sinceUid")
		return
	}
	limit, err := parseUintParam(q.Get("limit"), syncDefaultLimit)
	if err != nil || limit == 0 {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	if limit > syncMaxLimit {
		limit = syncMaxLimit
	}

	messageList, err := s.store.ListMessageSince(r.Context(), u.ID, mailboxName, sinceUID, int(limit))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	// read the mailbox after the list so uidNext is past every listed uid
	box, err := s.store.GetMailbox(r.Context(), u.ID, mailboxName)
	if err != nil {
		mapStoreErr(w, err)
		return
	}

	out := make([]syncMessageDTO, 0, len(messageList))
	for _, m := range messageList {
		meta := s.syncMetaOf(r, u.ID, m)
		dto := syncMessageDTO{
			ID: m.ID, UID: m.UID,
			MessageID: meta.MessageID, InReplyTo: meta.InReplyTo, ReferenceList: meta.ReferenceList,
			From: meta.From, ToList: meta.ToList, CcList: meta.CcList,
			Subject:       meta.Subject,
			Date:          meta.Date,
			InternalDate:  m.InternalDate.UTC().Format(time.RFC3339),
			FlagList:      m.Flags,
			Size:          m.SizeBytes,
			HasAttachment: meta.HasAttachment,
		}
		if dto.Date == "" {
			dto.Date = dto.InternalDate
		}
		if dto.Subject == "" {
			dto.Subject = m.Subject
		}
		if dto.From.Address == "" {
			dto.From.Address = m.FromAddr
		}
		if dto.FlagList == nil {
			dto.FlagList = []string{}
		}
		if dto.ReferenceList == nil {
			dto.ReferenceList = []string{}
		}
		if dto.ToList == nil {
			dto.ToList = []syncAddress{}
		}
		if dto.CcList == nil {
			dto.CcList = []syncAddress{}
		}
		if m.Preview != nil {
			dto.Preview = *m.Preview
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uidValidity": box.UIDValidity,
		"uidNext":     box.UIDNext,
		"messageList": out,
		// a full page means there may be more after the last uid
		"hasMore": len(messageList) == int(limit),
	})
}

// handleInternalFlag lists (uid, flags) from a UID on, capped at 5000 rows.
// ?mailbox=INBOX&fromUid=1 — uids missing from a full listing were expunged.
func (s *Server) handleInternalFlag(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	q := r.URL.Query()
	mailboxName := q.Get("mailbox")
	if mailboxName == "" {
		mailboxName = mailboxInbox
	}
	fromUID, err := parseUintParam(q.Get("fromUid"), 1)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid fromUid")
		return
	}
	box, err := s.store.GetMailbox(r.Context(), u.ID, mailboxName)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	// one extra row tells whether the cap cut the listing
	rowList, err := s.store.ListFlagSince(r.Context(), u.ID, mailboxName, fromUID, flagMaxLimit+1)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	truncated := len(rowList) > flagMaxLimit
	if truncated {
		rowList = rowList[:flagMaxLimit]
	}
	type flagRow struct {
		UID      uint32   `json:"uid"`
		FlagList []string `json:"flagList"`
	}
	out := make([]flagRow, 0, len(rowList))
	for _, row := range rowList {
		out = append(out, flagRow{UID: row.UID, FlagList: row.FlagList})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"uidValidity": box.UIDValidity,
		"uidNext":     box.UIDNext,
		"flagList":    out,
		"truncated":   truncated,
	})
}

func parseUintParam(raw string, fallback uint32) (uint32, error) {
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseUint(raw, 10, 32)
	return uint32(v), err
}

// ── Message ─────────────────────────────────────────────────

// handleInternalMessageDetail is the webmail detail without the implicit
// \Seen — ara decides read state itself (via batch).
func (s *Server) handleInternalMessageDetail(w http.ResponseWriter, r *http.Request) {
	s.messageDetail(w, r, false)
}

// ── Send ────────────────────────────────────────────────────

// handleInternalSend sends a message ara composed itself (any MIME, e.g. an
// iMIP invitation with a text/calendar part).
// body: {raw: base64 RFC822, envelopeFrom?, recipientList?}
//
// The bytes go out as given: no re-encoding, no body change. Only two headers
// may be prepended when missing — Message-ID and Date — which leaves the body
// and every existing header untouched (DKIM is signed later by the queue
// worker, over the final bytes). The header From must be an owned address
// (CanSendAs), as must envelopeFrom when given; then the shared delivery path
// applies group permissions, the daily limit and the external-send policy.
// Recipients default to To+Cc of the message; a Bcc header is refused
// (it would be delivered verbatim to everyone) — Bcc goes in recipientList.
func (s *Server) handleInternalSend(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, internalSendMaxBody)
	var req struct {
		Raw           string   `json:"raw"`
		EnvelopeFrom  string   `json:"envelopeFrom"`
		RecipientList []string `json:"recipientList"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.Raw)
	if err != nil || len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "invalid raw (base64 RFC822 required)")
		return
	}
	if len(raw) > internalSendMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "message too large")
		return
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		writeError(w, http.StatusBadRequest, "unparseable message header")
		return
	}
	if msg.Header.Get("Bcc") != "" {
		writeError(w, http.StatusBadRequest, "Bcc header not allowed in raw (use recipientList)")
		return
	}

	// header From — the identity recipients see, and what DKIM aligns with
	fromList, err := msg.Header.AddressList("From")
	if err != nil || len(fromList) != 1 {
		writeError(w, http.StatusBadRequest, "exactly one From address required")
		return
	}
	from := strings.ToLower(fromList[0].Address)
	if ok, err := s.store.CanSendAs(r.Context(), u.ID, from); err != nil {
		mapStoreErr(w, err)
		return
	} else if !ok {
		writeError(w, http.StatusForbidden, "From must be an owned address")
		return
	}
	envelopeFrom := from
	if e := strings.TrimSpace(req.EnvelopeFrom); e != "" {
		addr, err := mail.ParseAddress(e)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid envelopeFrom")
			return
		}
		envelopeFrom = strings.ToLower(addr.Address)
		if envelopeFrom != from {
			if ok, err := s.store.CanSendAs(r.Context(), u.ID, envelopeFrom); err != nil {
				mapStoreErr(w, err)
				return
			} else if !ok {
				writeError(w, http.StatusForbidden, "envelopeFrom must be an owned address")
				return
			}
		}
	}

	// recipients: explicit list, else the message's To+Cc
	candidateList := req.RecipientList
	if len(candidateList) == 0 {
		for _, key := range []string{"To", "Cc"} {
			list, err := msg.Header.AddressList(key)
			if err != nil && !errors.Is(err, mail.ErrHeaderNotPresent) {
				writeError(w, http.StatusBadRequest, "invalid "+key+" header")
				return
			}
			for _, a := range list {
				candidateList = append(candidateList, a.Address)
			}
		}
	}
	var rcptList []string
	seen := map[string]bool{}
	for _, c := range candidateList {
		addr, err := mail.ParseAddress(strings.TrimSpace(c))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid recipient: "+c)
			return
		}
		a := strings.ToLower(addr.Address)
		if !seen[a] {
			seen[a] = true
			rcptList = append(rcptList, a)
		}
	}
	if len(rcptList) == 0 {
		writeError(w, http.StatusBadRequest, "no recipients")
		return
	}
	if len(rcptList) > internalMaxRcpt {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many recipients (max %d)", internalMaxRcpt))
		return
	}

	// Message-ID/Date are prepended only when absent (body untouched)
	messageID := strings.Trim(strings.TrimSpace(msg.Header.Get("Message-Id")), "<>")
	var prefix bytes.Buffer
	if messageID == "" {
		host := s.hostname
		if host == "" {
			host = "localhost"
		}
		messageID = uuid.NewString() + "@" + host
		fmt.Fprintf(&prefix, "Message-ID: <%s>\r\n", messageID)
	}
	if msg.Header.Get("Date") == "" {
		fmt.Fprintf(&prefix, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	}
	if prefix.Len() > 0 {
		raw = append(prefix.Bytes(), raw...)
	}

	localCount, externalCount, ok := s.deliverOutgoing(w, r, u, envelopeFrom, rcptList, raw, "internal")
	if !ok {
		return
	}
	log.Printf("api: internal send account=%s from=%s local=%d external=%d size=%d",
		u.ID, envelopeFrom, localCount, externalCount, len(raw))
	writeJSON(w, http.StatusOK, map[string]any{
		"messageId": messageID,
		"queued":    externalCount,
		"delivered": localCount,
	})
}
