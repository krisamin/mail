package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/microcosm-cc/bluemonday"

	_ "github.com/emersion/go-message/charset"
	gomail "github.com/emersion/go-message/mail"

	"github.com/krisamin/mail/internal/store"
)

// Webmail actions that operate on many messages at once, on mailboxes, and on
// drafts — everything a mail client needs beyond "read one message".
//
// Batch endpoints exist because the UI selects rows with checkboxes: without
// them a ten-message cleanup is ten round trips, each re-rendering the list.

// Well-known mailbox names. IMAP folder names are strings on the wire, so
// the app pins the ones it creates and protects in one place.
const (
	mailboxInbox   = "INBOX"
	mailboxSent    = "Sent"
	mailboxDraft   = "Drafts"
	mailboxTrash   = "Trash"
	mailboxJunk    = "Junk"
	mailboxArchive = "Archive"
)

// previewLimit is how much plain text the list preview keeps per message.
const previewLimit = 180

// batchLimit caps one batch call — the UI never selects more than a page.
const batchLimit = 200

// htmlPolicy sanitises HTML bodies server-side. The web UI additionally
// renders them inside a sandboxed iframe with a restrictive CSP, so this is
// the inner of two walls: scripts, objects, forms and event handlers are gone
// before the markup ever reaches a browser.
var htmlPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("style").OnElements("span", "div", "p", "td", "th", "table", "tr", "a", "h1", "h2", "h3", "font")
	p.AllowAttrs("align", "width", "height", "bgcolor", "cellpadding", "cellspacing", "border").Globally()
	p.AllowAttrs("color", "face", "size").OnElements("font")
	p.AllowElements("table", "thead", "tbody", "tr", "td", "th", "center", "font")
	p.AllowImages()
	p.AllowStandardURLs()
	p.RequireNoFollowOnLinks(true)
	p.AddTargetBlankToFullyQualifiedLinks(true)
	return p
}()

// sanitizeHTML strips anything executable out of a mail HTML body.
func sanitizeHTML(raw string) string {
	if raw == "" {
		return ""
	}
	return htmlPolicy.Sanitize(raw)
}

// previewOf extracts the list preview and recipient cache from a raw message.
// Both are best-effort: an unparseable body yields empty strings, which the
// caller stores anyway so the parse is not retried on every listing.
func previewOf(raw []byte) (preview, toAddr string) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", ""
	}
	if list, err := mr.Header.AddressList("To"); err == nil && len(list) > 0 {
		partList := make([]string, 0, len(list))
		for _, a := range list {
			partList = append(partList, formatAddress(a))
		}
		toAddr = strings.Join(partList, ", ")
	}
	var text, html string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		inline, ok := part.Header.(*gomail.InlineHeader)
		if !ok {
			continue
		}
		ct, _, _ := inline.ContentType()
		body, err := io.ReadAll(io.LimitReader(part.Body, 64<<10))
		if err != nil {
			continue
		}
		if ct == "text/plain" && text == "" {
			text = string(body)
		}
		if ct == "text/html" && html == "" {
			html = string(body)
		}
		if text != "" {
			break
		}
	}
	if text == "" && html != "" {
		text = bluemonday.StrictPolicy().Sanitize(html)
	}
	return squash(text), toAddr
}

// squash collapses whitespace and trims the preview to previewLimit runes.
func squash(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	runeList := []rune(s)
	if len(runeList) > previewLimit {
		return strings.TrimSpace(string(runeList[:previewLimit])) + "…"
	}
	return s
}

// fillCache computes preview/to_addr for rows that never had them (mail that
// predates the cache columns, or delivery paths that skip parsing) and writes
// them back so the work happens exactly once per message.
func (s *Server) fillCache(r *http.Request, accountID uuid.UUID, messageList []*store.Message) {
	for _, m := range messageList {
		if m.Preview != nil {
			continue
		}
		raw, err := s.store.GetMessageBlob(r.Context(), m.ID)
		if err != nil {
			continue
		}
		preview, toAddr := previewOf(raw)
		if err := s.store.SetMessageCache(r.Context(), accountID, m.ID, preview, toAddr); err != nil {
			continue
		}
		m.Preview = &preview
		m.ToAddr = &toAddr
	}
}

// ── Batch actions ───────────────────────────────────────────

// handleMeBatch applies one action to a set of messages.
// body: {idList: [...], action: "seen"|"unseen"|"flag"|"unflag"|"move"|"delete", mailbox?: "Archive"}
//
// Each message is applied independently and failures are counted rather than
// aborting: a stale ID in the selection (someone else's client moved it)
// must not cancel the other nineteen.
func (s *Server) handleMeBatch(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	var req struct {
		IDList  []string `json:"idList"`
		Action  string   `json:"action"`
		Mailbox string   `json:"mailbox"`
	}
	if err := decodeBody(r, &req); err != nil || len(req.IDList) == 0 {
		writeError(w, http.StatusBadRequest, "invalid body (idList required)")
		return
	}
	if len(req.IDList) > batchLimit {
		writeError(w, http.StatusBadRequest, "too many messages in one batch")
		return
	}
	if req.Action == "move" && req.Mailbox == "" {
		writeError(w, http.StatusBadRequest, "mailbox required for move")
		return
	}

	done, failed := 0, 0
	for _, rawID := range req.IDList {
		id, err := uuid.Parse(rawID)
		if err != nil {
			failed++
			continue
		}
		if err := s.applyBatchAction(r, u.ID, id, req.Action, req.Mailbox); err != nil {
			failed++
			continue
		}
		done++
	}
	writeJSON(w, http.StatusOK, map[string]int{"done": done, "failed": failed})
}

// applyBatchAction runs one action on one message.
func (s *Server) applyBatchAction(r *http.Request, accountID, messageID uuid.UUID, action, mailbox string) error {
	ctx := r.Context()
	switch action {
	case "seen", "unseen", "flag", "unflag":
		m, _, err := s.store.GetAccountMessage(ctx, accountID, messageID)
		if err != nil {
			return err
		}
		flag := "\\Seen"
		add := action == "seen"
		if action == "flag" || action == "unflag" {
			flag = "\\Flagged"
			add = action == "flag"
		}
		return s.store.SetAccountMessageFlag(ctx, accountID, messageID, withFlag(m.Flags, flag, add))
	case "move":
		return s.store.MoveAccountMessage(ctx, accountID, messageID, mailbox)
	case "delete":
		// same two-step rule as the single-message delete: Trash first,
		// physical removal only for mail already there.
		_, mailboxName, err := s.store.GetAccountMessage(ctx, accountID, messageID)
		if err != nil {
			return err
		}
		if mailboxName == mailboxTrash {
			return s.store.DeleteAccountMessage(ctx, accountID, messageID)
		}
		return s.store.MoveAccountMessage(ctx, accountID, messageID, mailboxTrash)
	default:
		return errors.New("invalid action")
	}
}

// withFlag returns the flag list with one flag added or removed.
func withFlag(current []string, flag string, add bool) []string {
	out := make([]string, 0, len(current)+1)
	for _, f := range current {
		if f != flag {
			out = append(out, f)
		}
	}
	if add {
		out = append(out, flag)
	}
	return out
}

// ── Mailbox management ──────────────────────────────────────

// systemMailboxMap lists the folders the app itself relies on; they cannot be
// renamed or deleted from the web UI.
var systemMailboxMap = map[string]bool{
	mailboxInbox: true, mailboxSent: true, mailboxDraft: true,
	mailboxTrash: true, mailboxJunk: true, mailboxArchive: true,
}

func (s *Server) handleMeCreateMailbox(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	name := ""
	if err := decodeBody(r, &req); err == nil {
		name = strings.TrimSpace(req.Name)
	}
	if !validMailboxName(name) {
		writeError(w, http.StatusBadRequest, "invalid mailbox name")
		return
	}
	if _, err := s.store.CreateMailbox(r.Context(), u.ID, name); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) handleMeRenameMailbox(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	current := r.PathValue("name")
	var req struct {
		Name string `json:"name"`
	}
	next := ""
	if err := decodeBody(r, &req); err == nil {
		next = strings.TrimSpace(req.Name)
	}
	if systemMailboxMap[current] {
		writeError(w, http.StatusBadRequest, "system mailbox cannot be renamed")
		return
	}
	if !validMailboxName(next) {
		writeError(w, http.StatusBadRequest, "invalid mailbox name")
		return
	}
	if err := s.store.RenameMailbox(r.Context(), u.ID, current, next); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": next})
}

func (s *Server) handleMeDeleteMailbox(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	name := r.PathValue("name")
	if systemMailboxMap[name] {
		writeError(w, http.StatusBadRequest, "system mailbox cannot be deleted")
		return
	}
	if err := s.store.DeleteMailbox(r.Context(), u.ID, name); err != nil {
		mapStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validMailboxName keeps names to something every IMAP client can show:
// printable, no path separator, no leading dot, 64 chars max.
func validMailboxName(name string) bool {
	if name == "" || len(name) > 64 || strings.HasPrefix(name, ".") {
		return false
	}
	if strings.ContainsAny(name, "/\\\"\r\n\t") {
		return false
	}
	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}
	return !systemMailboxMap[name]
}

// ── Drafts ──────────────────────────────────────────────────

// handleMeSaveDraft stores a compose-in-progress in the Drafts mailbox.
// A draft is a real message, so replacing one means appending the new version
// and deleting the old — the same dance every IMAP client does.
func (s *Server) handleMeSaveDraft(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	var req struct {
		ReplaceID string   `json:"replaceId"`
		From      string   `json:"from"`
		ToList    []string `json:"toList"`
		CcList    []string `json:"ccList"`
		Subject   string   `json:"subject"`
		Text      string   `json:"text"`
		InReplyTo string   `json:"inReplyTo"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	from := strings.TrimSpace(req.From)
	if from != "" {
		ok, err := s.store.CanSendAs(r.Context(), u.ID, from)
		if err != nil || !ok {
			writeError(w, http.StatusForbidden, "address not owned by this account")
			return
		}
	}

	raw, err := buildOutgoingMessage(s.hostname, from, req.ToList, req.CcList,
		req.Subject, req.Text, req.InReplyTo, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "draft build failed")
		return
	}
	box, err := s.store.EnsureMailbox(r.Context(), u.ID, mailboxDraft)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	saved, err := s.store.AppendMessage(r.Context(), box.ID, raw, []string{"\\Draft", "\\Seen"}, time.Now())
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	// drop the previous revision once the new one is safely stored
	if req.ReplaceID != "" {
		if oldID, err := uuid.Parse(req.ReplaceID); err == nil {
			_ = s.store.DeleteAccountMessage(r.Context(), u.ID, oldID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": saved.ID.String()})
}

// ── Preference ──────────────────────────────────────────────

var validThemeMap = map[string]bool{"system": true, "dark": true, "light": true}

func (s *Server) handleMeGetPreference(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	p, err := s.store.GetPreference(r.Context(), u.ID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"theme": p.Theme, "locale": p.Locale})
}

func (s *Server) handleMeSetPreference(w http.ResponseWriter, r *http.Request) {
	u := s.resolveMe(w, r)
	if u == nil {
		return
	}
	var req struct {
		Theme  string `json:"theme"`
		Locale string `json:"locale"`
	}
	if err := decodeBody(r, &req); err != nil || !validThemeMap[req.Theme] || !validLocaleMap[req.Locale] {
		writeError(w, http.StatusBadRequest, "invalid body (theme system|dark|light, locale auto|ko|en|ja)")
		return
	}
	if err := s.store.SetPreference(r.Context(), u.ID, &store.Preference{Theme: req.Theme, Locale: req.Locale}); err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"theme": req.Theme, "locale": req.Locale})
}
