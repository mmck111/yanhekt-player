// Package language 是 golang.org/x/text/language 的最小替身（见模块根目录说明）。
//
// goja 只用到 language.Und 这个"未指定语言"的标签；真正的语言匹配表
// 有 1 MB 多，对签名脚本毫无用处。
package language

// Tag 语言标签（这里只需要一个占位）
type Tag struct {
	lang string
}

// Und 未指定语言
var Und = Tag{lang: "und"}

// Make 由字符串造一个标签（不解析任何子标签，够用即可）
func Make(s string) Tag { return Tag{lang: s} }

// String 返回标签字符串
func (t Tag) String() string { return t.lang }
