// Package rangetable 是 golang.org/x/text/unicode/rangetable 的最小替身
// （见模块根目录说明）。
//
// goja 用 New / Merge 造"能不能忽略大小写"的字符表，这里产出标准库的
// *unicode.RangeTable，并且保证区间有序、不重叠（unicode.Is 用二分查找）。
package rangetable

import (
	"sort"
	"unicode"
)

type span struct {
	lo, hi rune
}

// New 把若干码点做成一个 RangeTable
func New(r ...rune) *unicode.RangeTable {
	if len(r) == 0 {
		return &unicode.RangeTable{}
	}
	spans := make([]span, 0, len(r))
	for _, c := range r {
		spans = append(spans, span{c, c})
	}
	return build(mergeSpans(spans))
}

// Merge 合并若干 RangeTable
//
// 注意：原表里 stride != 1 的区间会被当作"连续区间"，也就是可能多包含
// 几个字符。goja 只用它判断大小写可忽略字符，多包含不影响签名。
func Merge(t ...*unicode.RangeTable) *unicode.RangeTable {
	var spans []span
	for _, tab := range t {
		if tab == nil {
			continue
		}
		for _, r := range tab.R16 {
			spans = append(spans, span{rune(r.Lo), rune(r.Hi)})
		}
		for _, r := range tab.R32 {
			spans = append(spans, span{rune(r.Lo), rune(r.Hi)})
		}
	}
	if len(spans) == 0 {
		return &unicode.RangeTable{}
	}
	return build(mergeSpans(spans))
}

func mergeSpans(spans []span) []span {
	sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
	out := spans[:0]
	for _, s := range spans {
		if len(out) == 0 || s.lo > out[len(out)-1].hi+1 {
			out = append(out, s)
			continue
		}
		if s.hi > out[len(out)-1].hi {
			out[len(out)-1].hi = s.hi
		}
	}
	return out
}

func build(spans []span) *unicode.RangeTable {
	tab := &unicode.RangeTable{}
	for _, s := range spans {
		if s.lo <= 0xFFFF {
			hi := s.hi
			if hi > 0xFFFF {
				hi = 0xFFFF
			}
			tab.R16 = append(tab.R16, unicode.Range16{Lo: uint16(s.lo), Hi: uint16(hi), Stride: 1})
			if s.hi > 0xFFFF {
				tab.R32 = append(tab.R32, unicode.Range32{Lo: 0x10000, Hi: uint32(s.hi), Stride: 1})
			}
			continue
		}
		tab.R32 = append(tab.R32, unicode.Range32{Lo: uint32(s.lo), Hi: uint32(s.hi), Stride: 1})
	}
	return tab
}
