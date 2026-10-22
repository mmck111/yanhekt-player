// Package transform 是 golang.org/x/text/transform 的最小替身。
package transform

import "errors"

// ErrShortDst 目标缓冲区不够（和原包同名同语义）
var ErrShortDst = errors.New("transform: short destination buffer")
