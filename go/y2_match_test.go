package main

import "testing"

func TestY2NormTextFoldsRomanVariants(t *testing.T) {
	same := [][2]string{
		{"大学物理AⅡ", "大学物理AII"},
		{"大学物理AⅡ", "大学物理 A II"},
		{"大学物理AⅡ", "大学物理A2"},
		{"大学物理AⅢ", "大学物理AIII"},
		{"线性代数（理工）", "线性代数(理工)"},
		{"高等数学  A", "高等数学a"},
		{"物理Ⅳ", "物理4"},
	}
	for _, p := range same {
		if got, want := y2NormText(p[0]), y2NormText(p[1]); got != want {
			t.Errorf("y2NormText(%q)=%q, y2NormText(%q)=%q — 应该相等", p[0], got, p[1], want)
		}
	}
}

func TestY2FuzzyHas(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        bool
	}{
		{"大学物理AⅡ 朱星宝", "大学物理", true},
		{"大学物理AⅡ 朱星宝", "大学物理AII", true},
		{"大学物理AⅡ 朱星宝", "A", true},
		{"大学物理实验 张三", "大学物理A", false},
		{"数值分析", "孙新", false},
		{"数值分析 孙新", "孙新", true},
	}
	for _, c := range cases {
		if got := y2FuzzyHas(c.hay, c.needle); got != c.want {
			t.Errorf("y2FuzzyHas(%q, %q)=%v, want %v", c.hay, c.needle, got, c.want)
		}
	}
}
