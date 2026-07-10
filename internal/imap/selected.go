package imap

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/krisamin/mail/internal/store"
)

// ── Selected state ──────────────────────────────────────────

// Expunge는 \Deleted 메시지를 삭제하고 EXPUNGE 응답을 쓴다.
// uids가 nil이면 전체(CLOSE/EXPUNGE), 아니면 UID EXPUNGE.
func (s *Session) Expunge(w *imapserver.ExpungeWriter, uidSet *goimap.UIDSet) error {
	if err := s.requireWritable(); err != nil {
		return err
	}
	ctx, cancel := opCtx()
	defer cancel()

	var uidFilter []uint32
	if uidSet != nil {
		static := s.staticUIDSet(*uidSet)
		for _, e := range s.snap {
			if static.Contains(e.uid) {
				uidFilter = append(uidFilter, uint32(e.uid))
			}
		}
		if uidFilter == nil {
			return nil // 매칭 없음
		}
	}

	expunged, err := s.backend.store.ExpungeDeleted(ctx, s.mailbox.ID, uidFilter)
	if err != nil {
		return err
	}

	gone := make(map[goimap.UID]bool, len(expunged))
	for _, u := range expunged {
		gone[goimap.UID(u)] = true
	}

	// 높은 seqnum부터 EXPUNGE 응답 + 스냅샷 제거 (RFC 3501 §7.4.1)
	for i := len(s.snap) - 1; i >= 0; i-- {
		if !gone[s.snap[i].uid] {
			continue
		}
		if w != nil {
			if err := w.WriteExpunge(uint32(i + 1)); err != nil {
				return err
			}
		}
		s.snap = append(s.snap[:i], s.snap[i+1:]...)
	}
	return nil
}

// Fetch는 매칭 메시지의 요청 항목을 응답으로 쓴다.
func (s *Session) Fetch(w *imapserver.FetchWriter, numSet goimap.NumSet, options *goimap.FetchOptions) error {
	if err := s.requireSelected(); err != nil {
		return err
	}

	// 본문 섹션 요청 중 Peek 아닌 게 있으면 \Seen 부여 (RFC 3501 §6.4.5)
	// — 단 EXAMINE(read-only) 선택에선 영구 상태를 바꾸지 않는다.
	markSeen := false
	if !s.readOnly {
		for _, section := range options.BodySection {
			if !section.Peek {
				markSeen = true
				break
			}
		}
	}

	// 매칭 항목 수집
	type hit struct {
		seqNum uint32
		entry  snapEntry
	}
	var hits []hit
	idList := map[int64]bool{}
	s.forEachInSet(numSet, func(seqNum uint32, e snapEntry) bool {
		hits = append(hits, hit{seqNum, e})
		idList[e.msgID] = true
		return true
	})
	if len(hits) == 0 {
		return nil
	}

	metaMap, err := s.loadMessageMap(idList)
	if err != nil {
		return err
	}

	needBlob := options.Envelope || options.BodyStructure != nil ||
		len(options.BodySection) > 0 || len(options.BinarySection) > 0 ||
		len(options.BinarySectionSize) > 0

	for _, h := range hits {
		m := metaMap[h.entry.msgID]
		if m == nil {
			continue // 다른 세션이 지움 — 다음 Poll에서 EXPUNGE로 반영
		}

		flagList := m.Flags
		if markSeen && !hasFlag(flagList, goimap.FlagSeen) {
			flagList = append(append([]string{}, flagList...), string(goimap.FlagSeen))
			ctx, cancel := opCtx()
			err := s.backend.store.SetFlag(ctx, m.ID, flagList)
			cancel()
			if err != nil {
				return err
			}
		}

		var raw []byte
		if needBlob {
			ctx, cancel := opCtx()
			raw, err = s.backend.store.GetMessageBlob(ctx, m.ID)
			cancel()
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return err
			}
		}

		mw := w.CreateMessage(h.seqNum)
		mw.WriteUID(h.entry.uid)
		if options.Flags {
			mw.WriteFlags(toImapFlagList(flagList))
		}
		if options.InternalDate {
			mw.WriteInternalDate(m.InternalDate)
		}
		if options.RFC822Size {
			mw.WriteRFC822Size(m.SizeBytes)
		}
		if options.Envelope {
			mw.WriteEnvelope(extractEnvelope(raw))
		}
		if options.BodyStructure != nil {
			mw.WriteBodyStructure(imapserver.ExtractBodyStructure(bytes.NewReader(raw)))
		}
		for _, section := range options.BodySection {
			buf := imapserver.ExtractBodySection(bytes.NewReader(raw), section)
			wc := mw.WriteBodySection(section, int64(len(buf)))
			_, werr := wc.Write(buf)
			cerr := wc.Close()
			if werr != nil {
				return werr
			}
			if cerr != nil {
				return cerr
			}
		}
		for _, section := range options.BinarySection {
			buf := imapserver.ExtractBinarySection(bytes.NewReader(raw), section)
			wc := mw.WriteBinarySection(section, int64(len(buf)))
			_, werr := wc.Write(buf)
			cerr := wc.Close()
			if werr != nil {
				return werr
			}
			if cerr != nil {
				return cerr
			}
		}
		for _, bss := range options.BinarySectionSize {
			mw.WriteBinarySectionSize(bss, imapserver.ExtractBinarySectionSize(bytes.NewReader(raw), bss))
		}
		if err := mw.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Store는 플래그를 변경하고 (Silent 아니면) FETCH 응답으로 결과를 쓴다.
func (s *Session) Store(w *imapserver.FetchWriter, numSet goimap.NumSet, op *goimap.StoreFlags, options *goimap.StoreOptions) error {
	if err := s.requireWritable(); err != nil {
		return err
	}

	idList := map[int64]bool{}
	s.forEachInSet(numSet, func(_ uint32, e snapEntry) bool {
		idList[e.msgID] = true
		return true
	})
	if len(idList) == 0 {
		return nil
	}

	metaMap, err := s.loadMessageMap(idList)
	if err != nil {
		return err
	}
	for _, m := range metaMap {
		next := applyStoreFlagList(m.Flags, op)
		ctx, cancel := opCtx()
		err := s.backend.store.SetFlag(ctx, m.ID, next)
		cancel()
		if err != nil {
			return err
		}
	}

	if !op.Silent {
		return s.Fetch(w, numSet, &goimap.FetchOptions{Flags: true})
	}
	return nil
}

// Copy는 매칭 메시지를 대상 메일박스로 복사한다.
func (s *Session) Copy(numSet goimap.NumSet, destName string) (*goimap.CopyData, error) {
	if err := s.requireSelected(); err != nil {
		return nil, err
	}
	ctx, cancel := opCtx()
	defer cancel()

	dest, err := s.backend.store.GetMailbox(ctx, s.user.ID, normMailbox(destName))
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
	if dest.ID == s.mailbox.ID {
		return nil, &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			Text: "source and destination mailboxList are identical",
		}
	}

	var sourceUIDs, destUIDs goimap.UIDSet
	var copyErr error
	s.forEachInSet(numSet, func(_ uint32, e snapEntry) bool {
		cctx, ccancel := opCtx()
		copied, err := s.backend.store.CopyMessage(cctx, e.msgID, dest.ID)
		ccancel()
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return true // 다른 세션이 지운 메시지 — skip
			}
			copyErr = err
			return false
		}
		sourceUIDs.AddNum(e.uid)
		destUIDs.AddNum(goimap.UID(copied.UID))
		return true
	})
	if copyErr != nil {
		return nil, copyErr
	}

	return &goimap.CopyData{
		UIDValidity: dest.UIDValidity,
		SourceUIDs:  sourceUIDs,
		DestUIDs:    destUIDs,
	}, nil
}

// Search는 조건에 맞는 메시지를 찾는다.
func (s *Session) Search(kind imapserver.NumKind, criteria *goimap.SearchCriteria, options *goimap.SearchOptions) (*goimap.SearchData, error) {
	if err := s.requireSelected(); err != nil {
		return nil, err
	}

	idList := make(map[int64]bool, len(s.snap))
	for _, e := range s.snap {
		idList[e.msgID] = true
	}
	metaMap, err := s.loadMessageMap(idList)
	if err != nil {
		return nil, err
	}

	var (
		data   goimap.SearchData
		seqSet goimap.SeqSet
		uidSet goimap.UIDSet
	)
	for i, e := range s.snap {
		m := metaMap[e.msgID]
		if m == nil {
			continue
		}
		seqNum := uint32(i + 1)
		if !s.matchCriteria(m, seqNum, criteria) {
			continue
		}

		var num uint32
		switch kind {
		case imapserver.NumKindSeq:
			seqSet.AddNum(seqNum)
			num = seqNum
		case imapserver.NumKindUID:
			uidSet.AddNum(e.uid)
			num = uint32(e.uid)
		}
		if data.Min == 0 || num < data.Min {
			data.Min = num
		}
		if num > data.Max {
			data.Max = num
		}
		data.Count++
	}

	switch kind {
	case imapserver.NumKindSeq:
		data.All = seqSet
	case imapserver.NumKindUID:
		data.All = uidSet
	}
	return &data, nil
}

// matchCriteria는 메시지가 SEARCH 조건에 맞는지 검사한다.
// 본문 접근이 필요한 조건(Header/Body/Text/Sent*)에서만 blob을 로드한다.
func (s *Session) matchCriteria(m *store.Message, seqNum uint32, c *goimap.SearchCriteria) bool {
	for _, set := range c.SeqNum {
		static := s.staticSeqSet(set)
		if !static.Contains(seqNum) {
			return false
		}
	}
	for _, set := range c.UID {
		static := s.staticUIDSet(set)
		if !static.Contains(goimap.UID(m.UID)) {
			return false
		}
	}
	if !matchDate(m.InternalDate, c.Since, c.Before) {
		return false
	}
	for _, f := range c.Flag {
		if !hasFlag(m.Flags, f) {
			return false
		}
	}
	for _, f := range c.NotFlag {
		if hasFlag(m.Flags, f) {
			return false
		}
	}
	if c.Larger != 0 && m.SizeBytes <= c.Larger {
		return false
	}
	if c.Smaller != 0 && m.SizeBytes >= c.Smaller {
		return false
	}

	if len(c.Header) > 0 || len(c.Body) > 0 || len(c.Text) > 0 ||
		!c.SentSince.IsZero() || !c.SentBefore.IsZero() {
		ctx, cancel := opCtx()
		raw, err := s.backend.store.GetMessageBlob(ctx, m.ID)
		cancel()
		if err != nil {
			return false
		}
		if !matchRawMessage(raw, c) {
			return false
		}
	}

	for i := range c.Not {
		if s.matchCriteria(m, seqNum, &c.Not[i]) {
			return false
		}
	}
	for i := range c.Or {
		if !s.matchCriteria(m, seqNum, &c.Or[i][0]) && !s.matchCriteria(m, seqNum, &c.Or[i][1]) {
			return false
		}
	}
	return true
}

// ── 본문 매칭 헬퍼 (imapmemserver 참고) ─────────────────────

func extractEnvelope(raw []byte) *goimap.Envelope {
	br := bufio.NewReader(bytes.NewReader(raw))
	header, err := textproto.ReadHeader(br)
	if err != nil {
		return nil
	}
	return imapserver.ExtractEnvelope(header)
}

func readEntity(raw []byte) *gomessage.Entity {
	e, _ := gomessage.Read(bytes.NewReader(raw))
	if e == nil {
		e, _ = gomessage.New(gomessage.Header{}, bytes.NewReader(nil))
	}
	return e
}

func matchRawMessage(raw []byte, c *goimap.SearchCriteria) bool {
	header := mail.Header{Header: readEntity(raw).Header}

	for _, fc := range c.Header {
		if !matchHeaderFields(header.FieldsByKey(fc.Key), fc.Value) {
			return false
		}
	}
	if !c.SentSince.IsZero() || !c.SentBefore.IsZero() {
		t, err := header.Date()
		if err != nil || !matchDate(t, c.SentSince, c.SentBefore) {
			return false
		}
	}
	for _, text := range c.Text {
		if !matchEntity(readEntity(raw), text, true) {
			return false
		}
	}
	for _, body := range c.Body {
		if !matchEntity(readEntity(raw), body, false) {
			return false
		}
	}
	return true
}

func matchDate(t, since, before time.Time) bool {
	// RFC 3501: 날짜 비교는 타임존 무시
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if !since.IsZero() {
		sinceDay := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
		if t.Before(sinceDay) {
			return false
		}
	}
	if !before.IsZero() {
		beforeDay := time.Date(before.Year(), before.Month(), before.Day(), 0, 0, 0, 0, time.UTC)
		if !t.Before(beforeDay) {
			return false
		}
	}
	return true
}

func matchHeaderFields(fields gomessage.HeaderFields, pattern string) bool {
	if pattern == "" {
		return fields.Len() > 0
	}
	pattern = strings.ToLower(pattern)
	for fields.Next() {
		v, _ := fields.Text()
		if strings.Contains(strings.ToLower(v), pattern) {
			return true
		}
	}
	return false
}

func matchEntity(e *gomessage.Entity, pattern string, includeHeader bool) bool {
	if pattern == "" {
		return true
	}
	if includeHeader && matchHeaderFields(e.Header.Fields(), pattern) {
		return true
	}

	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				return false
			}
			if matchEntity(part, pattern, includeHeader) {
				return true
			}
		}
		return false
	}

	t, _, err := e.Header.ContentType()
	if err != nil {
		return false
	}
	if !strings.HasPrefix(t, "text/") && !strings.HasPrefix(t, "message/") {
		return false
	}
	buf, err := io.ReadAll(e.Body)
	if err != nil {
		return false
	}
	return bytes.Contains(bytes.ToLower(buf), bytes.ToLower([]byte(pattern)))
}
