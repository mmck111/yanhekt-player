// Package norm 是 golang.org/x/text/unicode/norm 的最小替身
// （见模块根目录说明）。
//
// goja 只用到 Form.String；这里原样返回，不做归一化。
package norm

// Form 归一化形式
type Form int

// 四种归一化形式（替身里都是"原样返回"）
var (
	NFC  = Form(0)
	NFD  = Form(1)
	NFKC = Form(2)
	NFKD = Form(3)
)

// String 返回原串（不做归一化）
func (f Form) String(s string) string { return s }

// Bytes 返回原字节（不做归一化）
func (f Form) Bytes(b []byte) []byte { return b }

// IsNormalString 永远认为已经归一化
func (f Form) IsNormalString(s string) bool { return true }
