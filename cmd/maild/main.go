// Command maild is the mail server daemon.
//
// One binary assembling IMAP (:1143) + inbound SMTP (:2525) + submission
// (:2587) + admin/self-service REST API (:8080) + the outbound queue worker.
// On startup the embedded migrations converge the schema; SIGTERM/SIGINT
// triggers a graceful drain (safe for k8s rolling updates).
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2/imapserver"
	gosmtp "github.com/emersion/go-smtp"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/krisamin/mail/internal/api"
	imapbackend "github.com/krisamin/mail/internal/imap"
	"github.com/krisamin/mail/internal/queue"
	smtpbackend "github.com/krisamin/mail/internal/smtp"
	"github.com/krisamin/mail/internal/spam"
	"github.com/krisamin/mail/internal/store/migration"
	"github.com/krisamin/mail/internal/store/postgres"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// metricMux serves Prometheus metrics (and nothing else).
func metricMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

// loadTLS builds a TLS config when MAIL_TLS_CERT/MAIL_TLS_KEY are set.
// Unset returns nil (plaintext — behind a proxy/tunnel, or dev).
func loadTLS() *tls.Config {
	certFile, keyFile := os.Getenv("MAIL_TLS_CERT"), os.Getenv("MAIL_TLS_KEY")
	if certFile == "" || keyFile == "" {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("TLS certificate load failed: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

func main() {
	// dev defaults: 143/25/587 are privileged ports → 1143/2525/2587. Mapped by a Service on k8s.
	cfg := loadConfig()
	imapAddr := cfg.IMAPAddr
	smtpAddr := cfg.SMTPAddr
	submissionAddr := cfg.SubmissionAddr
	hostname := cfg.Hostname
	tlsConfig := loadTLS()

	st, err := postgres.New(context.Background(), cfg.DSN)
	if err != nil {
		log.Fatalf("store connect failed: %v", err)
	}
	defer st.Close()

	// embedded migrations — schema converges here for both empty and existing DBs (the whole story on k8s)
	if err := migration.Run(context.Background(), st.Pool()); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	errCh := make(chan error, 5) // sender: IMAP/SMTP/submission/SMTPS/API

	// mailbox-change push hub (LISTEN/NOTIFY) — IDLE wakes immediately instead of polling
	notifyCtx, notifyCancel := context.WithCancel(context.Background())
	notifier := postgres.NewNotifier(st)
	go notifier.Run(notifyCtx)

	// IMAP server — implicit TLS when configured, plaintext otherwise (proxy/dev assumption)
	imapOpts := &imapserver.Options{
		NewSession: imapbackend.NewBackend(st).WithNotifier(notifier).NewSession,
		TLSConfig:  tlsConfig,
	}
	if tlsConfig == nil {
		imapOpts.InsecureAuth = true
	}
	imapSrv := imapserver.New(imapOpts)
	go func() {
		if tlsConfig != nil {
			log.Printf("maild: IMAP server listening %s (TLS)", imapAddr)
			errCh <- imapSrv.ListenAndServeTLS(imapAddr)
			return
		}
		log.Printf("maild: IMAP server listening %s (plaintext — proxy/dev assumption)", imapAddr)
		errCh <- imapSrv.ListenAndServe(imapAddr)
	}()

	// inbound (MX) SMTP server — SPF/DKIM/DMARC verification + optional policy enforcement
	mxBackend := smtpbackend.NewBackend(st, hostname)
	if cfg.DMARCEnforce {
		mxBackend.WithDMARCEnforcement()
		log.Printf("maild: DMARC enforcement active (reject→550, quarantine→Junk)")
	} else {
		mxBackend.WithInboundVerification()
	}
	// connection screening: DNSBL(reject) + FCrDNS/HELO(quarantine).
	if len(cfg.DNSBLZoneList) > 0 {
		mxBackend.WithSpamChecker(spam.NewChecker(cfg.DNSBLZoneList))
		log.Printf("maild: connection screening active (dnsbl=%v, rDNS/HELO quarantine)", cfg.DNSBLZoneList)
	}
	// greylisting: first-contact triplets get 451, retries pass.
	// FCrDNS-verified senders skip it (requires the spam checker above).
	if cfg.Greylist {
		mxBackend.WithGreylist(cfg.GreylistDelay)
		log.Printf("maild: greylisting active (delay=%s, FCrDNS-verified senders exempt)", cfg.GreylistDelay)
	}
	// rspamd content scanning (reject → 554, add-header/greylist → Junk)
	if cfg.RspamdURL != "" {
		mxBackend.WithScanner(spam.NewScanner(cfg.RspamdURL, cfg.RspamdPass))
		log.Printf("maild: rspamd scanning active (url=%s)", cfg.RspamdURL)
	}
	smtpSrv := gosmtp.NewServer(mxBackend)
	smtpSrv.Addr = smtpAddr
	smtpSrv.Domain = hostname
	smtpSrv.ReadTimeout = 60 * time.Second
	smtpSrv.WriteTimeout = 60 * time.Second
	smtpSrv.MaxMessageBytes = 25 * 1024 * 1024 // 25MB
	smtpSrv.MaxRecipients = 50
	smtpSrv.TLSConfig = tlsConfig // offers STARTTLS (when configured)
	go func() {
		log.Printf("maild: inbound SMTP server listening %s (hostname=%s)", smtpAddr, hostname)
		errCh <- smtpSrv.ListenAndServe()
	}()

	// SMTP submission server (AUTH required — our users submit with app passwords)
	// Outbound relays are DB-managed (0005) — external-domain submissions are
	// always queued; the worker resolves the relay at send time (domain-assigned → default).
	submissionBackend := smtpbackend.NewSubmissionBackend(st, hostname, true)
	subSrv := gosmtp.NewServer(submissionBackend)
	subSrv.Addr = submissionAddr
	subSrv.Domain = hostname
	subSrv.ReadTimeout = 60 * time.Second
	subSrv.WriteTimeout = 60 * time.Second
	subSrv.MaxMessageBytes = 25 * 1024 * 1024
	subSrv.MaxRecipients = 50
	subSrv.TLSConfig = tlsConfig
	// plaintext AUTH only without TLS (assumes proxy/tunnel in front)
	subSrv.AllowInsecureAuth = tlsConfig == nil
	go func() {
		log.Printf("maild: SMTP submission server listening %s (STARTTLS=%v)", submissionAddr, tlsConfig != nil)
		errCh <- subSrv.ListenAndServe()
	}()

	// SMTPS submission server (implicit TLS — RFC 8314 recommended, exposed as 465).
	// Shares the same backend (identical auth/queue logic). Skipped without a TLS cert.
	var smtpsSrv *gosmtp.Server
	smtpsAddr := cfg.SMTPSAddr
	if tlsConfig != nil {
		smtpsSrv = gosmtp.NewServer(submissionBackend)
		smtpsSrv.Addr = smtpsAddr
		smtpsSrv.Domain = hostname
		smtpsSrv.ReadTimeout = 60 * time.Second
		smtpsSrv.WriteTimeout = 60 * time.Second
		smtpsSrv.MaxMessageBytes = 25 * 1024 * 1024
		smtpsSrv.MaxRecipients = 50
		smtpsSrv.TLSConfig = tlsConfig
		go func() {
			log.Printf("maild: SMTPS submission server listening %s (implicit TLS)", smtpsAddr)
			errCh <- smtpsSrv.ListenAndServeTLS()
		}()
	}

	// outbound queue worker — relays are fully DB-managed (add/change via the
	// admin UI, no restart). Sends for relay-less domains retry as transient errors.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	worker := queue.NewWorker(st, queue.NewResolvingSender(st), queue.Config{}).
		WithSigner(queue.NewDKIMSigner(st)).
		WithHostname(hostname)
	workerDone := make(chan struct{})
	go func() {
		worker.Run(workerCtx)
		close(workerDone)
	}()

	// Admin REST API — requires an OIDC Bearer token + the admin group
	apiAddr := cfg.APIAddr
	if cfg.DevInsecure {
		log.Printf("★★★ MAIL_DEV_INSECURE=true — API token verification OFF, every request treated as admin (never in production) ★★★")
	}
	authCfg := api.AuthConfig{
		IssuerURL:          cfg.OIDCIssuer,
		ClientID:           cfg.OIDCClientID,
		AdminGroup:         cfg.AdminGroup,
		InsecureSkipVerify: cfg.DevInsecure,
	}
	authn, err := api.NewAuthenticator(context.Background(), authCfg)
	if err != nil {
		log.Fatalf("OIDC init failed: %v", err)
	}
	// health-check targets — smtps exists only when TLS is configured
	systemPortList := []api.SystemPort{
		{Name: "imap", Addr: imapAddr, Kind: "imap", TLS: tlsConfig != nil, Check: true},
		{Name: "smtp", Addr: smtpAddr, Kind: "smtp", Check: true},
		{Name: "submission", Addr: submissionAddr, Kind: "smtp", Check: true},
		{Name: "smtps", Addr: smtpsAddr, Kind: "smtp", TLS: true, Check: tlsConfig != nil},
	}
	apiSrv := &http.Server{
		Addr: apiAddr,
		Handler: api.NewServer(st, authn).WithHostname(hostname).
			WithSystemPort(systemPortList).
			WithExternalPort(hostname, []api.ExternalPort{
				{Name: "imaps", Port: "993", Mode: "tls"},
				{Name: "smtp", Port: "25", Mode: "banner"},
				{Name: "submission", Port: "587", Mode: "banner"},
				{Name: "smtps", Port: "465", Mode: "tls"},
			}),
		// slowloris defense — caps on header/full reads and idle connections
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		log.Printf("maild: Admin API listening %s (issuer=%q group=%s)",
			apiAddr, authCfg.IssuerURL, authCfg.AdminGroup)
		errCh <- apiSrv.ListenAndServe()
	}()

	// ── metrics (Prometheus) ─────────────────────────────────
	// Separate listener from the Admin API: cluster-internal only, never
	// routed through the gateway, so /metrics needs no auth story.
	metricAddr := cfg.MetricAddr
	metricSrv := &http.Server{
		Addr:              metricAddr,
		Handler:           metricMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("maild: metrics listening %s (/metrics)", metricAddr)
		// metrics are observability, not service — a dead metrics listener
		// must not take down mail (unlike errCh servers).
		if err := metricSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("maild: metrics server stopped: %v", err)
		}
	}()

	// ── graceful shutdown ────────────────────────────────────
	// On SIGTERM (k8s)/SIGINT: stop accepting new connections → stop the worker
	// → clean up within the grace period. Any single server dying exits the whole
	// process (crash-fast).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		log.Printf("maild: received %s — starting graceful drain", sig)
	case err := <-errCh:
		log.Printf("maild: server error — shutting down: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	workerCancel() // queue worker: finish the current batch, then stop
	notifyCancel() // stop the LISTEN loop
	_ = apiSrv.Shutdown(shutdownCtx)
	_ = metricSrv.Shutdown(shutdownCtx)
	_ = smtpSrv.Shutdown(shutdownCtx)
	_ = subSrv.Shutdown(shutdownCtx)
	if smtpsSrv != nil {
		_ = smtpsSrv.Shutdown(shutdownCtx)
	}
	_ = imapSrv.Close()

	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		log.Printf("maild: timed out waiting for the worker to stop")
	}
	log.Printf("maild: bye")
}
