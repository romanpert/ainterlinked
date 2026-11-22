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

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

func newServer(t *testing.T) (*Server, *hub.Local) {
	t.Helper()
	st, err := core.Open(filepath.Join(t.TempDir(), "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := hub.NewLocal(st)
	return &Server{Hub: h, Machine: "box", Version: "test",
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

func TestDashboardState(t *testing.T) {
	s, h := newServer(t)
	if err := h.Call(context.Background(), "register", core.RegisterReq{Harness: "codex", Machine: "box", Project: "p", ProjectPath: "/p", AnchorPID: 7}, nil); err != nil {
		t.Fatal(err)
	}
	w := do(t, s, "POST", "/api/send", `{"to":"codex@p","body":"hello from the dashboard"}`, nil)
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
	for _, gone := range []string{"/rpc", "/pair", "/mcp"} {
		if w := do(t, s, "POST", gone, `{}`, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s belongs to the hosted service, not the library: %d", gone, w.Code)
		}
	}
}

// The dashboard is this machine's: no other site's page, no foreign Host
// (DNS rebinding), nothing through a proxy, and no listening off loopback.
func TestOnlyThisMachine(t *testing.T) {
	s, _ := newServer(t)
	if w := do(t, s, "GET", "/api/state", "", map[string]string{"Origin": "http://evil.example"}); w.Code != http.StatusForbidden {
		t.Errorf("cross-origin: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "http://evil.example:7717/api/state", nil)
	req.Host = "evil.example:7717"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("foreign host: %d", w.Code)
	}
	if w := do(t, s, "POST", "/api/send", `{"to":"x","body":"y"}`, map[string]string{"X-Forwarded-For": "203.0.113.9"}); w.Code != http.StatusForbidden {
		t.Errorf("proxied request: %d", w.Code)
	}
	if err := s.Listen(context.Background(), "0.0.0.0:7799"); err == nil || !strings.Contains(err.Error(), "this machine only") {
		t.Errorf("listening off loopback must be refused: %v", err)
	}
	if !IsLoopback("127.0.0.1:1") || IsLoopback("0.0.0.0:1") || !IsLoopback("localhost:1") {
		t.Error("IsLoopback")
	}
}
