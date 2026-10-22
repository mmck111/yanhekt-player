// Package collate 是 golang.org/x/text/collate 的最小替身（见模块根目录说明）。
//
// 只实现 goja 用到的 CompareString：按 Unicode 码点比较。
// 真正的语言相关排序表 1 MB 多，签名脚本用不到。
package collate

import (
	"strings"

	"golang.org/x/text/language"
)

// Option 排序选项
type Option int

// IgnoreCase 忽略大小写
const IgnoreCase Option = 0

// Collator 比较器
type Collator struct {
	ignoreCase bool
}

// New 造一个比较器
func New(t language.Tag, opts ...Option) *Collator {
	c := &Collator{}
	for _, o := range opts {
		if o == IgnoreCase {
			c.ignoreCase = true
		}
	}
	return c
}

// Compare 按码点比较两个字符串（负数 a<b，0 相等，正数 a>b）
func (c *Collator) Compare(a, b string) int {
	if c.ignoreCase {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	}
	return strings.Compare(a, b)
}

// CompareString 同 Compare
func (c *Collator) CompareString(a, b string) int { return c.Compare(a, b) }
