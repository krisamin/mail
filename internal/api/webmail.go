package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	// registers message.CharsetReader — without it non-UTF-8 mail
	// (EUC-KR, ISO-2022-JP, ...) fails to decode.
	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"

	"github.com/krisamin/mail/internal/filter"
	"github.com/krisamin/mail/internal/store"
)

// Webmail API (/api/me/mailbox, /api/me/message*) — the signed-in user reads
// and sends their own mail from the web UI. Same identity model as the rest
// of self-service: OIDC sub → account; every store query is account-scoped.
//
// Reading: mailbox summaries, cursor-paged lists, parsed detail (text + html
// + attachment manifest), raw part download.
// Writing: flag patch (seen/flagged), move, two-step delete (→Trash, then
// physical), and send (local direct delivery + external via outbound queue —
// the exact split submission uses, including the header-From ownership check).

// sendMaxBytes caps the JSON compose payload (text-only compose — a far cry
// from the 25MB SMTP cap; attachments come later via multipart).
const sendMaxBytes = 5 << 20

// ── DTO ─────────────────────────────────────────────────────

type mailboxSummaryDTO struct {
	Name         string `json:"name"`
	MessageCount uint32 `json:"messageCount"`
	UnseenCount  uint32 `json:"unseenCount"`
}

type messageRowDTO struct {
	ID           uuid.UUID `json:"id"`
	UID          uint32    `json:"uid"`
	Subject      string    `json:"subject"`
	FromAddr     string    `json:"fromAddr"`
	InternalDate string    `json:"internalDate"`
	SizeBytes    int64     `json:"sizeBytes"`
	Seen         bool      `json:"seen"`
	Flagged      bool      `json:"flagged"`
	Answered     bool      `json:"answered"`
}

type attachmentDTO struct {
	Index       int    `json:"index"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int    `json:"sizeBytes"`
}

type messageDetailDTO struct {
	messageRowDTO
	Mailbox        string          `json:"mailbox"`
	ToList         []string        `json:"toList"`
	CcList         []string        `json:"ccList"`
	ReplyTo        string          `json:"replyTo,omitempty"`
	MessageID      string          `json:"messageId,omitempty"`
	Date           string          `json:"date,omitempty"`
	TextBody       string          `json:"textBody"`
	HTMLBody       string          `json:"htmlBody"`
	AttachmentList []attachmentDTO `json:"attachmentList"`
	ParseWarn      string          `json:"parseWarn,omitempty"`
}

func toMessageRowDTO(m *store.Message) messageRowDTO {
	dto := messageRowDTO{
		ID: m.ID, UID: m.UID, Subject: m.Subject, FromAddr: m.FromAddr,
		InternalDate: m.InternalDate.UTC().Format(time.RFC3339),
		SizeBytes:    m.SizeBytes,
	}
	for _, f := range m.Flags {
		switch f {
		case "\\Seen":
			dto.Seen = true
		case "\\Flagged":
			dto.Flagged = true
		case "\\Answered":
			dto.Answered = true
		}
	}
	return dto
}

// ── Mailboxes ───────────────────────────────────────────────

func (s *Server) handleMeMailbox(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	summaryList, err := s.store.ListMailboxSummary(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	out := make([]mailboxSummaryDTO, 0, len(summaryList)+1)
	hasInbox := false
	for _, m := range summaryList {
		if m.Name == "INBOX" {
			hasInbox = true
		}
		out = append(out, mailboxSummaryDTO{
			Name: m.Name, MessageCount: m.MessageCount, UnseenCount: m.UnseenCount,
		})
	}
	// INBOX always exists in the response — a fresh account that has never
	// received mail would otherwise render an empty sidebar (the row is
	// created on first delivery/selection, but the UI must not wait for it).
	if !hasInbox {
		out = append([]mailboxSummaryDTO{{Name: "INBOX"}}, out...)
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Message list / detail ───────────────────────────────────

// handleMeMessageList pages one mailbox newest-first.
// ?mailbox=INBOX&before=<uid|0>&limit=<n> — before=0 is the newest page,
// nextBefore in the response feeds the next request (0 = no more pages).
func (s *Server) handleMeMessageList(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	mailbox := r.URL.Query().Get("mailbox")
	if mailbox == "" {
		mailbox = "INBOX"
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 32)

	messageList, err := s.store.ListMessagePage(r.Context(), u.ID, mailbox, limit, uint32(before))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	rowList := make([]messageRowDTO, 0, len(messageList))
	for _, m := range messageList {
		rowList = append(rowList, toMessageRowDTO(m))
	}
	// full page → probably more below the last UID; short page → done
	var nextBefore uint32
	if len(messageList) == limit && limit > 0 {
		nextBefore = messageList[len(messageList)-1].UID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messageList": rowList,
		"nextBefore":  nextBefore,
	})
}

func (s *Server) handleMeMessageDetail(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	m, mailboxName, err := s.store.GetAccountMessage(r.Context(), u.ID, id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	raw, err := s.store.GetMessageBlob(r.Context(), m.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}

	detail := messageDetailDTO{messageRowDTO: toMessageRowDTO(m), Mailbox: mailboxName}
	parseMessageBody(raw, &detail)

	// opening a message marks it read (like every mail client). Non-fatal.
	if !detail.Seen {
		flagList := append(append([]string{}, m.Flags...), "\\Seen")
		if err := s.store.SetAccountMessageFlag(r.Context(), u.ID, m.ID, flagList); err == nil {
			detail.Seen = true
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

// parseMessageBody walks the MIME structure into the DTO. Parse failures are
// recorded (ParseWarn) instead of failing the request — a malformed spam mail
// must still render its metadata.
func parseMessageBody(raw []byte, out *messageDetailDTO) {
	out.AttachmentList = []attachmentDTO{}
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		out.ParseWarn = "unparseable message body"
		return
	}
	h := mr.Header
	if list, err := h.AddressList("To"); err == nil {
		for _, a := range list {
			out.ToList = append(out.ToList, formatAddress(a))
		}
	}
	if list, err := h.AddressList("Cc"); err == nil {
		for _, a := range list {
			out.CcList = append(out.CcList, formatAddress(a))
		}
	}
	if list, err := h.AddressList("Reply-To"); err == nil && len(list) > 0 {
		out.ReplyTo = list[0].Address
	}
	if id, err := h.MessageID(); err == nil {
		out.MessageID = id
	}
	if d, err := h.Date(); err == nil {
		out.Date = d.UTC().Format(time.RFC3339)
	}

	index := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			out.ParseWarn = "some parts could not be parsed"
			break
		}
		switch ph := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := ph.ContentType()
			body, err := io.ReadAll(part.Body)
			if err != nil {
				out.ParseWarn = "some parts could not be read"
				continue
			}
			switch {
			// first part of each kind wins (multipart/alternative order)
			case ct == "text/plain" && out.TextBody == "":
				out.TextBody = string(body)
			case ct == "text/html" && out.HTMLBody == "":
				out.HTMLBody = string(body)
			}
		case *gomail.AttachmentHeader:
			filename, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			// size = decoded size; read to count without keeping the bytes
			n, _ := io.Copy(io.Discard, part.Body)
			out.AttachmentList = append(out.AttachmentList, attachmentDTO{
				Index: index, Filename: filename, ContentType: ct, SizeBytes: int(n),
			})
		}
		index++
	}
}

func formatAddress(a *gomail.Address) string {
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, a.Address)
	}
	return a.Address
}

// handleMeAttachment streams one attachment by walk index.
// The index space is shared with parseMessageBody (same walk order), so the
// detail response's attachment indexes are directly usable here.
func (s *Server) handleMeAttachment(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	wantIndex, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || wantIndex < 0 {
		writeError(w, http.StatusBadRequest, "invalid index")
		return
	}
	m, _, err := s.store.GetAccountMessage(r.Context(), u.ID, id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	raw, err := s.store.GetMessageBlob(r.Context(), m.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "unparseable message")
		return
	}
	index := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			break
		}
		if index == wantIndex {
			ah, ok := part.Header.(*gomail.AttachmentHeader)
			if !ok {
				writeError(w, http.StatusNotFound, "not an attachment")
				return
			}
			filename, _ := ah.Filename()
			ct, _, _ := ah.ContentType()
			if ct == "" {
				ct = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition",
				mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
			// attachments render as downloads, never inline — a text/html
			// attachment must not execute in the app origin
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = io.Copy(w, part.Body)
			return
		}
		index++
	}
	writeError(w, http.StatusNotFound, "attachment not found")
}

// handleMeMessageRaw downloads the original .eml.
func (s *Server) handleMeMessageRaw(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	m, _, err := s.store.GetAccountMessage(r.Context(), u.ID, id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	raw, err := s.store.GetMessageBlob(r.Context(), m.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{
			"filename": fmt.Sprintf("message-%d.eml", m.ID),
		}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

// ── Flags / move / delete ───────────────────────────────────

// handleMePatchMessage updates seen/flagged. Only the given fields change.
func (s *Server) handleMePatchMessage(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Seen    *bool `json:"seen"`
		Flagged *bool `json:"flagged"`
	}
	if err := decodeBody(r, &req); err != nil || (req.Seen == nil && req.Flagged == nil) {
		writeError(w, http.StatusBadRequest, "invalid body (seen or flagged required)")
		return
	}
	m, _, err := s.store.GetAccountMessage(r.Context(), u.ID, id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	flagSet := map[string]bool{}
	for _, f := range m.Flags {
		flagSet[f] = true
	}
	if req.Seen != nil {
		flagSet["\\Seen"] = *req.Seen
	}
	if req.Flagged != nil {
		flagSet["\\Flagged"] = *req.Flagged
	}
	var flagList []string
	for f, on := range flagSet {
		if on {
			flagList = append(flagList, f)
		}
	}
	if err := s.store.SetAccountMessageFlag(r.Context(), u.ID, id, flagList); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeMoveMessage moves to another mailbox (created on demand).
func (s *Server) handleMeMoveMessage(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Mailbox string `json:"mailbox"`
	}
	if err := decodeBody(r, &req); err != nil || strings.TrimSpace(req.Mailbox) == "" {
		writeError(w, http.StatusBadRequest, "invalid body (mailbox required)")
		return
	}
	if err := s.store.MoveAccountMessage(r.Context(), u.ID, id, req.Mailbox); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeDeleteMessage: in Trash → physical delete, elsewhere → move to
// Trash (the two-step delete every client implements).
func (s *Server) handleMeDeleteMessage(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	_, mailboxName, err := s.store.GetAccountMessage(r.Context(), u.ID, id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	if mailboxName == "Trash" {
		err = s.store.DeleteAccountMessage(r.Context(), u.ID, id)
	} else {
		err = s.store.MoveAccountMessage(r.Context(), u.ID, id, "Trash")
	}
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Send ────────────────────────────────────────────────────

// handleMeSendMessage composes and delivers mail (text-only v1).
// Same policy as SMTP submission: the From must be an owned address
// (CanSendAs), local recipients are delivered directly, external recipients
// go through the outbound queue (DKIM signing happens in the worker), and a
// copy lands in Sent. Failure → the whole request errors (at-least-once).
func (s *Server) handleMeSendMessage(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sendMaxBytes)
	var req struct {
		From      string   `json:"from"`
		ToList    []string `json:"toList"`
		CcList    []string `json:"ccList"`
		Subject   string   `json:"subject"`
		TextBody  string   `json:"textBody"`
		InReplyTo string   `json:"inReplyTo"` // optional message id (uuid) being replied to
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	from := strings.ToLower(strings.TrimSpace(req.From))
	if from == "" || len(req.ToList) == 0 {
		writeError(w, http.StatusBadRequest, "invalid body (from and toList required)")
		return
	}

	// sender ownership — the exact same defense submission runs on MAIL FROM
	ok, err := s.store.CanSendAs(r.Context(), u.ID, from)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "from must be an owned address")
		return
	}

	// validate + normalize recipients
	var rcptList []string
	for _, list := range [][]string{req.ToList, req.CcList} {
		for _, a := range list {
			addr, err := mail.ParseAddress(strings.TrimSpace(a))
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid recipient: "+a)
				return
			}
			rcptList = append(rcptList, strings.ToLower(addr.Address))
		}
	}

	// reply threading — from the message being replied to
	var inReplyToID string
	if replyID, err := uuid.Parse(req.InReplyTo); err == nil {
		if orig, _, err := s.store.GetAccountMessage(r.Context(), u.ID, replyID); err == nil {
			if raw, err := s.store.GetMessageBlob(r.Context(), orig.ID); err == nil {
				var d messageDetailDTO
				parseMessageBody(raw, &d)
				inReplyToID = d.MessageID
			}
		}
	}

	raw, err := buildOutgoingMessage(s.hostname, from, req.ToList, req.CcList,
		req.Subject, req.TextBody, inReplyToID, time.Now())
	if err != nil {
		log.Printf("api: message build failed: %v", err)
		writeError(w, http.StatusInternalServerError, "message build failed")
		return
	}

	// split recipients: our domain → direct delivery, otherwise → queue
	var localList, externalList []string
	for _, rcptAddr := range rcptList {
		at := strings.LastIndex(rcptAddr, "@")
		if at < 0 {
			writeError(w, http.StatusBadRequest, "invalid recipient: "+rcptAddr)
			return
		}
		if _, err := s.store.FindDomain(r.Context(), rcptAddr[at+1:]); err == nil {
			localList = append(localList, rcptAddr)
		} else if errors.Is(err, store.ErrNotFound) {
			externalList = append(externalList, rcptAddr)
		} else {
			mapStoreErr(w, err)
			return
		}
	}
	// local recipients must exist — mirror submission's 550 at RCPT time
	for _, rcptAddr := range localList {
		if _, err := s.store.ResolveAddress(r.Context(), rcptAddr); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "no such user: "+rcptAddr)
				return
			}
			mapStoreErr(w, err)
			return
		}
	}

	// enqueue external first — if the queue insert fails nothing was
	// delivered yet and the client can simply retry
	if len(externalList) > 0 {
		if err := s.store.EnqueueOutbound(r.Context(), from, externalList, raw); err != nil {
			mapStoreErr(w, err)
			return
		}
	}
	for _, rcptAddr := range localList {
		acct, err := s.store.ResolveAddress(r.Context(), rcptAddr)
		if err != nil {
			mapStoreErr(w, err)
			return
		}
		// recipient filter rules apply to webmail-delivered mail too —
		// same semantics as the SMTP delivery path
		folder := "INBOX"
		var flagList []string
		if v := filter.Evaluate(r.Context(), s.store, acct.ID, raw); v.Discard {
			log.Printf("api: filter discard rule=%q to=%s from=%s", v.RuleName, rcptAddr, from)
			continue
		} else {
			if v.Mailbox != "" {
				folder = v.Mailbox
			}
			flagList = v.FlagList
		}
		box, err := s.store.EnsureMailbox(r.Context(), acct.ID, folder)
		if err != nil {
			mapStoreErr(w, err)
			return
		}
		if _, err := s.store.AppendMessage(r.Context(), box.ID, raw, flagList, time.Now()); err != nil {
			mapStoreErr(w, err)
			return
		}
	}

	// Sent copy (\Seen — you already read what you wrote). Failure is warned,
	// not fatal: the mail is out, a missing Sent copy must not error the send.
	if box, err := s.store.EnsureMailbox(r.Context(), u.ID, "Sent"); err == nil {
		if _, err := s.store.AppendMessage(r.Context(), box.ID, raw, []string{"\\Seen"}, time.Now()); err != nil {
			log.Printf("api: Sent copy failed account=%s: %v", u.ID, err)
		}
	}

	// mark the original answered (best-effort)
	if replyID, err := uuid.Parse(req.InReplyTo); err == nil {
		if orig, _, err := s.store.GetAccountMessage(r.Context(), u.ID, replyID); err == nil {
			has := false
			for _, f := range orig.Flags {
				if f == "\\Answered" {
					has = true
				}
			}
			if !has {
				_ = s.store.SetAccountMessageFlag(r.Context(), u.ID, orig.ID,
					append(append([]string{}, orig.Flags...), "\\Answered"))
			}
		}
	}

	log.Printf("api: webmail send from=%s local=%d external=%d size=%d",
		from, len(localList), len(externalList), len(raw))
	writeJSON(w, http.StatusOK, map[string]any{
		"delivered": len(localList),
		"queued":    len(externalList),
	})
}

// buildOutgoingMessage assembles an RFC 5322 text/plain message.
func buildOutgoingMessage(hostname, from string, toList, ccList []string,
	subject, textBody, inReplyToID string, now time.Time) ([]byte, error) {
	var h gomail.Header
	h.SetDate(now)
	h.SetSubject(subject)
	fromAddr, err := gomail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("from parse: %w", err)
	}
	h.SetAddressList("From", []*gomail.Address{fromAddr})
	to, err := gomail.ParseAddressList(strings.Join(toList, ", "))
	if err != nil {
		return nil, fmt.Errorf("to parse: %w", err)
	}
	h.SetAddressList("To", to)
	if len(ccList) > 0 {
		cc, err := gomail.ParseAddressList(strings.Join(ccList, ", "))
		if err != nil {
			return nil, fmt.Errorf("cc parse: %w", err)
		}
		h.SetAddressList("Cc", cc)
	}
	if err := h.GenerateMessageIDWithHostname(hostname); err != nil {
		return nil, fmt.Errorf("message-id: %w", err)
	}
	if inReplyToID != "" {
		h.SetMsgIDList("In-Reply-To", []string{inReplyToID})
		h.SetMsgIDList("References", []string{inReplyToID})
	}
	h.Set("X-Mailer", "maild-webmail")
	h.Set("Content-Type", "text/plain; charset=utf-8")

	var buf bytes.Buffer
	body, err := gomail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, fmt.Errorf("writer: %w", err)
	}
	if _, err := io.WriteString(body, textBody); err != nil {
		return nil, fmt.Errorf("body write: %w", err)
	}
	if err := body.Close(); err != nil {
		return nil, fmt.Errorf("body close: %w", err)
	}
	return buf.Bytes(), nil
}
