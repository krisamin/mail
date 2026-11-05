// Package bao is a small OpenBao/Vault KV v2 client for one secret field.
//
// maild keeps relay SMTP passwords in OpenBao instead of its own database
// (DD: secrets live in the vault). The client logs in with the pod's
// ServiceAccount token (Kubernetes auth), caches the client token until shortly
// before its lease ends, and logs in again once when a call is rejected with
// 403 (token revoked or expired early). It never logs a secret value.
package bao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ErrNotFound means the KV path (or its current version) does not exist.
var ErrNotFound = errors.New("bao: secret not found")

// DefaultJWTPath is where Kubernetes mounts the pod's ServiceAccount token.
const DefaultJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// field is the KV key that holds the value.
const field = "password"

// Config is everything the client needs. Zero values get defaults.
type Config struct {
	Addr      string // http://openbao.openbao.svc:8200
	Role      string // Kubernetes auth role
	Mount     string // KV v2 mount, e.g. popstar (default "secret")
	AuthMount string // Kubernetes auth mount (default "kubernetes")
	JWTPath   string // default DefaultJWTPath
	HTTP      *http.Client
}

// Client talks to one OpenBao.
type Client struct {
	cfg Config

	mu        sync.Mutex
	token     string
	tokenFrom time.Time
	tokenTTL  time.Duration
}

// New validates the config and returns a client. It does not connect.
func New(cfg Config) (*Client, error) {
	cfg.Addr = strings.TrimRight(strings.TrimSpace(cfg.Addr), "/")
	if cfg.Addr == "" {
		return nil, errors.New("bao: addr required")
	}
	if cfg.Role == "" {
		return nil, errors.New("bao: role required")
	}
	if cfg.Mount == "" {
		cfg.Mount = "secret"
	}
	if cfg.AuthMount == "" {
		cfg.AuthMount = "kubernetes"
	}
	if cfg.JWTPath == "" {
		cfg.JWTPath = DefaultJWTPath
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg}, nil
}

// Get reads the value stored at path (relative to the mount).
func (c *Client) Get(ctx context.Context, path string) (string, error) {
	status, body, err := c.do(ctx, http.MethodGet, c.dataURL(path), nil)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		var out struct {
			Data struct {
				Data map[string]any `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return "", fmt.Errorf("bao: read %s: bad response", path)
		}
		v, ok := out.Data.Data[field].(string)
		if !ok {
			return "", ErrNotFound
		}
		return v, nil
	case http.StatusNotFound:
		return "", ErrNotFound
	default:
		return "", statusErr("read", path, status)
	}
}

// Put writes value at path as a new version.
func (c *Client) Put(ctx context.Context, path, value string) error {
	payload, _ := json.Marshal(map[string]any{"data": map[string]string{field: value}})
	status, _, err := c.do(ctx, http.MethodPost, c.dataURL(path), payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNoContent {
		return statusErr("write", path, status)
	}
	return nil
}

// Delete removes the secret and all its versions. A missing secret is not an error.
func (c *Client) Delete(ctx context.Context, path string) error {
	status, _, err := c.do(ctx, http.MethodDelete, c.metaURL(path), nil)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusOK && status != http.StatusNotFound {
		return statusErr("delete", path, status)
	}
	return nil
}

func statusErr(op, path string, status int) error {
	return fmt.Errorf("bao: %s %s: HTTP %d", op, path, status)
}

func (c *Client) dataURL(path string) string { return c.kvURL("data", path) }
func (c *Client) metaURL(path string) string { return c.kvURL("metadata", path) }

func (c *Client) kvURL(kind, path string) string {
	segList := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segList {
		segList[i] = url.PathEscape(s)
	}
	return fmt.Sprintf("%s/v1/%s/%s/%s", c.cfg.Addr, url.PathEscape(c.cfg.Mount), kind, strings.Join(segList, "/"))
}

// do sends an authenticated request; one 403 triggers a fresh login and a retry.
func (c *Client) do(ctx context.Context, method, target string, payload []byte) (int, []byte, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.login(ctx, attempt > 0)
		if err != nil {
			return 0, nil, err
		}
		status, body, err := c.raw(ctx, method, target, tok, payload)
		if err != nil {
			return 0, nil, err
		}
		if status == http.StatusForbidden && attempt == 0 {
			continue
		}
		return status, body, nil
	}
}

func (c *Client) raw(ctx context.Context, method, target, token string, payload []byte) (int, []byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("bao: %s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// login returns a valid client token, logging in when there is none, it is past
// 80% of its lease, or force is set.
func (c *Client) login(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.token != "" && time.Since(c.tokenFrom) < c.tokenTTL*8/10 {
		return c.token, nil
	}
	jwt, err := os.ReadFile(c.cfg.JWTPath)
	if err != nil {
		return "", fmt.Errorf("bao: read service account token: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{"role": c.cfg.Role, "jwt": strings.TrimSpace(string(jwt))})
	target := fmt.Sprintf("%s/v1/auth/%s/login", c.cfg.Addr, url.PathEscape(c.cfg.AuthMount))
	status, body, err := c.raw(ctx, http.MethodPost, target, "", payload)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("bao: login (role %s): HTTP %d", c.cfg.Role, status)
	}
	var out struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int    `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Auth.ClientToken == "" {
		return "", errors.New("bao: login: bad response")
	}
	c.token = out.Auth.ClientToken
	c.tokenFrom = time.Now()
	c.tokenTTL = time.Duration(out.Auth.LeaseDuration) * time.Second
	if c.tokenTTL <= 0 {
		c.tokenTTL = time.Minute // unknown lease: re-login often rather than hold a dead token
	}
	return c.token, nil
}
