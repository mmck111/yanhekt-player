// 课程名/教师名的"像不像"比较：本地做归一化 + 包含匹配
//
// 上游的课程名写法很不统一：同一个课在不同学期可能写成
//
//	大学物理AⅡ ／ 大学物理AII ／ 大学物理A2 ／ 大学物理 A II
//	线性代数（理工）／ 线性代数(理工)
//
// 直接 strings.Contains 会因为「Ⅱ / II / 2」「全角括号 / 半角括号」「空格」对不上，
// 所以匹配前先把两边都折成同一个形状：小写、去掉空白与常见分隔符、全角转半角、
// 罗马数字（Ⅰ Ⅱ Ⅲ… 和结尾的 I/II/III/IV…）统一成阿拉伯数字。
package main

import (
	"strings"
	"unicode"
)

// y2RomanFold 罗马数字字符 → 阿拉伯数字（Unicode 罗马数字区）
var y2RomanFold = strings.NewReplacer(
	"Ⅰ", "1", "Ⅱ", "2", "Ⅲ", "3", "Ⅳ", "4", "Ⅴ", "5",
	"Ⅵ", "6", "Ⅶ", "7", "Ⅷ", "8", "Ⅸ", "9", "Ⅹ", "10",
	"Ⅺ", "11", "Ⅻ", "12",
	"ⅰ", "1", "ⅱ", "2", "ⅲ", "3", "ⅳ", "4", "ⅴ", "5",
	"ⅵ", "6", "ⅶ", "7", "ⅷ", "8", "ⅸ", "9", "ⅹ", "10",
)

// y2RomanTail ASCII 结尾的罗马数字（AII / AIII / AIV / CVI …），转成数字
var y2RomanTail = map[string]string{
	"i": "1", "ii": "2", "iii": "3", "iv": "4", "v": "5",
	"vi": "6", "vii": "7", "viii": "8", "ix": "9", "x": "10",
}

// y2NormText 归一化：用于比较，不用于显示
func y2NormText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = y2RomanFold.Replace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E: // 全角 ASCII
			r -= 0xFEE0
		case r == 0x3000:
			r = ' '
		}
		if unicode.IsSpace(r) {
			continue
		}
		switch r { // 这些符号在不同课名里随便加，比较时一律忽略
		case '·', '-', '_', '(', ')', '（', '）', '\u2018', '\u2019', '\u201c', '\u201d', '\u3002':
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	// 把结尾的 i/ii/iii/iv… 折成数字（大学物理aii → 大学物理a2）
	if i := trailingRomanStart(out); i >= 0 {
		if n, ok := y2RomanTail[out[i:]]; ok && i > 0 {
			prev := rune(out[i-1])
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
				out = out[:i] + n
			}
		}
	}
	return out
}

// trailingRomanStart 结尾连续 [ivx] 的起点，没有就 -1
func trailingRomanStart(s string) int {
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if c != 'i' && c != 'v' && c != 'x' {
			break
		}
		i--
	}
	if i == len(s) || len(s)-i > 4 {
		return -1
	}
	return i
}

// y2FuzzyHas 归一化后 hay 是否包含 needle
func y2FuzzyHas(hay, needle string) bool {
	n := y2NormText(needle)
	if n == "" {
		return true
	}
	return strings.Contains(y2NormText(hay), n)
}

// y2FuzzyAll 多个词都要命中
func y2FuzzyAll(hay string, tokens []string) bool {
	nh := y2NormText(hay)
	for _, t := range tokens {
		nt := y2NormText(t)
		if nt == "" {
			continue
		}
		if !strings.Contains(nh, nt) {
			return false
		}
	}
	return true
}

// y2SameTitle 两门课的课名是否算同一个（归一化后相等）
func y2SameTitle(a, b string) bool {
	return y2NormText(a) == y2NormText(b)
}
