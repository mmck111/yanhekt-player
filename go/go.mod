module yanhekt

go 1.27

require github.com/dop251/goja v0.0.0-20261021140635-64adc8bb4d51

require (
	github.com/dlclark/regexp2/v2 v2.8.1 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20230207041349-798e818bf904 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// golang.org/x/text 换成 third_party/xtext 里的最小实现：
// goja 为支持 localeCompare / normalize / toLowerCase 拖进来 1.7 MB 的
// 语言数据表，签名脚本用不到。说明见 third_party/xtext/go.mod。
replace golang.org/x/text => ./third_party/xtext
