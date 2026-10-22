// /diag 用的搜索变体探针：把同一个关键词按各种参数名都打一次上游，
// 看哪个参数真的做「包含匹配」，以及各自的返回结构长什么样。
//
// 只在 /diag 里用，正常搜索路径不调用。
package main

// y2DiagSearch 返回每种参数写法的上游响应摘要（不含 token）
func y2DiagSearch(jwt, q string) []map[string]any {
	type variant struct {
		name   string
		params y2Params
	}
	base := func(extra ...y2Param) y2Params {
		p := y2Params{
			y2P("term", ""), y2P("page", 1), y2P("per_page", 20), y2P("tenant", y2Tenant),
		}
		return append(p, extra...)
	}
	variants := []variant{
		{"title", base(y2P("title", q), y2P("order", ""), y2P("publish", ""), y2P("struc_ids", []any{}))},
		{"title-plain", base(y2P("title", q))},
		{"like_title", base(y2P("like_title", q))},
		{"title_like", base(y2P("title_like", q))},
		{"keyword", base(y2P("keyword", q))},
		{"name", base(y2P("name", q))},
		{"search", base(y2P("search", q))},
		{"q", base(y2P("q", q))},
		{"course_name", base(y2P("course_name", q))},
		{"无 title（第 1 页）", y2Params{y2P("page", 1), y2P("per_page", 20), y2P("tenant", y2Tenant)}},
		{"无 title（第 2 页）", y2Params{y2P("page", 2), y2P("per_page", 20), y2P("tenant", y2Tenant)}},
	}

	out := []map[string]any{}
	for _, v := range variants {
		entry := map[string]any{"参数": v.name}
		raw, err := y2Req("GET", y2APIURL(y2CourseListPath, v.params), jwt)
		if err != nil {
			entry["error"] = err.Error()
			out = append(out, entry)
			continue
		}
		entry["http"] = raw.Status
		code := any(nil)
		if b := jobj(raw.JSON); b != nil {
			code = b["code"]
		}
		entry["code"] = code
		r := y2ParseCourseList(raw.JSON)
		entry["total"] = r.total
		entry["返回条数"] = len(r.courses)
		head := []map[string]any{}
		for i, c := range r.courses {
			if i >= 10 {
				break
			}
			head = append(head, map[string]any{
				"id": c["courseId"], "title": jstr(c["title"]),
				"teacher": y2RuneCut(jstr(c["teacher"]), 12),
				"termName": jstr(c["termName"]), "sessions": c["sessions"],
			})
		}
		if len(head) > 0 {
			entry["样例"] = head
		}
		if len(r.courses) == 0 {
			entry["原文"] = y2Snip(raw.Body)
			if raw.JSON != nil {
				entry["形状"] = y2Shape(raw.JSON)
			}
		}
		out = append(out, entry)
	}
	return out
}

// y2DiagCourse 一门课的真实讲次情况：detail 拿到的讲次数、状态分布、能不能看到流
func y2DiagCourse(jwt, courseID, account string) map[string]any {
	out := map[string]any{"courseId": courseID}
	d, err := y2CourseDetail(jwt, courseID, account)
	if err != nil {
		out["detailError"] = err.Error()
		return out
	}
	out["title"] = d.title
	out["termName"] = d.termName
	out["讲次数"] = len(d.sessions)
	status := map[string]int{}
	for _, s := range d.sessions {
		status[s.Status]++
	}
	out["状态分布"] = status
	head := []map[string]any{}
	for i, s := range d.sessions {
		if i >= 5 {
			break
		}
		head = append(head, map[string]any{
			"subId": s.SubID, "title": y2RuneCut(s.Title, 16),
			"status": s.Status, "date": s.Date,
		})
	}
	if len(head) > 0 {
		out["讲次样例"] = head
	}
	ok, failed := 0, 0
	if len(d.sessions) > 0 {
		for i, s := range d.sessions {
			if i >= 3 {
				break
			}
			if y, f := y2SessionHasStream(jwt, courseID, s.SubID); y {
				ok++
			} else if f {
				failed++
			}
		}
	}
	out["抽 3 讲能取到流"] = ok
	out["抽 3 讲取流失败"] = failed
	return out
}
