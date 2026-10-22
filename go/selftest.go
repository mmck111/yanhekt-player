// /api/selftest：把容易出错的判断（学期解析、教室编码、下载结构）跑一遍
//
// 对应 server.js 里 /api/selftest 那一段。这个接口被冒烟测试用来做整体自检。
package main

import (
	"fmt"
	"net/http"
	"strings"
)

type checkItem struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

type selftestOld struct {
	Name     any    `json:"name"`
	Term     any    `json:"term"`
	Total    any    `json:"total"`
	Playable any    `json:"playable"`
	Error    string `json:"error,omitempty"`
}

type selftestDownload struct {
	Session struct {
		Date        string `json:"date"`
		DurationMin any    `json:"durationMin"`
	} `json:"session"`
	Segments        int  `json:"segments"`
	PlaylistMinutes int  `json:"playlistMinutes"`
	HasEndList      bool `json:"hasEndList"`
	MediaSequence   any  `json:"mediaSequence"`
	Encrypted       bool `json:"encrypted"`
}

type selftestLive struct {
	Build    string       `json:"build"`
	OldLogin bool         `json:"oldLogin"`
	Old      *selftestOld `json:"old,omitempty"`
	Verdict  string       `json:"verdict,omitempty"`
	Download any          `json:"download,omitempty"`
}

// handleSelftest /api/selftest
func handleSelftest(w http.ResponseWriter, r *http.Request) {
	out := []checkItem{}
	ok := func(name string, cond bool, detail string) {
		out = append(out, checkItem{Name: name, Pass: cond, Detail: detail})
	}

	a := termKeyOf("2026-2027 第一学期")
	b := termKeyOf("2026-2027学年秋季学期")
	ok("学期写法归一：第一学期", a == b, a+" / "+b)
	c := termKeyOf("2025-2026 第二学期")
	d := termKeyOf("2025-2026春季学期")
	ok("学期写法归一：第二学期", c == d, c)
	ok("本学期算\"近的\"", termIsRecent("2026-2027 第一学期"), fmt.Sprintf("%v", termIsRecent("2026-2027 第一学期")))
	ok("两年前的学期不算近", !termIsRecent("2019-2020 第二学期"), fmt.Sprintf("%v", termIsRecent("2019-2020 第二学期")))
	_, _, parsed := parseTermName("2026-2027 第一学期")
	ok("学期解析", parsed, termParseJSON("2026-2027 第一学期"))

	room := roomName("280101004X", &courseRow{RoomByNumber: &map[string]string{"280101004X": "综教A406"}})
	ok("教室编码→名字", room == "综教A406", room)

	token := tokenOf(r)
	var oldUser *userInfo
	if token != "" {
		oldUser = verifyToken(token)
	}

	live := &selftestLive{Build: build, OldLogin: oldUser != nil}

	// 老平台这边同一条课的实际情况（有 id 才能查）
	cid := strings.TrimSpace(r.URL.Query().Get("id"))
	if cid != "" && oldUser != nil {
		data := getCourse(cid, token, false)
		if data != nil && data.ok {
			old := &selftestOld{Name: data.course.Name}
			if truthy(data.course.Year) {
				old.Term = jstr(data.course.Year) + " 第" + jstr(data.course.Semester) + "学期"
			} else {
				old.Term = ""
			}
			old.Total = data.stats.Total
			old.Playable = data.stats.Playable
			live.Old = old
		} else {
			msg := "读取失败"
			if data != nil && data.errMsg != "" {
				msg = data.errMsg
			}
			live.Old = &selftestOld{Error: msg}
		}
	}

	if live.Old != nil && live.Old.Playable != nil {
		playable := live.Old.Playable.(int)
		total := live.Old.Total.(int)
		switch {
		case playable > 0:
			live.Verdict = fmt.Sprintf("ok（有 %d 讲可播）", playable)
		case total > 0:
			live.Verdict = "no-video（老平台这边确实没有可播录像）"
		default:
			live.Verdict = "no-session（一门课都没排/没权限）"
		}
	}

	// 下载情况：第一讲在 CDN 上到底有多少片、多长、是不是"滑动窗口"
	if cid != "" && oldUser != nil && live.Old != nil && live.Old.Playable != nil && live.Old.Playable.(int) > 0 {
		data := getCourse(cid, token, false)
		var first *sessionRow
		for i := range data.sessions {
			if data.sessions[i].mainURL() != "" || data.sessions[i].vgaURL() != "" {
				first = &data.sessions[i]
				break
			}
		}
		if first != nil {
			vid, err := videoToken(token, oldUser.Badge)
			if err != nil {
				// 和 Node 一样：失败也给出原因，别让自检页看不出发生过什么
				live.Download = map[string]any{"error": err.Error()}
			} else if m, err := resolveMedia(orStr(first.mainURL(), first.vgaURL()), vid); err != nil {
				live.Download = map[string]any{"error": err.Error()}
			} else {
				dl := &selftestDownload{}
				dl.Session.Date = first.Date
				if truthy(first.Duration) {
					dl.Session.DurationMin = int(jnum(first.Duration)/60 + 0.5)
				}
				dl.Segments = len(m.segments)
				dl.PlaylistMinutes = int(m.duration/60 + 0.5)
				dl.HasEndList = m.endlist
				if m.mediaSequence != nil {
					dl.MediaSequence = *m.mediaSequence
				}
				dl.Encrypted = m.encrypted
				live.Download = dl
			}
		}
	}

	allPass := true
	for _, c := range out {
		if !c.Pass {
			allPass = false
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "checks": out, "allPass": allPass, "live": live,
	})
}

// termParseJSON 复刻 JSON.stringify({year,sem}) 的输出
func termParseJSON(name string) string {
	year, sem, ok := parseTermName(name)
	if !ok {
		return "null"
	}
	return fmt.Sprintf(`{"year":%q,"sem":%d}`, year, sem)
}
