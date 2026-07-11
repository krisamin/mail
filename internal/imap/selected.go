package imap

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/krisamin/mail/internal/store"
)

// ── Selected state ──────────────────────────────────────────

// Expunge deletes \Deleted messages and writes EXPUNGE responses.
// nil uidSet means all (CLOSE/EXPUNGE), otherwise UID EXPUNGE.
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
			return nil // no match
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

	// EXPUNGE responses + snapshot removal from the highest seqnum down (RFC 3501 §7.4.1)
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

// Fetch writes the requested items of matching messages as responses.
func (s *Session) Fetch(w *imapserver.FetchWriter, numSet goimap.NumSet, options *goimap.FetchOptions) error {
	if err := s.requireSelected(); err != nil {
		return err
	}

	// Any non-Peek body-section request sets \Seen (RFC 3501 §6.4.5)
	// — except under EXAMINE (read-only), which must not change permanent state.
	markSeen := false
	if !s.readOnly {
		for _, section := range options.BodySection {
			if !section.Peek {
				markSeen = true
				break
			}
		}
	}

	// collect matching entries
	type hit struct {
		seqNum uint32
		entry  snapEntry
	}
	var hits []hit
	idList := map[uuid.UUID]bool{}
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
			continue // deleted by another session — surfaces as EXPUNGE on the next Poll
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

// Store mutates flags and (unless Silent) writes the result as FETCH responses.
func (s *Session) Store(w *imapserver.FetchWriter, numSet goimap.NumSet, op *goimap.StoreFlags, options *goimap.StoreOptions) error {
	if err := s.requireWritable(); err != nil {
		return err
	}

	idList := map[uuid.UUID]bool{}
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

// Copy copies matching messages to the destination mailbox.
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
				return true // message deleted by another session — skip
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

// Search finds messages matching the criteria.
func (s *Session) Search(kind imapserver.NumKind, criteria *goimap.SearchCriteria, options *goimap.SearchOptions) (*goimap.SearchData, error) {
	if err := s.requireSelected(); err != nil {
		return nil, err
	}

	idList := make(map[uuid.UUID]bool, len(s.snap))
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

// matchCriteria checks whether a message matches the SEARCH criteria.
// The blob is loaded only for criteria that need body access (Header/Body/Text/Sent*).
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

// ── Body matching helpers (modeled on imapmemserver) ───────

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
	// RFC 3501: date comparison ignores timezones
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
