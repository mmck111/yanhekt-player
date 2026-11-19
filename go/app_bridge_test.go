package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeAppReturnPath(t *testing.T) {
	cases := []struct {
		raw, fallback, want string
	}{
		{"", "/", "/"},
		{"", "/y2", "/y2"},
		{"/y2?course=12345", "/", "/y2?course=12345"},
		{"http://127.0.0.1:8788/y2?q=math", "/", "/y2?q=math"},
		{"https://evil.example/phish", "/y2", "/y2"},
		{"//evil.example/phish", "/", "/"},
		{"/_app/auth?code=abc", "/", "/"},
	}
	for _, tc := range cases {
		if got := normalizeAppReturnPath(tc.raw, tc.fallback); got != tc.want {
			t.Fatalf("normalizeAppReturnPath(%q, %q) = %q, want %q", tc.raw, tc.fallback, got, tc.want)
		}
	}
}

func TestAppAuthTicketLifecycle(t *testing.T) {
	code := issueAppAuthTicket("y2", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "/y2?course=99")
	if code == "" {
		t.Fatal("expected non-empty ticket code")
	}

	req := httptest.NewRequest(http.MethodGet, "/_app/auth?code="+code, nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	handleAppAuth(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/y2?course=99" {
		t.Fatalf("Location = %q, want /y2?course=99", loc)
	}
	cookies := strings.Join(rec.Header().Values("Set-Cookie"), "; ")
	if !strings.Contains(cookies, y2Cookie+"=") {
		t.Fatalf("expected %s cookie, got %q", y2Cookie, cookies)
	}

	// 单次消费后立即失效，避免重放
	req2 := httptest.NewRequest(http.MethodGet, "/_app/auth?code="+code, nil)
	req2.RemoteAddr = "127.0.0.1:54321"
	rec2 := httptest.NewRecorder()
	handleAppAuth(rec2, req2)
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/" {
		t.Fatalf("reused code status=%d Location=%q, want 302 fallback /", rec2.Code, rec2.Header().Get("Location"))
	}
}

func TestAppFocusHookAndRemoteGuard(t *testing.T) {
	setDesktopFocusHook(nil)
	noHookReq := httptest.NewRequest(http.MethodGet, "/_app/focus", nil)
	noHookReq.RemoteAddr = "127.0.0.1:50000"
	noHookRec := httptest.NewRecorder()
	handleAppFocus(noHookRec, noHookReq)
	if noHookRec.Code != http.StatusNotFound {
		t.Fatalf("no-hook status = %d, want 404", noHookRec.Code)
	}

	called := 0
	setDesktopFocusHook(func() { called++ })
	defer setDesktopFocusHook(nil)

	remoteReq := httptest.NewRequest(http.MethodGet, "/_app/focus", nil)
	remoteReq.RemoteAddr = "192.168.1.25:4444"
	remoteRec := httptest.NewRecorder()
	handleAppFocus(remoteRec, remoteReq)
	if remoteRec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d, want 403", remoteRec.Code)
	}
	if called != 0 {
		t.Fatalf("focus hook called on remote request: %d", called)
	}

	localReq := httptest.NewRequest(http.MethodGet, "/_app/focus", nil)
	localReq.RemoteAddr = "127.0.0.1:50000"
	localRec := httptest.NewRecorder()
	handleAppFocus(localRec, localReq)
	if localRec.Code != http.StatusOK || called != 1 {
		t.Fatalf("local status=%d called=%d, want 200 and 1", localRec.Code, called)
	}
}
