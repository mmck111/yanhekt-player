// 新版平台（aita.yanhekt.cn）基础层
//
// 对应 yanhe2.js 的 tokenCookie / req / apiUrl / getJson / tryJson / extractJwt /
// login / profile / signPath / signedUrl，以及 tsOf / dateOf / text / num / pick / arr
// 这些"按 JS 语义取值"的辅助函数（上游 JSON 的类型并不严谨）。
//
// 和老平台完全是两套东西：
//   - 登录：24 小时 JWT（浏览器里的 `_token` cookie）
//   - 鉴权：每个请求带 t={userId}-{unix}-md5(pathname+userId+tenantId+reverse(phone)+unix)
//   - 播放：/play/ 下是支持 Range 的 MP4
//
// 手机号字段是签名密钥，只留在内存里，不写盘、不进日志。
package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	y2Base   = "https://aita.yanhekt.cn"
	y2Tenant = 21
	y2UA     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.3"
	y2Cookie = "y2tok"          // 新版 JWT 的本地 cookie 名（老平台是 yht）
	y2TTL    = 24 * 3600        // 本地 cookie 的有效期（新版 JWT 本身是 24 小时）
	y2APITTL = 25 * time.Second // 上游 JSON 接口的超时（Node 是 socket 空闲 25s）
	y2Tail   = 12               // 缓存键取 JWT 尾巴的位数（Node 的 jwt.slice(-12)）
	y2SegMax = 8 << 20          // m3u8 播放列表最大读这么多
	y2HdrKey = "tenant-id"      // 网关要的租户头
)

// 上游 JSON 接口的客户端：只发小请求，超时 25s（Node 版是 socket 空闲 25s）
var y2Client = &http.Client{
	Timeout: y2APITTL,
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: y2APITTL,
		DisableCompression:    true,
	},
}

// /play/ 流媒体的客户端：不能设整体超时（要整份转发几分钟的 MP4），
// 只限"等首字节 60s"（Node 是 socket 空闲 60s）
var y2StreamClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxConnsPerHost:       4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true,
	},
}

// ---------------------------------------------------------------- 取值辅助

// y2Text 近似 yanhe2.js 的 text()：字符串 trim、数字转字符串、其它给空串
func y2Text(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

// y2Num 近似 num()：Number(v) 有限且 > 0 时取整，否则 0
func y2Num(v any) int {
	f := jnum(v)
	if f > 0 {
		return int(f)
	}
	return 0
}

// y2Pick 近似 pick(o, ...keys)：第一个"不是 undefined / null / 空串"的值（原样返回）
func y2Pick(o map[string]any, keys ...string) any {
	if o == nil {
		return ""
	}
	for _, k := range keys {
		v, ok := o[k]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr && s == "" {
			continue
		}
		return v
	}
	return ""
}

// y2PickStr 是 pick + text 的简写（最常用的一种）
func y2PickStr(o map[string]any, keys ...string) string { return y2Text(y2Pick(o, keys...)) }

// y2Truthy 近似 JS 的真值判断。
// 注意：和 api.go 里的 truthy 不同 —— JS 里空数组、空对象都是**真**，
// 而 `a || b` 这种写法在上游数据里到处都有，差一个就选错分支。
func y2Truthy(v any) bool {
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
	case int64:
		return t != 0
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	}
	return true // 数组 / 对象（含空）都算真
}

// y2First 对应 JS 的 `a || b || c`：返回第一个为真的值，全假返回 nil
func y2First(vs ...any) any {
	for _, v := range vs {
		if y2Truthy(v) {
			return v
		}
	}
	return nil
}

// y2Arr 近似 arr()：数组原样；对象取所有值；其它空
func y2Arr(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if m, ok := v.(map[string]any); ok {
		out := make([]any, 0, len(m))
		for _, k := range y2Keys(m) {
			out = append(out, m[k])
		}
		return out
	}
	return nil
}

// y2Keys 对象的键（排序后返回：Go 的 map 无序，Node 用 JSON 里的插入顺序）
func y2Keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// y2HasKey 对应 JS 的 `'k' in o`（键存在即算，值是不是 null 不管）
func y2HasKey(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

var (
	y2DigitsRe = regexp.MustCompile(`^\d+$`)
	y2DateOnly = regexp.MustCompile(`^\d{4}/\d{1,2}/\d{1,2}$`)
	y2SpaceRun = regexp.MustCompile(`\s+`)
)

// y2TsOf epoch 秒 / 毫秒 / "YYYY-MM-DD HH:mm:ss" 都能转成秒（0 表示认不出）
func y2TsOf(v any) int64 {
	if v == nil {
		return 0
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return 0
	}
	// 数字，或者纯数字字符串 → 按时间戳（毫秒自动降级到秒）
	asNum := false
	var n float64
	switch t := v.(type) {
	case float64:
		n, asNum = t, true
	case int:
		n, asNum = float64(t), true
	case int64:
		n, asNum = float64(t), true
	case json.Number:
		f, err := t.Float64()
		n, asNum = f, err == nil
	case string:
		if y2DigitsRe.MatchString(t) {
			f, err := strconv.ParseFloat(t, 64)
			n, asNum = f, err == nil
		}
	}
	if asNum {
		if n <= 0 || n != n { // n != n 等价于 NaN
			return 0
		}
		if n > 1e12 {
			return int64(n / 1000)
		}
		return int64(n)
	}

	// 其余当成日期字符串：Node 会把 '-' 换成 '/' 之后交给 Date.parse（本地时区）
	s := strings.Replace(strings.TrimSpace(y2Text(v)), "T", " ", 1)
	ss := strings.ReplaceAll(s, "-", "/")
	if y2DateOnly.MatchString(ss) {
		ss += " 00:00:00"
	}
	for _, layout := range []string{
		"2006/01/02 15:04:05", "2006/01/02 15:04", "2006/01/02",
		"2006/1/2 15:04:05", "2006/1/2 15:04", "2006/1/2",
	} {
		if t, err := time.ParseInLocation(layout, ss, time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// y2DateOf 时间戳 → 北京时间 "YYYY-MM-DD"（认不出给空串）
func y2DateOf(v any) string {
	ts := y2TsOf(v)
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
}

// ---------------------------------------------------------------- 鉴权写法

// aita 的网关有时候认 Authorization，有时候只认 _token cookie（浏览器两样都带）。
// 这里跟着登录探测的结果切换，探测到哪种能过就一直用它。
var (
	y2AuthMu    sync.Mutex
	y2AuthStyle = "off" // off | plain | serialized | raw
	y2RawCookie string
)

// y2SetAuthStyle 记住能过的鉴权写法（非法值忽略，和 Node 版一致）
func y2SetAuthStyle(s string) {
	switch s {
	case "off", "plain", "serialized", "raw":
		y2AuthMu.Lock()
		y2AuthStyle = s
		y2AuthMu.Unlock()
	}
}

// y2GetAuthStyle 当前用的鉴权写法
func y2GetAuthStyle() string {
	y2AuthMu.Lock()
	defer y2AuthMu.Unlock()
	return y2AuthStyle
}

func y2SetRawCookie(s string) {
	y2AuthMu.Lock()
	y2RawCookie = s
	y2AuthMu.Unlock()
}

func y2GetRawCookie() string {
	y2AuthMu.Lock()
	defer y2AuthMu.Unlock()
	return y2RawCookie
}

// y2TokenCookie 按当前鉴权写法生成 Cookie 头：
//
//	off        —— 不发 cookie，只发 Authorization: Bearer
//	plain      —— Cookie: _token=<jwt>
//	serialized —— Cookie: _token=<PHP 序列化串>
//	raw        —— 原样把用户粘进来的整段 cookie 发过去
func y2TokenCookie(jwt string) string {
	switch y2GetAuthStyle() {
	case "off":
		return ""
	case "raw":
		return y2GetRawCookie()
	case "serialized":
		v := `a:2:{i:0;s:6:"_token";i:1;s:` + strconv.Itoa(len(jwt)) + `:"` + jwt + `";}`
		return "_token=" + encodeURIComponent(v)
	}
	return "_token=" + jwt
}

// ---------------------------------------------------------------- HTTP

// y2Resp 一次上游请求的结果（Body 留着是为了错误信息里能带一段原文）
type y2Resp struct {
	Status int
	Header http.Header
	JSON   any
	Body   []byte
}

// y2Req 发一次 aita 请求。
// 注意：Node 版的 req 还支持写 body，但 y2 这几个接口全是 GET，这里没留。
func y2Req(method, rawURL, jwt string) (*y2Resp, error) {
	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", y2UA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// 网关/CDN 会看来源页：http 请求头里的 Referer 只能是路径级的
	req.Header.Set("Referer", y2Base+"/portal/")
	req.Header.Set("Origin", y2Base)
	req.Header.Set(y2HdrKey, strconv.Itoa(y2Tenant))
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
		if c := y2TokenCookie(jwt); c != "" {
			req.Header.Set("Cookie", c)
		}
	}

	resp, err := y2Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody))
	if err != nil {
		return nil, err
	}
	var parsed any
	if json.Unmarshal(body, &parsed) != nil || parsed == nil {
		parsed = nil // Node 的 JSON.parse 失败、或者结果是 null，都当"没有 JSON"
	}
	return &y2Resp{Status: resp.StatusCode, Header: resp.Header, JSON: parsed, Body: body}, nil
}

// y2FormEncode 等价于 URLSearchParams 的序列化：空格变 '+'，
// 只有 [A-Za-z0-9*-._] 保持原样，其余按 UTF-8 逐字节 %XX（大写）。
// 不能用 url.QueryEscape：它保留的字符集和 JS 那套不一样（'~' '!' '(' ')' 等）。
func y2FormEncode(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// y2DecodeURIComponent 等价于 JS 的 decodeURIComponent：只处理 %XX，'+' 原样保留；
// 残缺的 % 序列报错，调用方保持原值（对应 Node 的 try/catch）
func y2DecodeURIComponent(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", errors.New("bad escape")
		}
		h, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", errors.New("bad escape")
		}
		b.WriteByte(byte(h))
		i += 2
	}
	return b.String(), nil
}

// y2Param 一个查询参数
type y2Param struct {
	k string
	v any
}

// y2Params 有序参数表。
// Node 的 URLSearchParams 是按插入顺序拼的，Go 的 map 不是 —— 想逐字符对上上游地址
// 就必须保留插入顺序。
type y2Params []y2Param

func y2P(k string, v any) y2Param { return y2Param{k: k, v: v} }

// y2APIURL 拼一个 aita 接口地址（对应 yanhe2.js 的 apiUrl）
func y2APIURL(path string, params y2Params) string {
	var q strings.Builder
	add := func(k, v string) {
		if q.Len() > 0 {
			q.WriteByte('&')
		}
		q.WriteString(y2FormEncode(k))
		q.WriteByte('=')
		q.WriteString(y2FormEncode(v))
	}
	for _, p := range params {
		// 空值直接跳过（Node: undefined / null / ''）
		if p.v == nil {
			continue
		}
		if s, ok := p.v.(string); ok && s == "" {
			continue
		}
		switch arr := p.v.(type) {
		case []any:
			if len(arr) == 0 {
				continue
			}
			for _, x := range arr {
				if x == nil {
					continue
				}
				s := jstr(x)
				if s == "" {
					continue
				}
				add(p.k+"[]", s)
			}
		case []string:
			if len(arr) == 0 {
				continue
			}
			for _, x := range arr {
				if x == "" {
					continue
				}
				add(p.k+"[]", x)
			}
		default:
			add(p.k, jstr(p.v))
		}
	}
	if q.Len() == 0 {
		return y2Base + path
	}
	return y2Base + path + "?" + q.String()
}

// y2Snip 错误信息里带一段响应原文（最多 90 个字符，空白压成一个空格）
func y2Snip(body []byte) string {
	t := strings.TrimSpace(y2SpaceRun.ReplaceAllString(string(body), " "))
	if t == "" {
		return ""
	}
	r := []rune(t)
	if len(r) > 90 {
		r = r[:90]
	}
	return "：" + string(r)
}

// y2GetJSON 取一个接口的 JSON；401/403 / 非 2xx / 非 JSON 都返回错误
func y2GetJSON(jwt, path string, params y2Params) (any, error) {
	res, err := y2Req("GET", y2APIURL(path, params), jwt)
	if err != nil {
		return nil, err
	}
	if res.Status == 401 || res.Status == 403 {
		return nil, fmt.Errorf("Yanhe2 拒绝访问（HTTP %d%s）", res.Status, y2Snip(res.Body))
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("Yanhe2 HTTP %d", res.Status)
	}
	if res.JSON == nil {
		return nil, errors.New("Yanhe2 返回非 JSON")
	}
	return res.JSON, nil
}

// y2TryJSON 不抛错的探测：失败时带上错误信息，便于上层自动降级
func y2TryJSON(jwt, path string, params y2Params) (any, error) {
	return y2GetJSON(jwt, path, params)
}

// y2IsAuthRejected 对应 Node 里的 /会话被拒绝/ 判断。
// 老实说 y2GetJSON 的错误文案里从来没有这个词（是"Yanhe2 拒绝访问"），
// 所以这几处"遇到鉴权失败就直接抛出去"的分支在 Node 版里也是死的 ——
// 为了行为和注释都对齐，这里照样保留。
func y2IsAuthRejected(msg string) bool { return strings.Contains(msg, "会话被拒绝") }

// ---------------------------------------------------------------- JWT

var (
	y2ReJwtStart   = regexp.MustCompile(`^(a:\d+:|eyJ)`)
	y2RePercent    = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)
	y2RePhpWrapped = regexp.MustCompile(`\{i:\d+;s:\d+:"_token";i:\d+;s:\d+:"(.+?)";\}`)
	y2ReNamedToken = regexp.MustCompile(`(?:^|[;\s])_token=([^;\s]+)`)
	y2ReJwtBody    = regexp.MustCompile(`eyJ[A-Za-z0-9_\-\+\/]+\.[A-Za-z0-9_\-\+\/]+\.[A-Za-z0-9_\-\+\/=]+`)
)

// y2ExtractJwt 从用户粘进来/书签带进来的一坨东西里抠出 JWT。
// 可能是：裸 JWT、整段 cookie、URL 编码过的 cookie、PHP 序列化的 cookie 值。
func y2ExtractJwt(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// 已经是 JWT 或 PHP 序列化串就别解码了（里面可能带 % 号）
	if !y2ReJwtStart.MatchString(s) && y2RePercent.MatchString(s) {
		if d, err := y2DecodeURIComponent(s); err == nil {
			s = d
		}
	}
	if m := y2RePhpWrapped.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := y2ReNamedToken.FindStringSubmatch(s); m != nil {
		v := m[1]
		if d, err := y2DecodeURIComponent(v); err == nil {
			v = d
		}
		if inner := y2RePhpWrapped.FindStringSubmatch(v); inner != nil {
			return strings.TrimSpace(inner[1])
		}
		return strings.TrimSpace(v)
	}
	if m := y2ReJwtBody.FindString(s); m != "" {
		return m
	}
	// 兜底：整段当 token，交给服务器判
	return s
}

// y2JwtExp JWT 里的 exp（秒）；解析不了就返回 0
func y2JwtExp(jwt string) int64 {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return 0
	}
	p := strings.ReplaceAll(strings.ReplaceAll(parts[1], "-", "+"), "_", "/")
	var b []byte
	var err error
	if b, err = decodeBase64Loose(p); err != nil {
		return 0
	}
	var claims map[string]any
	if json.Unmarshal(b, &claims) != nil {
		return 0
	}
	f := jnum(claims["exp"])
	return int64(f)
}

// decodeBase64Loose 按 Node 的 Buffer.from(s, 'base64') 的宽容度解码：
// 有填充没填充都行，标准表和 URL 表都认
func decodeBase64Loose(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return nil, errors.New("bad base64")
}

// ---------------------------------------------------------------- 账号信息

// y2Identity JWT → 身份（含签名用的 phone），只放内存里。
// 字段故意不导出：手机号只在这个包里出现，不会被顺手序列化出去。
type y2Identity struct {
	userId   int
	tenantId int
	phone    string
	account  string
	realname string
}

// y2IdentityJSON 把身份摊成可比较的字段（测试 / 诊断用，不含 phone 之外的额外信息）
func y2IdentityJSON(id *y2Identity) map[string]any {
	if id == nil {
		return nil
	}
	return map[string]any{
		"userId": id.userId, "tenantId": id.tenantId, "phone": id.phone,
		"account": id.account, "realname": id.realname,
	}
}

// y2Profile 对应 yanhe2.js 的 profile：/userapi/v1/infosimple → 身份
func y2Profile(jwt string) (*y2Identity, error) {
	body, err := y2GetJSON(jwt, "/userapi/v1/infosimple", nil)
	if err != nil {
		return nil, err
	}
	return y2ParseProfile(body)
}

// y2ParseProfile 只做解析（测试里用固定 JSON 直接调它）
func y2ParseProfile(body any) (*y2Identity, error) {
	b := jobj(body)
	p := jobj(j2get(b, "params"))
	if p == nil {
		return nil, errors.New("infosimple 返回结构异常")
	}
	userId := y2Num(p["id"])
	tenantId := y2Num(p["tenant_id"])
	if tenantId == 0 {
		tenantId = y2Tenant
	}
	phone := y2Text(p["phone"])
	if userId == 0 {
		return nil, errors.New("infosimple 缺少 id")
	}
	return &y2Identity{
		userId:   userId,
		tenantId: tenantId,
		phone:    phone,
		account:  y2Text(p["account"]),
		realname: y2Text(p["realname"]),
	}, nil
}

// j2get 从 map 里取值；m 为 nil 时给 nil（对应 JS 的 `o && o[k]`）
func j2get(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

// y2Login 登录验证：一次试几种鉴权写法，哪种能过就一直用哪种。
// 返回身份和最终采用的写法；全失败抛最后一次的错误。
func y2Login(jwt, rawCookie string) (*y2Identity, string, error) {
	styles := []string{"off", "plain", "serialized"}
	if rawCookie != "" && (strings.Contains(rawCookie, "=") || strings.Contains(rawCookie, ";")) {
		styles = append(styles, "raw")
	}
	keep, keepRaw := y2GetAuthStyle(), y2GetRawCookie()
	y2SetRawCookie(rawCookie)

	var lastErr error
	for _, s := range styles {
		y2SetAuthStyle(s)
		id, err := y2Profile(jwt)
		if err == nil {
			return id, s, nil
		}
		lastErr = err
	}
	y2SetAuthStyle(keep)
	y2SetRawCookie(keepRaw)
	if lastErr == nil {
		lastErr = errors.New("Yanhe2 登录验证失败")
	}
	return nil, "", lastErr
}

// ---------------------------------------------------------------- /play/ 签名

// y2SignPath /play/ 签名：
// t = {userId}-{unix}-md5(pathname + userId + tenantId + reverse(phone) + unix)
func y2SignPath(pathname string, id *y2Identity, ts int64) string {
	rev := y2Reverse(id.phone)
	raw := pathname + strconv.Itoa(id.userId) + strconv.Itoa(id.tenantId) + rev + strconv.FormatInt(ts, 10)
	sum := md5.Sum([]byte(raw))
	return strconv.Itoa(id.userId) + "-" + strconv.FormatInt(ts, 10) + "-" + hex.EncodeToString(sum[:])
}

// y2Reverse 按字符反转（手机号，都是 ASCII）
func y2Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// y2SignedURL 给 /play/ 地址补上 t 参数（对应 yanhe2.js 的 signedUrl）
func y2SignedURL(rawURL string, id *y2Identity) (string, error) {
	return y2SignedURLAt(rawURL, id, time.Now().Unix())
}

// y2SignedURLAt 是指定时间戳的版本（测试用：Node 那边把 Date.now 钉死再对拍）
func y2SignedURLAt(rawURL string, id *y2Identity, now int64) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	sig := y2SignPath(u.EscapedPath(), id, now)

	// Node 是 u.searchParams.delete('t') 再 set，整条 query 会被重新序列化 ——
	// 这里照着做：先按原顺序解出所有参数，丢掉 t，再按 URLSearchParams 的规则重新编码。
	var q strings.Builder
	add := func(k, v string) {
		if q.Len() > 0 {
			q.WriteByte('&')
		}
		q.WriteString(y2FormEncode(k))
		q.WriteByte('=')
		q.WriteString(y2FormEncode(v))
	}
	if u.RawQuery != "" {
		for _, pair := range strings.Split(u.RawQuery, "&") {
			if pair == "" {
				continue
			}
			k, v := pair, ""
			if i := strings.IndexByte(pair, '='); i >= 0 {
				k, v = pair[:i], pair[i+1:]
			}
			dk, dv := k, v
			if d, err := url.QueryUnescape(k); err == nil {
				dk = d
			}
			if d, err := url.QueryUnescape(v); err == nil {
				dv = d
			}
			if dk == "t" {
				continue
			}
			add(dk, dv)
		}
	}
	add("t", sig)
	u.RawQuery = q.String()
	return u.String(), nil
}
