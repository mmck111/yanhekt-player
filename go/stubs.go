// 三个"对外小接口"：取 token、验 token、签名器状态
//
// 骨架里这里是最小实现，现在换成真的：
//   - tokenOf     ：cookie（yht）优先，其次 Authorization: Bearer
//   - verifyToken ：真的去 /v1/user 验（实现在 api.go，带 10 分钟缓存）
//   - signerStatus：当前签名来源（live:xxx.js / cache / default）
package main

import (
	"net/http"
	"net/url"
	"strings"
)

// tokenOf 取登录 token
func tokenOf(r *http.Request) string {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		// 种 cookie 时是 encodeURIComponent 过的，这里解回来
		if v, err := url.QueryUnescape(c.Value); err == nil {
			return v
		}
		return c.Value
	}
	h := r.Header.Get("authorization")
	if reBearer.MatchString(h) {
		return strings.TrimSpace(reBearer.ReplaceAllString(h, ""))
	}
	return ""
}

// verifyToken 验证登录态（无效返回 nil）
func verifyToken(token string) *userInfo { return apiVerifyUser(token) }

// signerStatus 当前签名来源，显示在 /api/status 里
func signerStatus() string { return signerSource() }
