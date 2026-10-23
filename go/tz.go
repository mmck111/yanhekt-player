// 时区：延河给的时间全是"北京时间墙钟串"（2026-10-22 18:30:00，不带时区）
//
// 以前按 time.Local 解析，电脑上（Windows/中国时区）没问题，但在手机上会翻车：
// Android 没有 /etc/localtime，Go 的 time.Local 退化成 UTC，整批时间偏 8 小时 ——
// 正在直播的课被判成"未开始"，几小时后才开的课又被 6 小时窗口过滤掉，
// 表现就是"电脑上有直播、手机上一场都不显示"。
//
// 所以这里固定按 +08:00 解析，跟设备在哪个时区、有没有 tzdata 都无关。
package main

import "time"

// bj 北京时间（中国无夏令时，固定偏移足够）
var bj = time.FixedZone("CST", 8*3600)

// nowMillis 当前毫秒时间戳（绝对时间，与时区无关，用于和解析出来的时间比较）
func nowMillis() int64 { return time.Now().UnixNano() / int64(time.Millisecond) }
