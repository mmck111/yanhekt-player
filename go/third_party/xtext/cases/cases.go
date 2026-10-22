// Package cases 是 golang.org/x/text/cases 的最小替身（见模块根目录说明）。
//
// 只实现 goja 用到的 Lower / Upper / Caser.Transform，行为等同 strings 包。
package cases

import (
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/transform"
)

// Caser 大小写转换器（goja 只用到 Transform / Reset / String）
type Caser interface {
	String(string) string
	Reset()
	Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error)
}

type caser struct {
	upper bool
}

// Lower 小写转换器
func Lower(t language.Tag) Caser { return &caser{} }

// Upper 大写转换器
func Upper(t language.Tag) Caser { return &caser{upper: true} }

// Reset 无状态，什么都不用清
func (c *caser) Reset() {}

// String 转换一个字符串
func (c *caser) String(s string) string {
	if c.upper {
		return strings.ToUpper(s)
	}
	return strings.ToLower(s)
}

// Transform 转换一段字节（目标缓冲区不够时返回 ErrShortDst，让调用方扩容）
func (c *caser) Transform(dst, src []byte, atEOF bool) (int, int, error) {
	out := c.String(string(src))
	if len(out) > len(dst) {
		return 0, 0, transform.ErrShortDst
	}
	copy(dst, out)
	return len(out), len(src), nil
}
