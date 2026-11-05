// Package baotest is a fake OpenBao for tests: Kubernetes-auth login plus the
// KV v2 data/metadata endpoints the bao client uses. It checks the token on
// every KV call (403 otherwise), like the real thing.
package baotest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/krisamin/mail/internal/bao"
)

const (
	Role  = "maild"
	Mount = "popstar"
	JWT   = "sa-jwt-for-tests"
)

// Server is the fake.
type Server struct {
	*httptest.Server
	JWTPath string

	mu     sync.Mutex
	secret map[string]string
	valid  map[string]bool
	n      int
	Lease  int  // lease_duration returned by login (default 3600)
	Down   bool // true = every KV call answers 503
	// Standby simulates reading from a standby node that lags the active node:
	// a GET is answered from a stale copy (404 for anything written since)
	// unless it carries X-Vault-Index at or past the latest write together with
	// X-Vault-Inconsistent: forward-active-node (then it is forwarded and fresh).
	Standby bool
	index   int
	Logins  int
	Reads   int
	Writes  int
	Deletes int
}

// New starts the fake and writes the service account token file.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{secret: map[string]string{}, valid: map[string]bool{}, Lease: 3600}
	s.JWTPath = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(s.JWTPath, []byte(JWT+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Client returns a bao.Client pointed at the fake.
func (s *Server) Client(t *testing.T) *bao.Client {
	t.Helper()
	c, err := bao.New(bao.Config{Addr: s.URL, Role: Role, Mount: Mount, JWTPath: s.JWTPath})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Value returns the stored password at a path relative to the mount.
func (s *Server) Value(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.secret[path]
	return v, ok
}

// Set stores a value directly (as if written by someone else).
func (s *Server) Set(path, v string) {
	s.mu.Lock()
	s.secret[path] = v
	s.mu.Unlock()
}

// PathList returns every stored path.
func (s *Server) PathList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.secret {
		out = append(out, p)
	}
	return out
}

// Revoke invalidates every issued client token (the next KV call gets 403).
func (s *Server) Revoke() {
	s.mu.Lock()
	s.valid = map[string]bool{}
	s.mu.Unlock()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/v1/auth/kubernetes/login" && r.Method == http.MethodPost {
		var in struct{ Role, JWT string }
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		if in.Role != Role || in.JWT != JWT {
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
			return
		}
		s.Logins++
		s.n++
		tok := fmt.Sprintf("tok-%d", s.n)
		s.valid[tok] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": tok, "lease_duration": s.Lease}})
		return
	}
	if !s.valid[r.Header.Get("X-Vault-Token")] {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		return
	}
	if s.Down {
		http.Error(w, `{"errors":["down"]}`, http.StatusServiceUnavailable)
		return
	}
	prefix := "/v1/" + Mount + "/"
	switch {
	case strings.HasPrefix(r.URL.Path, prefix+"data/"):
		path := strings.TrimPrefix(r.URL.Path, prefix+"data/")
		switch r.Method {
		case http.MethodGet:
			s.Reads++
			v, ok := s.secret[path]
			if s.Standby && !s.caughtUp(r) {
				ok = false
			}
			if !ok {
				http.Error(w, `{"errors":[]}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"password": v}, "metadata": map[string]any{"version": 1}}})
		case http.MethodPost:
			s.Writes++
			s.index++
			w.Header().Set("X-Vault-Index", fmt.Sprintf("idx-%d", s.index))
			var in struct{ Data map[string]string }
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &in)
			s.secret[path] = in.Data["password"]
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1}})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	case strings.HasPrefix(r.URL.Path, prefix+"metadata/") && r.Method == http.MethodDelete:
		s.Deletes++
		s.index++
		w.Header().Set("X-Vault-Index", fmt.Sprintf("idx-%d", s.index))
		delete(s.secret, strings.TrimPrefix(r.URL.Path, prefix+"metadata/"))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

// caughtUp reports whether a read carries the headers that make a lagging
// standby forward it to the active node.
func (s *Server) caughtUp(r *http.Request) bool {
	if r.Header.Get("X-Vault-Inconsistent") != "forward-active-node" {
		return false
	}
	var n int
	if _, err := fmt.Sscanf(r.Header.Get("X-Vault-Index"), "idx-%d", &n); err != nil {
		return false
	}
	return n >= s.index
}
