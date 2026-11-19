package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/interlinked/internal/core"
	"github.com/romanpert/interlinked/internal/hub"
)

func newServer(t *testing.T, token string) (*Server, *hub.Local) {
	t.Helper()
	st, err := core.Open(filepath.Join(t.TempDir(), "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := hub.NewLocal(st)
	return &Server{Hub: h, Token: token, Machine: "box", Version: "test",
		Scan: func() []HarnessStatus { return []HarnessStatus{{ID: "codex", Detected: true}} }}, h
}

func do(t *testing.T, s *Server, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1:7717"+path, strings.NewReader(body))
	req.Host = "127.0.0.1:7717"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestRPCAndState(t *testing.T) {
	s, _ := newServer(t, "")
	w := do(t, s, "POST", "/rpc", `{"method":"register","params":{"harness":"codex","machine":"box","project":"p","project_path":"/p","anchor_pid":7}}`, nil)
	var r hub.Response
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.Error != "" {
		t.Fatalf("register over rpc: %s", w.Body.String())
	}
	w = do(t, s, "POST", "/rpc", `{"method":"nope"}`, nil)
	if !strings.Contains(w.Body.String(), "unknown method") {
		t.Fatalf("unknown method: %s", w.Body.String())
	}
	w = do(t, s, "POST", "/api/send", `{"to":"codex@p","body":"hello from the dashboard"}`, nil)
	if !strings.Contains(w.Body.String(), "result") {
		t.Fatalf("send: %s", w.Body.String())
	}
	w = do(t, s, "GET", "/api/state", "", nil)
	var st state
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) != 2 || len(st.Messages) != 1 || len(st.Harnesses) != 1 || st.Messages[0].From != "human" {
		t.Fatalf("state: %+v", st)
	}
	if w := do(t, s, "GET", "/", "", nil); !strings.Contains(w.Body.String(), "interlinked") || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("dashboard not served with a CSP")
	}
}

func TestAccessControl(t *testing.T) {
	open, _ := newServer(t, "")
	if w := do(t, open, "GET", "/api/state", "", map[string]string{"Origin": "http://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "http://evil.example:7717/api/state", nil)
	req.Host = "evil.example:7717" // DNS rebinding: foreign Host, same origin
	w := httptest.NewRecorder()
	open.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("foreign host without token: %d", w.Code)
	}

	locked, _ := newServer(t, "s3cret")
	if w := do(t, locked, "GET", "/api/state", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := do(t, locked, "GET", "/api/state?token=s3cret", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("token in the query string must not be accepted: %d", w.Code)
	}
	if w := do(t, locked, "GET", "/api/state", "", map[string]string{"Authorization": "Bearer s3cret"}); w.Code != http.StatusOK {
		t.Errorf("bearer token: %d", w.Code)
	}
	if err := open.Listen(context.Background(), "0.0.0.0:7799"); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("public listen without a token must be refused: %v", err)
	}
	if !IsLoopback("127.0.0.1:1") || IsLoopback("0.0.0.0:1") || !IsLoopback("localhost:1") {
		t.Error("IsLoopback")
	}
}
