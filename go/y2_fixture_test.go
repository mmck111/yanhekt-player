// 解析层的对拍：固定 fixture（aita 的假响应）喂给 Go 的解析函数，
// 和 Node 版 yanhe2.js 用同一份 body 跑出来的结果逐字段比对。
//
// fixture 由 `node tools\parity-y2-fixtures.js` 生成（go\testdata\y2-fixtures.json）：
// 每条都带 body + Node 的返回值（ok/value）或错误文本（ok:false/error）。
//
// 这样"登录之后"的解析路径（profile / 学期 / 检索 / 讲次 / 取流 / 课表 / 我的课程）
// 在没有真 JWT 的环境里也能被验证，而不是只能代码审查。
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type y2FxCase struct {
	Name  string          `json:"name"`
	Fn    string          `json:"fn"`
	Body  json.RawMessage `json:"body"`
	Ok    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error string          `json:"error"`
}

// y2SessionAllFields 把讲次拍成"扫课表"那套完整字段（15 个键全给，不做 omitempty），
// 好和 Node 版 rowOf 的对象逐字段比。
func y2SessionAllFields(s y2Session) map[string]any {
	return map[string]any{
		"courseId": s.CourseID, "subId": s.SubID, "title": s.Title, "subTitle": s.SubTitle,
		"status": s.Status, "duration": s.Duration, "startAt": s.StartAt, "endAt": s.EndAt,
		"teacher": s.Teacher, "room": s.Room, "period": s.Period, "type": s.Type,
		"kind": s.Kind, "drag": s.Drag,
	}
}

func TestY2Fixtures(t *testing.T) {
	// fixture 里的 date 期望值是 Node 版在 Asia/Shanghai(+08:00) 上跑出来的
	// （dateOf 固定换算到 +8，tsOf 又按本地时区解析），换时区就没意义了
	if _, off := time.Now().Zone(); off != 8*3600 {
		t.Skipf("本机时区偏移 %ds 不是 +8 小时，fixture 里的日期期望值不适用", off)
	}
	raw, err := os.ReadFile("testdata/y2-fixtures.json")
	if err != nil {
		t.Fatalf("读不到 fixture（先跑 node tools\\parity-y2-fixtures.js）：%v", err)
	}
	var doc struct {
		Cases []y2FxCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("fixture 解析失败：%v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture 里没有用例")
	}

	for _, c := range doc.Cases {
		var body any
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatalf("%s：body 解析失败 %v", c.Name, err)
		}

		// got: 本实现的结果（any，交给后面统一转 JSON 比较）；errText: 错误文本
		var got any
		errText := ""
		switch c.Fn {
		case "profile":
			if id, err := y2ParseProfile(body); err != nil {
				errText = err.Error()
			} else {
				got = y2IdentityJSON(id)
			}
		case "termList":
			got = y2ParseTermList(body)
		case "searchCourses":
			r := y2ParseCourseList(body)
			got = map[string]any{"courses": r.courses, "total": r.total}
		case "courseDetail":
			d := y2ParseDetail("96123", body)
			if d == nil {
				errText = "get-course-detail 没返回可用数据"
			} else {
				got = map[string]any{
					"courseId": d.courseID, "title": d.title,
					"termName": d.termName, "sessions": d.sessions,
				}
			}
		case "subInfo":
			if info, err := y2ParseSubInfo(body); err != nil {
				errText = err.Error()
			} else {
				got = info
			}
		case "weekSchedule":
			got = y2ParseWeekSchedule(body)
		case "dayRows":
			rows := []map[string]any{}
			for _, s := range y2ParseDayRows(body) {
				rows = append(rows, y2SessionAllFields(s))
			}
			got = rows
		case "myCourses":
			got = y2ParseMyCourses(body)
		default:
			t.Fatalf("%s：未知的 fn=%q", c.Name, c.Fn)
		}

		if !c.Ok {
			if errText != c.Error {
				t.Errorf("%s：错误文本 = %q，Node 版是 %q", c.Name, errText, c.Error)
			}
			continue
		}
		if errText != "" {
			t.Errorf("%s：Go 报了错 %q，Node 版成功", c.Name, errText)
			continue
		}

		// 两边都转成 JSON 再比，避开 Go 具体类型（int / int64 / 指针）的差异
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s：结果无法序列化 %v", c.Name, err)
		}
		var gotAny any
		if err := json.Unmarshal(gotJSON, &gotAny); err != nil {
			t.Fatalf("%s：结果回读失败 %v", c.Name, err)
		}
		var wantAny any
		if err := json.Unmarshal(c.Value, &wantAny); err != nil {
			t.Fatalf("%s：期望值解析失败 %v", c.Name, err)
		}
		if !reflect.DeepEqual(gotAny, wantAny) {
			gb, _ := json.Marshal(gotAny)
			wb, _ := json.Marshal(wantAny)
			t.Errorf("%s：\n  Go   = %s\n  Node = %s", c.Name, gb, wb)
		}
	}
}
