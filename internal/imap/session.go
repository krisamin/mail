package imap

import (
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/krisamin/mail/internal/guard"
	"github.com/krisamin/mail/internal/store"
)

// mailboxDelim is the mailbox hierarchy delimiter.
const mailboxDelim rune = '/'

// appendLimit caps APPEND literal size (also advertised via STATUS APPENDLIMIT).
const appendLimit = 50 * 1024 * 1024 // 50MB

// snapEntry is one entry in the SELECT snapshot. seqnum = index + 1.
type snapEntry struct {
	msgID uuid.UUID
	uid   goimap.UID
}

// Session implements imapserver.Session. One per connection.
type Session struct {
	backend  *Backend
	remoteIP string // brute-force protection key

	// filled after authentication
	user *store.Account

	// filled after SELECT
	mailbox  *store.Mailbox
	readOnly bool
	snap     []snapEntry
}

var _ imapserver.Session = (*Session)(nil)

// normMailbox applies the INBOX case-insensitivity rule (RFC 3501).
func normMailbox(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

// mapMailboxErr converts store errors into IMAP status responses.
func mapMailboxErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Code: goimap.ResponseCodeNonExistent,
			Text: "no such mailbox",
		}
	}
	return err
}

// definedFlagList lists the system flags supported by the server.
func definedFlagList() []goimap.Flag {
	return []goimap.Flag{
		goimap.FlagSeen, goimap.FlagAnswered, goimap.FlagFlagged,
		goimap.FlagDeleted, goimap.FlagDraft,
	}
}

func (s *Session) Close() error {
	return nil
}

// ── Not authenticated ───────────────────────────────────────

// Login authenticates address + app password (DD-02: mail apps use app passwords).
// Brute-force protection tracks both an IP (/64) key and an account key —
// blocking distributed-IP attacks against a single account too.
func (s *Session) Login(username, password string) error {
	ipKey := "ip:" + guard.KeyForIP(s.remoteIP)
	acctKey := "acct:" + strings.ToLower(username)
	if !s.backend.limiter.Allow(ipKey) || !s.backend.limiter.Allow(acctKey) {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Text: "too many failed attempts, try again later",
		}
	}

	ctx, cancel := opCtx()
	defer cancel()

	u, err := s.backend.store.AuthenticateAppPassword(ctx, username, password)
	if err != nil {
		if errors.Is(err, store.ErrAuthFailed) || errors.Is(err, store.ErrNotFound) {
			s.backend.limiter.Fail(ipKey)
			s.backend.limiter.Fail(acctKey)
			return imapserver.ErrAuthFailed
		}
		return err
	}
	s.backend.limiter.Success(ipKey)
	s.backend.limiter.Success(acctKey)
	s.user = u
	return nil
}

// ── Authenticated ───────────────────────────────────────────

func (s *Session) Select(name string, options *goimap.SelectOptions) (*goimap.SelectData, error) {
	ctx, cancel := opCtx()
	defer cancel()

	mbox, err := s.backend.store.GetMailbox(ctx, s.user.ID, normMailbox(name))
	if err != nil {
		return nil, mapMailboxErr(err)
	}
	messageList, err := s.backend.store.ListMessage(ctx, mbox.ID)
	if err != nil {
		return nil, err
	}

	snap := make([]snapEntry, len(messageList))
	for i, m := range messageList {
		snap[i] = snapEntry{msgID: m.ID, uid: goimap.UID(m.UID)}
	}

	st, err := s.backend.store.MailboxStatus(ctx, mbox.ID)
	if err != nil {
		return nil, err
	}

	s.mailbox = mbox
	s.snap = snap
	s.readOnly = options != nil && options.ReadOnly

	return &goimap.SelectData{
		Flags:          definedFlagList(),
		PermanentFlags: append(definedFlagList(), goimap.FlagWildcard),
		NumMessages:    st.MessageCount,
		UIDNext:        goimap.UID(st.UIDNext),
		UIDValidity:    st.UIDValidity,
	}, nil
}

func (s *Session) Unselect() error {
	s.mailbox = nil
	s.snap = nil
	s.readOnly = false
	return nil
}

func (s *Session) Create(name string, options *goimap.CreateOptions) error {
	ctx, cancel := opCtx()
	defer cancel()

	name = strings.TrimRight(normMailbox(name), string(mailboxDelim))
	if _, err := s.backend.store.GetMailbox(ctx, s.user.ID, name); err == nil {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Code: goimap.ResponseCodeAlreadyExists,
			Text: "mailbox already exists",
		}
	}
	_, err := s.backend.store.CreateMailbox(ctx, s.user.ID, name)
	return err
}

func (s *Session) Delete(name string) error {
	ctx, cancel := opCtx()
	defer cancel()
	return mapMailboxErr(s.backend.store.DeleteMailbox(ctx, s.user.ID, normMailbox(name)))
}

func (s *Session) Rename(name, newName string, options *goimap.RenameOptions) error {
	ctx, cancel := opCtx()
	defer cancel()

	newName = strings.TrimRight(normMailbox(newName), string(mailboxDelim))
	if _, err := s.backend.store.GetMailbox(ctx, s.user.ID, newName); err == nil {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Code: goimap.ResponseCodeAlreadyExists,
			Text: "mailbox already exists",
		}
	}
	return mapMailboxErr(s.backend.store.RenameMailbox(ctx, s.user.ID, normMailbox(name), newName))
}

func (s *Session) Subscribe(name string) error {
	return s.setSubscribed(name, true)
}

func (s *Session) Unsubscribe(name string) error {
	return s.setSubscribed(name, false)
}

func (s *Session) setSubscribed(name string, subscribed bool) error {
	ctx, cancel := opCtx()
	defer cancel()

	mbox, err := s.backend.store.GetMailbox(ctx, s.user.ID, normMailbox(name))
	if err != nil {
		return mapMailboxErr(err)
	}
	return s.backend.store.SetSubscribed(ctx, mbox.ID, subscribed)
}

func (s *Session) List(w *imapserver.ListWriter, ref string, patterns []string, options *goimap.ListOptions) error {
	ctx, cancel := opCtx()
	defer cancel()

	// no patterns = hierarchy delimiter query (RFC 3501 §6.3.8)
	if len(patterns) == 0 {
		return w.WriteList(&goimap.ListData{
			Attrs: []goimap.MailboxAttr{goimap.MailboxAttrNoSelect},
			Delim: mailboxDelim,
		})
	}

	boxList, err := s.backend.store.ListMailbox(ctx, s.user.ID)
	if err != nil {
		return err
	}

	var out []goimap.ListData
	for _, mbox := range boxList {
		if options.SelectSubscribed && !mbox.Subscribed {
			continue
		}
		match := false
		for _, pattern := range patterns {
			if imapserver.MatchList(mbox.Name, mailboxDelim, ref, pattern) {
				match = true
				break
			}
		}
		if !match {
			continue
		}

		data := goimap.ListData{Delim: mailboxDelim, Mailbox: mbox.Name}
		if mbox.Subscribed && (options.ReturnSubscribed || options.SelectSubscribed) {
			data.Attrs = append(data.Attrs, goimap.MailboxAttrSubscribed)
		}
		out = append(out, data)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Mailbox < out[j].Mailbox })
	for i := range out {
		if err := w.WriteList(&out[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) Status(name string, options *goimap.StatusOptions) (*goimap.StatusData, error) {
	ctx, cancel := opCtx()
	defer cancel()

	mbox, err := s.backend.store.GetMailbox(ctx, s.user.ID, normMailbox(name))
	if err != nil {
		return nil, mapMailboxErr(err)
	}
	st, err := s.backend.store.MailboxStatus(ctx, mbox.ID)
	if err != nil {
		return nil, err
	}

	// ★Every requested option MUST be filled — the go-imap encoder blindly
	// dereferences the pointer for any requested item (writeStatus panics on
	// nil). Real clients do ask for RECENT/SIZE/DELETED.
	data := goimap.StatusData{Mailbox: name}
	if options.NumMessages {
		n := st.MessageCount
		data.NumMessages = &n
	}
	if options.NumUnseen {
		n := st.UnseenCount
		data.NumUnseen = &n
	}
	if options.NumRecent {
		n := uint32(0) // RECENT is obsolete (IMAP4rev1) — always 0
		data.NumRecent = &n
	}
	if options.NumDeleted {
		n := st.DeletedCount
		data.NumDeleted = &n
	}
	if options.Size {
		n := st.TotalBytes
		data.Size = &n
	}
	if options.AppendLimit {
		n := uint32(appendLimit)
		data.AppendLimit = &n
	}
	if options.DeletedStorage {
		n := int64(0) // QUOTA=RES-STORAGE not supported — report 0
		data.DeletedStorage = &n
	}
	if options.UIDNext {
		data.UIDNext = goimap.UID(st.UIDNext)
	}
	if options.UIDValidity {
		data.UIDValidity = st.UIDValidity
	}
	return &data, nil
}

func (s *Session) Append(mailbox string, r goimap.LiteralReader, options *goimap.AppendOptions) (*goimap.AppendData, error) {
	ctx, cancel := opCtx()
	defer cancel()

	mbox, err := s.backend.store.GetMailbox(ctx, s.user.ID, normMailbox(mailbox))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &goimap.Error{
				Type: goimap.StatusResponseTypeNo,
				Code: goimap.ResponseCodeTryCreate,
				Text: "no such mailbox",
			}
		}
		return nil, err
	}

	// Size cap — trusting the client-declared literal size ({N} — r.Size())
	// for allocation means a bare {2GB} declaration can OOM us. Reject over-limit immediately.
	if r.Size() > appendLimit {
		return nil, &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Code: goimap.ResponseCodeTooBig,
			Text: "message exceeds append limit",
		}
	}

	raw := make([]byte, 0, r.Size())
	buf := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(buf)
		raw = append(raw, buf[:n]...)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// Treating a non-EOF read error as EOF would store a truncated
			// message as valid mail — reject with the error.
			return nil, rerr
		}
	}

	t := options.Time
	if t.IsZero() {
		t = timeNow()
	}
	msg, err := s.backend.store.AppendMessage(ctx, mbox.ID, raw, fromImapFlagList(options.Flags), t)
	if err != nil {
		return nil, err
	}

	// if this session has the mailbox selected, reflect it in the snapshot too
	if s.mailbox != nil && s.mailbox.ID == mbox.ID {
		s.snap = append(s.snap, snapEntry{msgID: msg.ID, uid: goimap.UID(msg.UID)})
	}

	return &goimap.AppendData{
		UID:         goimap.UID(msg.UID),
		UIDValidity: mbox.UIDValidity,
	}, nil
}

// Poll surfaces changes made by other sessions (new mail/expunge) by diffing the snapshot against the DB.
func (s *Session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	return s.pollChanges(w, allowExpunge)
}

// Idle notifies the client of new mail/changes (RFC 2177).
// With a notifier we wake immediately on LISTEN/NOTIFY push; a low-frequency
// fallback poll (the original ticker) runs alongside in case notifications are lost.
func (s *Session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	ticker := newIdleTicker()
	defer ticker.Stop()

	// subscribe to change push (only meaningful with a selected mailbox)
	var notifyCh <-chan struct{}
	if s.backend.notifier != nil && s.mailbox != nil {
		ch, cancel := s.backend.notifier.Subscribe(s.mailbox.ID)
		defer cancel()
		notifyCh = ch
	}

	// Dropping IDLE on a single transient DB error causes a client reconnect
	// storm — only bail after consecutive failures accumulate.
	failCount := 0
	poll := func() error {
		if err := s.pollChanges(w, true); err != nil {
			failCount++
			if failCount >= 3 {
				return err
			}
			return nil
		}
		failCount = 0
		return nil
	}

	for {
		select {
		case <-stop:
			return nil
		case <-notifyCh:
			if err := poll(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := poll(); err != nil {
				return err
			}
		}
	}
}

func (s *Session) pollChanges(w *imapserver.UpdateWriter, allowExpunge bool) error {
	if s.mailbox == nil {
		return nil
	}
	ctx, cancel := opCtx()
	defer cancel()

	messageList, err := s.backend.store.ListMessage(ctx, s.mailbox.ID)
	if err != nil {
		return err
	}
	curByUID := make(map[goimap.UID]*store.Message, len(messageList))
	for _, m := range messageList {
		curByUID[goimap.UID(m.UID)] = m
	}

	// 1) vanished messages → EXPUNGE (highest seqnum first)
	if allowExpunge {
		for i := len(s.snap) - 1; i >= 0; i-- {
			if _, ok := curByUID[s.snap[i].uid]; ok {
				continue
			}
			if err := w.WriteExpunge(uint32(i + 1)); err != nil {
				return err
			}
			s.snap = append(s.snap[:i], s.snap[i+1:]...)
		}
	}

	// 2) new messages → append to snapshot + EXISTS
	inSnap := make(map[goimap.UID]bool, len(s.snap))
	for _, e := range s.snap {
		inSnap[e.uid] = true
	}
	added := false
	for _, m := range messageList { // ListMessage returns UID ascending order
		uid := goimap.UID(m.UID)
		if !inSnap[uid] {
			s.snap = append(s.snap, snapEntry{msgID: m.ID, uid: uid})
			added = true
		}
	}
	if added {
		if err := w.WriteNumMessages(uint32(len(s.snap))); err != nil {
			return err
		}
	}
	return nil
}
