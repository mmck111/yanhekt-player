// 上游接口（cbiz.yanhekt.cn）与通用 HTTP 工具
//
// 对应 server.js 里的 apiCall / verifyToken / videoToken，以及各处用到的
// "JS 风格的取值" 辅助函数（因为上游 JSON 的字段类型并不严谨，
// code 有时是 0 有时是 "0"，id 有时是数字有时是字符串）。
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	origin      = "https://www.yanhekt.cn"
	apiBase     = "https://cbiz.yanhekt.cn"
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	cookieName  = "yht"
	cookieTTL   = 6 * 24 * 3600
	maxJSONBody = 64 << 20 // 上游 JSON 最多读这么点，防止被喂爆内存
)

// 上游证书一律不校验（学校的 CDN 证书链经常不全，Node 版也是 rejectUnauthorized:false）
var insecureTLS = &tls.Config{InsecureSkipVerify: true}

// 接口调用走的客户端：只发小 JSON 请求，不排队
var apiClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableCompression:    true,
	},
}

// ---------------------------------------------------------------- JSON 取值

// jobj 取对象；不是对象就返回 nil（对应 JS 里 obj.x || {}）
func jobj(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// jarr 取数组；不是数组就返回 nil
func jarr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// jstr 近似 JS 的 String(x || ”)：null / 缺失 → ""
func jstr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	}
	return ""
}

// jnum 近似 JS 的 Number(x)：解析不了就是 NaN，用 0 代替
func jnum(v any) float64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0
		}
		return f
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return 0
}

// jint 取整数
func jint(v any) int { return int(jnum(v)) }

// truthy 近似 JS 的真值判断（用于 `a || b` 这种写法）
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return true
	}
	return true
}

// orStr 对应 JS 的 `a || b`（字符串场景）
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// codeIsZeroStrict 对应 Node 里的 `x.code === 0`（数字 0 才算）
func codeIsZeroStrict(j map[string]any) bool {
	f, ok := j["code"].(float64)
	return ok && f == 0
}

// codeIsZeroLoose 对应 Node 里的 `x.code !== 0 && x.code !== '0'`
func codeIsZeroLoose(j map[string]any) bool {
	switch t := j["code"].(type) {
	case float64:
		return t == 0
	case string:
		return t == "0"
	case json.Number:
		return t.String() == "0"
	}
	return false
}

// ---------------------------------------------------------------- 上游接口

// apiCall 调一个 cbiz 接口，返回解析后的 JSON 对象。
// p 是路径 + 查询串（已经拼好），token 为空表示匿名。
func apiCall(p, token string) (map[string]any, error) {
	req, err := http.NewRequest("GET", apiBase+p, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Xdomain-Client", "web_user")
	req.Header.Set("xclient-version", "v1")
	req.Header.Set("xclient-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("API 返回非 JSON")
	}
	return out, nil
}

// ---------------------------------------------------------------- 用户身份

type userInfo struct {
	ID       any    `json:"id"`
	Badge    any    `json:"badge"`
	Nickname string `json:"nickname"`
}

type userEntry struct {
	at   time.Time
	user *userInfo
}

var (
	userMu    sync.Mutex
	userCache = map[string]userEntry{}
	userTTL   = 10 * time.Minute
)

// apiVerifyUser 用 /v1/user 验证登录态；有效返回用户，无效返回 nil。
// 结果缓存 10 分钟（Node 版一样）。对外的名字是 verifyToken（见 stubs.go）。
func apiVerifyUser(token string) *userInfo {
	if token == "" {
		return nil
	}
	userMu.Lock()
	if hit, ok := userCache[token]; ok && time.Since(hit.at) < userTTL {
		userMu.Unlock()
		return hit.user
	}
	userMu.Unlock()

	r, err := apiCall("/v1/user", token)
	if err == nil && codeIsZeroStrict(r) {
		if d := jobj(r["data"]); d != nil && truthy(d["id"]) {
			u := &userInfo{ID: d["id"], Badge: d["badge"], Nickname: jstr(d["nickname"])}
			userMu.Lock()
			if len(userCache) > 500 {
				userCache = map[string]userEntry{}
			}
			userCache[token] = userEntry{at: time.Now(), user: u}
			userMu.Unlock()
			return u
		}
	}

	userMu.Lock()
	delete(userCache, token)
	userMu.Unlock()
	return nil
}

// ---------------------------------------------------------------- 视频 token

type vtEntry struct {
	vidToken  string
	expiredAt float64
}

var (
	vtMu    sync.Mutex
	vtCache = map[string]vtEntry{}
)

// videoToken 拿 CDN 签名用的视频 token（按用户缓存，快过期才刷新）
func videoToken(userToken string, badge any) (string, error) {
	vtMu.Lock()
	hit, ok := vtCache[userToken]
	vtMu.Unlock()
	now := float64(time.Now().Unix())
	if ok && hit.expiredAt-now > 120 {
		return hit.vidToken, nil
	}

	res, err := apiCall("/v1/auth/video/token?id="+jstr(badge), userToken)
	if err != nil {
		return "", err
	}
	d := jobj(res["data"])
	if d == nil || !truthy(d["token"]) {
		return "", fmt.Errorf("取视频 token 失败（登录可能已过期）")
	}
	vtMu.Lock()
	if len(vtCache) > 500 {
		vtCache = map[string]vtEntry{}
	}
	vtCache[userToken] = vtEntry{vidToken: jstr(d["token"]), expiredAt: jnum(d["expired_at"])}
	vtMu.Unlock()
	return jstr(d["token"]), nil
}

// ---------------------------------------------------------------- 杂项

// encodeURIComponent 和 JS 的同名函数行为一致（空格是 %20，不是 +）
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' || c == '\'' || c == '(' || c == ')' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// setCookie 种登录 cookie（HttpOnly + SameSite=Lax，和 Node 版一致）
func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if maxAge <= 0 {
		// Go 只在 MaxAge<0 时才输出 "Max-Age=0"；Node 版退出时明确带 Max-Age=0
		c.MaxAge = -1
		c.Value = ""
	}
	http.SetCookie(w, c)
}
