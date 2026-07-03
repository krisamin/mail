package imap

import (
	"errors"
	"sort"
	"strings"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/krisamin/mail/internal/store"
)

// mailboxDelim은 메일박스 계층 구분자.
const mailboxDelim rune = '/'

// snapEntry는 SELECT 스냅샷의 한 항목. seqnum = 인덱스+1.
type snapEntry struct {
	msgID int64
	uid   goimap.UID
}

// Session은 imapserver.Session 구현체. 연결 하나당 하나.
type Session struct {
	backend *Backend

	// 인증 후 채워짐
	user *store.User

	// SELECT 후 채워짐
	mailbox  *store.Mailbox
	readOnly bool
	snap     []snapEntry
}

var _ imapserver.Session = (*Session)(nil)

// normMailbox는 INBOX 대소문자 무시 규칙(RFC 3501)을 적용한다.
func normMailbox(name string) string {
	if strings.EqualFold(name, "INBOX") {
		return "INBOX"
	}
	return name
}

// mapMailboxErr는 store 에러를 IMAP 상태 응답으로 변환한다.
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

// definedFlags는 서버가 지원하는 시스템 플래그.
func definedFlags() []goimap.Flag {
	return []goimap.Flag{
		goimap.FlagSeen, goimap.FlagAnswered, goimap.FlagFlagged,
		goimap.FlagDeleted, goimap.FlagDraft,
	}
}

func (s *Session) Close() error {
	return nil
}

// ── Not authenticated ───────────────────────────────────────

// Login은 주소+앱 비밀번호 인증 (DD-02: 메일앱은 앱 비밀번호).
func (s *Session) Login(username, password string) error {
	ctx, cancel := opCtx()
	defer cancel()

	u, err := s.backend.store.AuthenticateAppPassword(ctx, username, password)
	if err != nil {
		if errors.Is(err, store.ErrAuthFailed) || errors.Is(err, store.ErrNotFound) {
			return imapserver.ErrAuthFailed
		}
		return err
	}
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
	msgs, err := s.backend.store.ListMessages(ctx, mbox.ID)
	if err != nil {
		return nil, err
	}

	snap := make([]snapEntry, len(msgs))
	for i, m := range msgs {
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
		Flags:          definedFlags(),
		PermanentFlags: append(definedFlags(), goimap.FlagWildcard),
		NumMessages:    st.NumMessages,
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

	// 패턴 없음 = 계층 구분자 조회 (RFC 3501 §6.3.8)
	if len(patterns) == 0 {
		return w.WriteList(&goimap.ListData{
			Attrs: []goimap.MailboxAttr{goimap.MailboxAttrNoSelect},
			Delim: mailboxDelim,
		})
	}

	boxes, err := s.backend.store.ListMailboxes(ctx, s.user.ID)
	if err != nil {
		return err
	}

	var out []goimap.ListData
	for _, mbox := range boxes {
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

	data := goimap.StatusData{Mailbox: name}
	if options.NumMessages {
		n := st.NumMessages
		data.NumMessages = &n
	}
	if options.NumUnseen {
		n := st.NumUnseen
		data.NumUnseen = &n
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

	raw := make([]byte, 0, r.Size())
	buf := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(buf)
		raw = append(raw, buf[:n]...)
		if rerr != nil {
			break
		}
	}

	t := options.Time
	if t.IsZero() {
		t = timeNow()
	}
	msg, err := s.backend.store.AppendMessage(ctx, mbox.ID, raw, fromImapFlags(options.Flags), t)
	if err != nil {
		return nil, err
	}

	// 자기 세션에서 선택 중인 메일박스면 스냅샷에도 반영
	if s.mailbox != nil && s.mailbox.ID == mbox.ID {
		s.snap = append(s.snap, snapEntry{msgID: msg.ID, uid: goimap.UID(msg.UID)})
	}

	return &goimap.AppendData{
		UID:         goimap.UID(msg.UID),
		UIDValidity: mbox.UIDValidity,
	}, nil
}

// Poll은 다른 세션이 만든 변경(신규 메일/expunge)을 스냅샷과 DB 비교로 반영한다.
func (s *Session) Poll(w *imapserver.UpdateWriter, allowExpunge bool) error {
	return s.pollChanges(w, allowExpunge)
}

// Idle은 주기적으로 pollChanges를 돌려 신규 메일을 알린다 (RFC 2177).
// Phase 2+에서 LISTEN/NOTIFY 기반 push로 교체 예정.
func (s *Session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	ticker := newIdleTicker()
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-ticker.C:
			if err := s.pollChanges(w, true); err != nil {
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

	msgs, err := s.backend.store.ListMessages(ctx, s.mailbox.ID)
	if err != nil {
		return err
	}
	curByUID := make(map[goimap.UID]*store.Message, len(msgs))
	for _, m := range msgs {
		curByUID[goimap.UID(m.UID)] = m
	}

	// 1) 사라진 메시지 → EXPUNGE (높은 seqnum부터)
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

	// 2) 새 메시지 → 스냅샷 뒤에 추가 + EXISTS
	inSnap := make(map[goimap.UID]bool, len(s.snap))
	for _, e := range s.snap {
		inSnap[e.uid] = true
	}
	added := false
	for _, m := range msgs { // ListMessages는 UID 오름차순
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
