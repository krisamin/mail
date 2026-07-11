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
