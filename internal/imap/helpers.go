package imap

import (
	"errors"
	"time"

	goimap "github.com/emersion/go-imap/v2"

	"github.com/krisamin/mail/internal/store"
)

// timeNow는 테스트에서 바꿔치기 가능하도록 변수로.
var timeNow = time.Now

// idleInterval은 IDLE 중 DB 폴링 주기 (Phase 1 임시 — Phase 2에서 LISTEN/NOTIFY).
const idleInterval = 15 * time.Second

func newIdleTicker() *time.Ticker {
	return time.NewTicker(idleInterval)
}

// fromImapFlagList는 goimap.Flag → store 플래그 문자열.
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

// toImapFlagList는 store 플래그 문자열 → goimap.Flag.
func toImapFlagList(flagList []string) []goimap.Flag {
	out := make([]goimap.Flag, len(flagList))
	for i, f := range flagList {
		out[i] = goimap.Flag(f)
	}
	return out
}

// hasFlag는 대소문자 무시 플래그 검사 (RFC 3501: 시스템 플래그는 case-insensitive).
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

// applyStoreFlagList는 STORE 연산(set/add/del)을 기존 플래그에 적용한 결과를 돌려준다.
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

// requireSelected는 selected 상태 가드.
func (s *Session) requireSelected() error {
	if s.mailbox == nil {
		return errors.New("no mailbox selected")
	}
	return nil
}

// requireWritable은 EXAMINE(read-only)으로 선택된 메일박스의 상태 변경을
// 막는다 (RFC 3501 §6.3.2 — EXAMINE은 영구 상태를 바꾸면 안 된다).
func (s *Session) requireWritable() error {
	if err := s.requireSelected(); err != nil {
		return err
	}
	if s.readOnly {
		return &goimap.Error{
			Type: goimap.StatusResponseTypeNo,
			// beta.8에 READ-ONLY 상수가 없어 직접 표기 (RFC 3501 §7.1)
			Code: goimap.ResponseCode("READ-ONLY"),
			Text: "mailbox is selected read-only",
		}
	}
	return nil
}

// forEachInSet은 numSet(SeqSet 또는 UIDSet)에 매칭되는 스냅샷 항목을 순회한다.
// f가 false를 돌려주면 중단.
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

// staticSeqSet은 "*"(=0)을 스냅샷 최대값으로 치환한다.
func (s *Session) staticSeqSet(set goimap.SeqSet) goimap.SeqSet {
	max := uint32(len(s.snap))
	out := make(goimap.SeqSet, len(set))
	copy(out, set)
	for i := range out {
		staticRange(&out[i].Start, &out[i].Stop, max)
	}
	return out
}

// staticUIDSet은 "*"(=0)을 스냅샷 내 최대 UID로 치환한다.
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

// loadMessage는 스냅샷 항목의 최신 메타를 DB에서 읽는다.
// (스냅샷은 msgID/uid만 들고, 플래그 등 가변 데이터는 매번 조회)
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
