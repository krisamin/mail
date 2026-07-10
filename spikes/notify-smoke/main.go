// notify roundtrip smoke test: Subscribe → AppendMessage → confirm channel receipt
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/krisamin/mail/internal/store"
	"github.com/krisamin/mail/internal/store/postgres"
)

func main() {
	ctx := context.Background()
	st, err := postgres.New(ctx, os.Getenv("MAIL_TEST_DSN"))
	if err != nil {
		panic(err)
	}
	defer st.Close()

	notifier := postgres.NewNotifier(st)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go notifier.Run(runCtx)
	time.Sleep(500 * time.Millisecond) // wait for the LISTEN connection to settle

	// ensure maro INBOX
	acct, err := st.FindAccountByAddress(ctx, "maro@krisam.in")
	if err != nil {
		panic(err)
	}
	mb, err := st.GetMailbox(ctx, acct.ID, "INBOX")
	if err != nil {
		mb, err = st.CreateMailbox(ctx, acct.ID, "INBOX")
		if err != nil {
			panic(err)
		}
	}

	ch, unsub := notifier.Subscribe(mb.ID)
	defer unsub()

	raw := strings.ReplaceAll(`Subject: notify smoke
From: smoke@krisam.in
To: maro@krisam.in

hello push`, "\n", "\r\n")
	if _, err := st.AppendMessage(ctx, mb.ID, []byte(raw), nil, time.Now()); err != nil {
		panic(err)
	}

	select {
	case <-ch:
		fmt.Println("NOTIFY_ROUNDTRIP_OK")
	case <-time.After(3 * time.Second):
		fmt.Println("NOTIFY_TIMEOUT")
		os.Exit(1)
	}
	_ = store.Store(st)
}
