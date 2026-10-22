package main

import (
	"encoding/json"
	"testing"
)

const y2CatFixture = `{"code":0,"msg":"success","data":[
 {"course_id":"1001","title":"大学物理AⅡ","teacher":"朱星宝","term":"2026-2027 第一学期","course_code":"100180121","sub_count":40},
 {"course_id":"1002","title":"大学物理实验","teacher":"张三","term":"2026-2027 第一学期","course_code":"100180122"},
 {"course_id":"1003","title":"数值分析","teacher":"孙新","term":"2025-2026 第二学期","course_code":"100200001","sub_count":16}
]}`

func y2CatFixtureParse(t *testing.T) *y2Catalog {
	t.Helper()
	var body any
	if err := json.Unmarshal([]byte(y2CatFixture), &body); err != nil {
		t.Fatal(err)
	}
	items := y2CatItems(body)
	if len(items) != 3 {
		t.Fatalf("解析出 %d 条，期望 3 条", len(items))
	}
	cat := &y2Catalog{Ver: 1}
	for _, it := range items {
		r := y2CatFromItem(jobj(it))
		if r == nil {
			t.Fatal("y2CatFromItem 返回 nil")
		}
		cat.Rows = append(cat.Rows, *r)
	}
	return cat
}

func TestY2CatalogParse(t *testing.T) {
	cat := y2CatFixtureParse(t)
	first := cat.Rows[0]
	if first.ID != "1001" || first.Title != "大学物理AⅡ" || first.Teacher != "朱星宝" {
		t.Errorf("第一条解析错了: %+v", first)
	}
	if first.Subs != 40 {
		t.Errorf("节次数 = %d，期望 40", first.Subs)
	}
	if cat.Rows[1].Subs != 0 {
		t.Errorf("没给节次数的应该是 0，实际 %d", cat.Rows[1].Subs)
	}
}

func TestY2CatalogSearchContains(t *testing.T) {
	cat := y2CatFixtureParse(t)
	hits := y2CatalogSearch(cat, "大学物理", 10)
	if len(hits) != 2 {
		t.Fatalf("搜「大学物理」应该命中 2 门，实际 %d：%+v", len(hits), hits)
	}
	// AⅡ 应该排在「大学物理实验」前面（课名更短、更接近检索词）
	if hits[0].ID != "1001" {
		t.Errorf("排序不对，第一条是 %s（%s）", hits[0].ID, hits[0].Title)
	}
}

func TestY2CatalogSearchRomanAndTeacher(t *testing.T) {
	cat := y2CatFixtureParse(t)
	// 用户打 AII 也要能搜到 AⅡ
	if hits := y2CatalogSearch(cat, "大学物理AII", 10); len(hits) != 1 || hits[0].ID != "1001" {
		t.Errorf("搜「大学物理AII」应该命中 1001，实际 %+v", hits)
	}
	if hits := y2CatalogSearch(cat, "大学物理A2", 10); len(hits) != 1 || hits[0].ID != "1001" {
		t.Errorf("搜「大学物理A2」应该命中 1001，实际 %+v", hits)
	}
	if hits := y2CatalogSearch(cat, "朱星宝", 10); len(hits) != 1 || hits[0].ID != "1001" {
		t.Errorf("按老师搜应该命中 1001，实际 %+v", hits)
	}
	if hits := y2CatalogSearch(cat, "孙新 数值分析", 10); len(hits) != 1 || hits[0].ID != "1003" {
		t.Errorf("老师+课名应该命中 1003，实际 %+v", hits)
	}
	if hits := y2CatalogSearch(cat, "量子力学", 10); len(hits) != 0 {
		t.Errorf("不存在的课不该命中，实际 %+v", hits)
	}
}
