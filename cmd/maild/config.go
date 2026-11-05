package main

import (
	"log"
	"os"
	"strings"
	"time"
)

// config is every environment knob in one place — main() reads this struct
// instead of scattering os.Getenv calls through the assembly code, so a new
// setting is a one-line field + parse here.
type config struct {
	DSN      string
	Hostname string

	// listeners
	IMAPAddr       string
	SMTPAddr       string
	SubmissionAddr string
	SMTPSAddr      string
	APIAddr        string
	MetricAddr     string

	// inbound policy
	DMARCEnforce  bool
	DNSBLZoneList []string // empty = screening off
	Greylist      bool
	GreylistDelay time.Duration
	RspamdURL     string
	RspamdPass    string

	// OIDC
	OIDCIssuer   string
	OIDCClientID string
	AdminGroup   string
	DevInsecure  bool

	// internal API + event webhook (ara integration). Empty token = the
	// /internal/v1 routes do not exist; URL and secret both set = webhook on.
	InternalToken string
	EventURL      string
	EventSecret   string

	// OpenBao for relay passwords. Empty BaoAddr = passwords stay in the
	// database column (dev/tests). Set = they live in OpenBao and the column is
	// kept blank; a plaintext row is moved at start-up.
	BaoAddr      string // MAIL_BAO_ADDR, e.g. http://openbao.openbao.svc:8200
	BaoRole      string // MAIL_BAO_ROLE — Kubernetes auth role
	BaoMount     string // MAIL_BAO_MOUNT — KV v2 mount (default "secret")
	BaoPrefix    string // MAIL_BAO_PREFIX — path under the mount (default "mail/relay")
	BaoAuthMount string // MAIL_BAO_AUTH_MOUNT — Kubernetes auth mount (default "kubernetes")
	BaoJWTPath   string // MAIL_BAO_JWT_PATH — service account token file
}

func loadConfig() config {
	c := config{
		DSN:            os.Getenv("MAIL_DSN"),
		Hostname:       env("MAIL_HOSTNAME", "mail.example.com"),
		IMAPAddr:       env("MAIL_IMAP_ADDR", ":1143"),
		SMTPAddr:       env("MAIL_SMTP_ADDR", ":2525"),
		SubmissionAddr: env("MAIL_SUBMISSION_ADDR", ":2587"),
		SMTPSAddr:      env("MAIL_SMTPS_ADDR", ":2465"),
		APIAddr:        env("MAIL_API_ADDR", ":8080"),
		MetricAddr:     env("MAIL_METRIC_ADDR", ":2112"),
		DMARCEnforce:   os.Getenv("MAIL_DMARC_ENFORCE") == "true",
		Greylist:       os.Getenv("MAIL_GREYLIST") == "true",
		GreylistDelay:  time.Minute,
		RspamdURL:      os.Getenv("MAIL_RSPAMD_URL"),
		RspamdPass:     os.Getenv("MAIL_RSPAMD_PASSWORD"),
		OIDCIssuer:     os.Getenv("MAIL_OIDC_ISSUER"),
		OIDCClientID:   os.Getenv("MAIL_OIDC_CLIENT_ID"),
		AdminGroup:     env("MAIL_ADMIN_GROUP", "mail-admin"),
		DevInsecure:    os.Getenv("MAIL_DEV_INSECURE") == "true",
		InternalToken:  os.Getenv("MAIL_INTERNAL_TOKEN"),
		EventURL:       os.Getenv("MAIL_EVENT_URL"),
		EventSecret:    os.Getenv("MAIL_EVENT_SECRET"),
		BaoAddr:        os.Getenv("MAIL_BAO_ADDR"),
		BaoRole:        os.Getenv("MAIL_BAO_ROLE"),
		BaoMount:       env("MAIL_BAO_MOUNT", "secret"),
		BaoPrefix:      env("MAIL_BAO_PREFIX", "mail/relay"),
		BaoAuthMount:   env("MAIL_BAO_AUTH_MOUNT", "kubernetes"),
		BaoJWTPath:     os.Getenv("MAIL_BAO_JWT_PATH"),
	}
	if c.BaoAddr != "" && c.BaoRole == "" {
		log.Fatal("MAIL_BAO_ADDR is set but MAIL_BAO_ROLE is not — refusing to start with relay passwords half-configured")
	}
	if (c.EventURL == "") != (c.EventSecret == "") {
		log.Printf("maild: MAIL_EVENT_URL and MAIL_EVENT_SECRET must both be set — event webhook stays off")
	}
	if c.DSN == "" {
		log.Fatal("MAIL_DSN unset (e.g. postgres://mail:maildev@localhost:55432/mail)")
	}
	// DNSBL zones: comma-separated, "off" disables screening entirely
	if zoneEnv := env("MAIL_DNSBL_ZONE", "zen.spamhaus.org"); zoneEnv != "off" {
		for _, z := range strings.Split(zoneEnv, ",") {
			if z = strings.TrimSpace(z); z != "" {
				c.DNSBLZoneList = append(c.DNSBLZoneList, z)
			}
		}
	}
	if d, err := time.ParseDuration(env("MAIL_GREYLIST_DELAY", "1m")); err == nil && d > 0 {
		c.GreylistDelay = d
	}
	// fail-closed: refuse to start without an issuer. Prevents one missing env
	// from silently opening the admin API unauthenticated — the no-verification
	// dev mode requires an explicit MAIL_DEV_INSECURE=true opt-in.
	if c.OIDCIssuer == "" && !c.DevInsecure {
		log.Fatal("MAIL_OIDC_ISSUER unset — to run without verification set MAIL_DEV_INSECURE=true explicitly (never in production)")
	}
	return c
}
