package imap

import (
	"errors"
	"time"

	goimap "github.com/emersion/go-imap/v2"

	"github.com/krisamin/mail/internal/store"
)

// timeNow is a variable so tests can swap it out.
var timeNow = time.Now

// idleInterval is the DB fallback-poll period during IDLE (push comes via LISTEN/NOTIFY).
const idleInterval = 15 * time.Second

func newIdleTicker() *time.Ticker {
	return time.NewTicker(idleInterval)
}

// fromImapFlagList converts goimap.Flag → store flag strings.
func fromImapFlagList(flagList []goimap.Flag) []string {
	if len(flagList) == 0 {
		return nil
	}
	out := make([]string, len(flagList))
	for i, f := range flagList {
		out[i] = string(f)
	}
	return out
}

// toImapFlagList converts store flag strings → goimap.Flag.
func toImapFlagList(flagList []string) []goimap.Flag {
	out := make([]goimap.Flag, len(flagList))
	for i, f := range flagList {
		out[i] = goimap.Flag(f)
	}
	return out
}

// hasFlag is a case-insensitive flag check (RFC 3501: system flags are case-insensitive).
func hasFlag(flagList []string, want goimap.Flag) bool {
	for _, f := range flagList {
		if equalFlag(goimap.Flag(f), want) {
			return true
		}
	}
	return false
}

func equalFlag(a, b goimap.Flag) bool {
	return canonicalFlag(a) == canonicalFlag(b)
}

func canonicalFlag(f goimap.Flag) goimap.Flag {
	return goimap.Flag(lowerASCII(string(f)))
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if 'A' <= b[i] && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// applyStoreFlagList returns the result of applying a STORE op (set/add/del) to existing flags.
func applyStoreFlagList(existing []string, op *goimap.StoreFlags) []string {
	switch op.Op {
	case goimap.StoreFlagsSet:
		return fromImapFlagList(op.Flags)
	case goimap.StoreFlagsAdd:
		out := append([]string{}, existing...)
		for _, f := range op.Flags {
			if !hasFlag(out, f) {
				out = append(out, string(f))
			}
		}
		return out
	case goimap.StoreFlagsDel:
		var out []string
		for _, e := range existing {
			if !hasFlag(fromImapFlagList(op.Flags), goimap.Flag(e)) {
				out = append(out, e)
			}
		}
		return out
	}
	return existing
}

// requireSelected guards the selected state.
func (s *Session) requireSelected() error {
	if s.mailbox == nil {
		return errors.New("no mailbox selected")
	}
	return nil
}

// requireWritable blocks state mutation on a mailbox selected via EXAMINE
// (read-only) (RFC 3501 §6.3.2 — EXAMINE must not change permanent state).
func (s *Session) requireWritable() error {
	if err := s.requireSelected(); err != nil {
		return err
	}
	if s.readOnly {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			// beta.8 has no READ-ONLY constant, spell it out (RFC 3501 §7.1)
			Code: goimap.ResponseCode("READ-ONLY"),
			Text: "mailbox is selected read-only",
		}
	}
	return nil
}

// forEachInSet iterates snapshot entries matching numSet (SeqSet or UIDSet).
// Stops when f returns false.
func (s *Session) forEachInSet(numSet goimap.NumSet, f func(seqNum uint32, entry snapEntry) bool) {
	switch set := numSet.(type) {
	case goimap.SeqSet:
		set = s.staticSeqSet(set)
		for i, e := range s.snap {
			seq := uint32(i + 1)
			if set.Contains(seq) {
				if !f(seq, e) {
					return
				}
			}
		}
	case goimap.UIDSet:
		set = s.staticUIDSet(set)
		for i, e := range s.snap {
			if set.Contains(e.uid) {
				if !f(uint32(i+1), e) {
					return
				}
			}
		}
	}
}

// staticSeqSet replaces "*" (=0) with the snapshot maximum.
func (s *Session) staticSeqSet(set goimap.SeqSet) goimap.SeqSet {
	max := uint32(len(s.snap))
	out := make(goimap.SeqSet, len(set))
	copy(out, set)
	for i := range out {
		staticRange(&out[i].Start, &out[i].Stop, max)
	}
	return out
}

// staticUIDSet replaces "*" (=0) with the max UID in the snapshot.
func (s *Session) staticUIDSet(set goimap.UIDSet) goimap.UIDSet {
	var max uint32
	if n := len(s.snap); n > 0 {
		max = uint32(s.snap[n-1].uid)
	}
	out := make(goimap.UIDSet, len(set))
	copy(out, set)
	for i := range out {
		staticRange((*uint32)(&out[i].Start), (*uint32)(&out[i].Stop), max)
	}
	return out
}

func staticRange(start, stop *uint32, max uint32) {
	dyn := false
	if *start == 0 {
		*start = max
		dyn = true
	}
	if *stop == 0 {
		*stop = max
		dyn = true
	}
	if dyn && *start > *stop {
		*start, *stop = *stop, *start
	}
}

// loadMessageMap reads the latest metadata for snapshot entries from the DB.
// (The snapshot only holds msgID/uid; mutable data like flags is fetched each time.)
func (s *Session) loadMessageMap(msgIDMap map[int64]bool) (map[int64]*store.Message, error) {
	ctx, cancel := opCtx()
	defer cancel()

	messageList, err := s.backend.store.ListMessage(ctx, s.mailbox.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*store.Message, len(msgIDMap))
	for _, m := range messageList {
		if msgIDMap[m.ID] {
			out[m.ID] = m
		}
	}
	return out, nil
}
