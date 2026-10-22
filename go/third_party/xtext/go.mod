module golang.org/x/text

go 1.27

// 这是 golang.org/x/text 的最小替身，只为缩小可执行文件体积。
//
// goja（JS 引擎）为支持 String.prototype.localeCompare / normalize /
// toLowerCase 依赖 x/text，而 x/text 的排序表、大小写表、语言匹配表
// 加起来 1.7 MB；签名脚本只做 md5 + 十六进制 + 时间戳，用不到这些。
// 官方包没有精简构建开关，所以 go.mod 里用 replace 指到这里。
//
// 和真实 x/text 的差异：
//   - localeCompare 按码点比较，不做语言相关排序（ASCII / 中文结果一致）
//   - normalize 原样返回（NFC/NFD/NFKC/NFKD 都不归一化）
//   - toLowerCase / toUpperCase 用 strings.ToLower/ToUpper（和 Unicode 默认
//     映射一致，只差希腊语词尾 sigma 这类极少见的情况）
//
// 这些差异只有在"官网签名脚本恰好用到上述 API"时才可能碰到。
