package main

import "testing"

func TestOldCodeFromNumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"20212022110024520305", "100245203"},
		{"20212022110305320904", "103053209"},
		{"20211-24-2400186-1627541179636", ""},
		{"", ""},
		{"20212022110", ""},
	}
	for _, c := range cases {
		if got := oldCodeFromNumber(c.in); got != c.want {
			t.Fatalf("oldCodeFromNumber(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestOldTermFromNumber(t *testing.T) {
	year, sem := oldTermFromNumber("20212022110024520305")
	if year != "2021-2022" || sem != 1 {
		t.Fatalf("got %q/%d", year, sem)
	}
	year, sem = oldTermFromNumber("20222023210000000001")
	if year != "2022-2023" || sem != 2 {
		t.Fatalf("got %q/%d", year, sem)
	}
	if y, s := oldTermFromNumber("20211-24-2400186"); y != "" || s != 0 {
		t.Fatalf("短编号不该解析出学期，got %q/%d", y, s)
	}
}

func TestOldIsExpired(t *testing.T) {
	if !oldIsExpired(map[string]any{"code": float64(12121028), "message": "课程已过期"}) {
		t.Fatal("数字 code 应判为过期")
	}
	if !oldIsExpired(map[string]any{"code": "12121028"}) {
		t.Fatal("字符串 code 应判为过期")
	}
	if oldIsExpired(map[string]any{"code": float64(0)}) {
		t.Fatal("正常返回不该判为过期")
	}
}
