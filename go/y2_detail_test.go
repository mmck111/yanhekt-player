package main

import (
	"encoding/json"
	"testing"
)

// get-course-detail 的 sub_list 是按日期分组的对象；只取第一组就会少算成 1 讲。
// 这里固定一份真实形状的响应，保证以后不会再退化。
const y2GroupedDetail = `{"code":0,"msg":"success","data":{
 "id":"111630","title":"线性代数",
 "sub_list":{
   "2026.9.4":[{"id":"1","sub_status":"6","start_at":"1790000000","sub_title":"第1讲"}],
   "2026.9.11":[{"id":"2","sub_status":"6","start_at":"1790100000"},{"id":"3","sub_status":"6","start_at":"1790200000"}],
   "2026.9.18":[{"id":"4","sub_status":"6","start_at":"1790300000"},{"id":"5","sub_status":"5","start_at":"1790400000"}]
 }}}`

func TestY2ParseDetailFlattensGroupedSubList(t *testing.T) {
	var body any
	if err := json.Unmarshal([]byte(y2GroupedDetail), &body); err != nil {
		t.Fatal(err)
	}
	d := y2ParseDetail("111630", body)
	if d == nil {
		t.Fatal("y2ParseDetail 返回 nil")
	}
	if len(d.sessions) != 5 {
		t.Fatalf("分组 sub_list 应摊平成 5 讲，实际 %d 讲", len(d.sessions))
	}
	ids := map[string]bool{}
	for _, s := range d.sessions {
		ids[s.SubID] = true
	}
	for _, want := range []string{"1", "2", "3", "4", "5"} {
		if !ids[want] {
			t.Errorf("缺少讲次 %s（共 %v）", want, ids)
		}
	}
}

// get-course-info 的 sub_list 是平铺数组，状态字段叫 status 而不是 sub_status
const y2FlatInfo = `{"code":0,"msg":"success","data":{
 "course_id":"103994","title":"大学物理AⅡ",
 "sub_list":[
   {"id":"11","status":"6","start_at":"1790214900","type":"course_live"},
   {"id":"12","status":"6","start_at":"1790301300","type":"course_live"},
   {"id":"13","status":"5","start_at":"1790387700","type":"course_live"}
 ]}}`

func TestY2ParseDetailFlatInfo(t *testing.T) {
	var body any
	if err := json.Unmarshal([]byte(y2FlatInfo), &body); err != nil {
		t.Fatal(err)
	}
	d := y2ParseDetail("103994", body)
	if d == nil {
		t.Fatal("y2ParseDetail 返回 nil")
	}
	if len(d.sessions) != 3 {
		t.Fatalf("平铺 sub_list 应有 3 讲，实际 %d", len(d.sessions))
	}
	if d.title != "大学物理AⅡ" {
		t.Errorf("标题解析错了：%q", d.title)
	}
	if d.sessions[0].Status != "6" {
		t.Errorf("状态应取 status 字段，实际 %q", d.sessions[0].Status)
	}
}
