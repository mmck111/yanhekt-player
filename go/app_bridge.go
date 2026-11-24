package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type appAuthTicket struct {
	which     string // "old" | "y2"
	token     string
	returnURL string
	expiresAt time.Time
}

var (
	appTicketMu   sync.Mutex
	appTickets    = map[string]appAuthTicket{}
	appFocusMu    sync.Mutex
	appFocusHook  func()
)

func setDesktopFocusHook(fn func()) {
	appFocusMu.Lock()
	appFocusHook = fn
	appFocusMu.Unlock()
}

func hasCLIArg(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}

func normalizeAppReturnPath(raw, fallback string) string {
	if fallback != "/y2" {
		fallback = "/"
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if u, err := url.Parse(raw); err == nil {
		if u.Scheme != "" || u.Host != "" {
			h := strings.ToLower(u.Hostname())
			if (u.Scheme != "" && u.Scheme != "http") || (h != "127.0.0.1" && h != "localhost" && h != "::1") {
				return fallback
			}
		}
		if u.Path != "" {
			p := u.Path
			if u.RawQuery != "" {
				p += "?" + u.RawQuery
			}
			raw = p
		}
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, "/_app/") {
		return fallback
	}
	return raw
}

func issueAppAuthTicket(which, token, returnURL string) string {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	code := hex.EncodeToString(buf[:])
	now := time.Now()
	appTicketMu.Lock()
	for k, v := range appTickets {
		if now.After(v.expiresAt) {
			delete(appTickets, k)
		}
	}
	appTickets[code] = appAuthTicket{
		which:     which,
		token:     token,
		returnURL: normalizeAppReturnPath(returnURL, map[bool]string{true: "/y2", false: "/"}[which == "y2"]),
		expiresAt: now.Add(90 * time.Second),
	}
	appTicketMu.Unlock()
	return code
}

func consumeAppAuthTicket(code string) (appAuthTicket, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return appAuthTicket{}, false
	}
	appTicketMu.Lock()
	t, ok := appTickets[code]
	if ok {
		delete(appTickets, code)
	}
	appTicketMu.Unlock()
	if !ok || time.Now().After(t.expiresAt) {
		return appAuthTicket{}, false
	}
	return t, true
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func handleAppAuth(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	t, ok := consumeAppAuthTicket(r.URL.Query().Get("code"))
	if !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if t.which == "y2" {
		setCookie(w, y2Cookie, encodeURIComponent(t.token), y2TTL)
		y2CatalogKick(t.token)
	} else {
		setCookie(w, cookieName, encodeURIComponent(t.token), cookieTTL)
	}
	http.Redirect(w, r, t.returnURL, http.StatusFound)
}

func handleAppFocus(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	appFocusMu.Lock()
	fn := appFocusHook
	appFocusMu.Unlock()
	if fn == nil {
		writeJSON(w, 404, map[string]any{"ok": false})
		return
	}
	fn()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func verifyAppCandidate(which, blob, cookieHeader string) (string, bool) {
	parts := strings.Split(blob, "\n")
	if which == "old" {
		for _, raw := range parts {
			tok := strings.TrimSpace(reBearer.ReplaceAllString(strings.TrimSpace(raw), ""))
			if len(tok) < 20 || strings.ContainsAny(tok, " \t\r\n") {
				continue
			}
			if u := verifyToken(tok); u != nil {
				return tok, true
			}
		}
		return "", false
	}

	cookieOut := y2CleanCookie(cookieHeader)
	seen := map[string]bool{}
	nowUnix := time.Now().Unix()
	tryJWT := func(raw string) (string, bool) {
		jwt := y2ExtractJwt(strings.TrimSpace(raw))
		if jwt == "" || seen[jwt] {
			return "", false
		}
		seen[jwt] = true
		if exp := y2JwtExp(jwt); exp > 0 && nowUnix > exp {
			return "", false
		}
		ck := cookieOut
		if ck == "" {
			ck = y2CleanCookie(raw)
		}
		if ck != "" && !y2ReNamedCk.MatchString(ck) {
			ck = "_token=" + ck
		}
		if ck == "" {
			ck = "_token=" + jwt
		}
		id, _, err := y2Login(jwt, ck)
		if err != nil || id == nil {
			return "", false
		}
		y2RememberIdentity(jwt, id)
		return jwt, true
	}

	for _, raw := range parts {
		if jwt, ok := tryJWT(raw); ok {
			return jwt, true
		}
	}
	if cookieHeader != "" {
		if jwt, ok := tryJWT(cookieHeader); ok {
			return jwt, true
		}
	}
	return "", false
}
