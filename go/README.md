# Go 版后端（开发笔记）

单文件、零依赖的本地服务：页面与课程库用 `go:embed` 打进二进制，只监听 `127.0.0.1`。
1.5.0 起仓库里只剩这一条实现（早期的 Node 参考实现 `server.js` / `yanhe2.js` 已移除）。

> 下文提到「Node 版」「两个实现对拍」的部分是移植期的历史记录，仅作存档：
> 相关脚本和被测实现都已在 1.5.0 移除。

## 构建

```powershell
powershell -ExecutionPolicy Bypass -File go\build.ps1
```

产出：`dist\yanhekt.exe`（windows/amd64）。

构建脚本会先把 `public/` 同步到 `go/web/`（`go:embed` 不能引用上级目录），
再 `go build -trimpath -tags "nethttpomithttp2" -ldflags "-s -w"`。

> 注意 1：本机默认 `TEMP` 不可写，Go 需要可写的临时目录，脚本里已把
> `TMP/TEMP/GOCACHE/GOPATH` 指到 `tools\` 下。
>
> 注意 2：`build.ps1` **必须存成带 BOM 的 UTF-8**。Windows PowerShell 5.1
> 用 ANSI 代码页读无 BOM 的脚本，中文注释会把解析搞乱（`$PSScriptRoot` 变空）。
>
> 注意 3：`-tags nethttpomithttp2` 去掉 Go 内置的 HTTP/2 客户端代码（省 0.6 MB）。
> 上游一律走 HTTP/1.1（Transport 里 `ForceAttemptHTTP2=false`），没有行为差异。

## 体积是怎么压到 15 MB 以内的

goja（JS 引擎）本身就占 ~9 MB，为了压到 15 MB 以内做了两件事：

1. `go/third_party/xtext/`：`golang.org/x/text` 的最小替身，`go.mod` 里用
   `replace` 指过去。goja 为支持 `localeCompare` / `normalize` / `toLowerCase`
   拖进来 1.7 MB 的语言数据表，签名脚本用不到。
   影响：`localeCompare` 按码点比较、`normalize` 原样返回、
   `toLowerCase/UpperCase` 用 `strings` 包（ASCII / 中文结果一致）。
2. `-tags nethttpomithttp2`：见上。

不想要这两处改动的话，删掉 `replace` 和 `-tags` 即可，代价是 exe 变回 17.1 MB。

## 源码结构

```
go/
├─ main.go        启动、路由、页面内嵌、横幅、单实例检测、剪贴板
├─ api.go         上游 cbiz 接口（用户 / 课程 / 讲次 / 视频地址）+ JSON 取值辅助
├─ catalog.go     内置课程库（catalog.json.gz）+ 学期解析 + 本地检索 + 老平台检索
├─ course.go      课程详情 buildCourse / getCourse、讲次探测 probeOldCourses
├─ search.go      /api/db/search：线上检索 + 课程库兜底 + 讲次探查
├─ proxy.go       /p 录播代理（全局串行 + 分片 LRU 缓存）、CDN 拉流
├─ download.go    /dl 分片拼单文件、播放列表解析
├─ live.go        /api/live 直播列表 + /lp 直播代理（独立 4 并发连接池）
├─ signer.go      goja 跑官网签名 JS（自愈 + 磁盘缓存 + 兜底）
├─ browser.go     从 Edge/Chrome leveldb 里找旧平台 token / 新版 JWT（自动登录）
├─ selftest.go    /api/selftest 自检
├─ stubs.go       tokenOf / verifyToken / signerStatus
├─ platform_*.go  Windows 的控制台 UTF-8、clip.exe 复制；其它平台空实现
├─ y2.go          新版平台基础层：鉴权写法、请求头、URL 拼装、JWT 提取、/play/ 签名
├─ y2_api.go      新版平台接口：学期 / 我的课程 / 课表 / 讲次 / 检索 / 详情
├─ y2_scan.go     新版平台课表扫描 + 学期时间窗 / 日期区间
├─ y2_orch.go     新版平台编排：身份缓存、讲次缓存、录像判定、课程匹配
├─ y2_route.go    新版平台路由：/api/y2/*、/y2play、/y2dl、/y2stream、/diag
├─ y2_test.go     纯函数交叉验证（期望值来自 Node 版真跑的结果）
├─ y2_fixture_test.go  解析层 fixture 对拍（go/testdata/y2-fixtures.json）
├─ third_party/xtext/   golang.org/x/text 的最小替身（压体积用，见上）
└─ web/           构建时从 ../public 同步，勿手改
```

## 和 Node 版对齐时踩到的坑

- **签名模块要 `navigator`**：官网 `p()` 会读 `navigator.userAgent` 决定往
  地址里插哪段 hash。Node 18+ 自带 `navigator`，goja 没有 —— 不给它补一个
  桌面 Chrome 的 UA，`p()` 会抛 `ReferenceError`，签出来的地址被 CDN 403。
- **字节串编码**：leveldb 扫描按"一字一字节"处理（等价 Node 的
  `toString('latin1')`），用 Go 字符串索引即可。
- **base64url**：`enc/dec` 用 `base64.RawURLEncoding`，和 Node 的
  `toString('base64url')` 一致（无填充）。
- **encodeURIComponent**：文件名里的中文要用 JS 那套转义（空格是 `%20`），
  不能直接用 `url.QueryEscape`。
- **Go 正则没有 lookahead**：`#EXT-X-KEY:...METHOD=(?!NONE)` 改成逐行判断。

## 验收

```powershell
# 起 Go 版
$env:PORT="8899"; .\dist\yanhekt.exe

# 另开一个窗口，用现成的冒烟测试直接打它
$env:SMOKE_URL="http://127.0.0.1:8899"
node test\smoke.js

# 新版平台：纯函数 + 解析层交叉验证（不需要真 JWT）
node tools\parity-y2-pure.js        # 期望值来源（extractJwt / signPath / apiUrl / termWindow …）
node tools\parity-y2-fixtures.js    # 重新生成 go\testdata\y2-fixtures.json
cd go; go test ./... -run TestY2 -v # 和上面两份 Node 结果逐字段比

# 新版平台：匿名 / 错误路径对拍（两个实现都起起来）
node tools\parity-y2.js http://127.0.0.1:8898 http://127.0.0.1:8899
```

`test/smoke.js` 支持 `SMOKE_URL` 后就不再自己起 Node 服务，可以直接对拍两个实现。

## 当前支持范围

| 能力 | Go 版 |
|---|---|
| 页面（`/`、`/old`、`/y2`、`/hls.min.js`） | ✅ 与 Node 版同一份文件 |
| 老平台登录 / 自动登录 / 退出 | ✅ |
| 课程搜索（线上 + 课程库兜底 + 学期/教师 + 讲次探查排序） | ✅ |
| 课程详情 / 讲次 / 播放地址 | ✅ |
| 录播播放代理 `/p`（串行 + 分片缓存） | ✅ |
| 下载 `/dl`（分片拼接、重试、文件名） | ✅ |
| 直播列表 `/api/live` + 直播代理 `/lp` | ✅ |
| 自检 `/api/selftest` | ✅ |
| **新版平台 y2**（`/api/y2/*`、`/y2play`、`/y2dl`、`/y2stream`、`/diag`） | ✅ |

## 新版平台（y2）移植说明

一份 `yanhe2.js` + `server.js` 的 y2 段落，拆到 5 个文件里，逐函数对应：

| Node | Go |
|---|---|
| `tokenCookie` / `req` / `apiUrl` / `getJson` / `extractJwt` / `login` / `profile` / `signPath` / `signedUrl` / `tsOf` / `dateOf` | `y2.go` |
| `probeVariants` / `termList` / `myCourses` / `weekSchedule` / `scheduleMap` / `subInfo` / `searchCourses` / `courseDetail` / `courseVideos` / `filterRecordings` / `kindOfSub` | `y2_api.go` |
| `rowOf` / `dayRows` / `ymdShift` / `scanDays` / `scanDates` / `scanByKeyword` / `findCourses` / `scanSessions` / `termWindow` / `datesBetween` | `y2_scan.go` |
| `y2Who` / `y2Videos` / `y2Verify` / `windowsFor` / `y2ScanWindows` / `probeStream` / `pickPlayable` / `pool` / `y2Match` / `y2MatchByTeacher` / `y2Rescue` | `y2_orch.go` |
| `/api/y2/*`、`/y2play`、`/y2dl`、`/y2stream`、`/diag` 路由 + `y2ProxyPlay` | `y2_route.go` |

几个容易踩的点（都按 Node 版逐字对齐）：

- **参数顺序**：Node 的 `URLSearchParams` 按插入顺序拼串，Go 的 `map` 无序 ——
  所以 `apiUrl` 用的是有序的 `y2Params`，转义也照 URLSearchParams 那套
  （空格是 `+`，只有 `[A-Za-z0-9*-._]` 原样，`~!()'` 全转义）。
- **`code` 判定**：`searchCourses` 用 `code !== 0 && !== 200 && !== 1000`（字符串 `"0"` 也算失败），
  `get-sub-info` 用 `code === 0`，两处严格程度不同，分别对应 `y2CodeOK` / `codeIsZeroStrict`。
- **JS 真值**：`a || b` 里空数组、空对象都算真（`y2Truthy`），和 `api.go` 里的 `truthy` 不同。
- **鉴权写法轮询**：`off → plain → serialized → raw`，哪种过就记住（成功后不还原），
  失败则还原成原来那种；`raw` 只在粘进来的 cookie 里有 `=` 或 `;` 时才试。
- **`/会话被拒绝/` 那些分支**：`y2GetJSON` 的错误文案里从来没有这个词，所以
  Node 版里"遇到鉴权失败就直接抛"的几处是死分支；为了行为一致照样保留（`y2IsAuthRejected`）。
- **`y2Match` / `y2MatchByTeacher` / `y2Rescue`**：当前 `server.js` 里没有任何路由调用它们
  （老平台搜索那一段已经不"问新版"了）。仍然按 Node 逐行移植并标注了死代码，
  将来要接上直接调用即可。
- **`y2Session` 的 JSON 标签**：按"讲次直读"那套字段写（`/diag` 的抽检会把讲次原样吐出来），
  扫课表多出来的 `subTitle/period/courseId/endAt/drag` 用 `omitempty`。

## 验收记录（2026-10-22，第二批：y2 移植）

- `go vet ./...` 干净，`go build` / `go build.ps1` 通过
- `dist\yanhekt.exe` **15.08 MB**（y2 这部分只多了约 0.23 MB）
- `SMOKE_URL=http://127.0.0.1:8899 node test\smoke.js` → **17 通过 / 0 失败**
- `node tools\parity-y2.js http://127.0.0.1:8898 http://127.0.0.1:8899` → **13 通过 / 0 不一致**
  （`/api/y2/status` 形状、`/api/y2/terms|courses|search|find|sessions|sub` 的 401、
  `POST /api/y2/login` 的假 JWT/空串/编码 cookie 三种 200/400、`/y2dl` 未登录 401、
  `/y2play` 缺参数与未登录）
- 额外对拍（带 `y2tok` 假 cookie + `/api/y2/diag` + `/diag`）→ **13 通过 / 0 不一致**
  （时间戳归一后 `/diag` 的 HTML 逐字节一致）
- `go test ./... -run TestY2 -v` → **14 个测试全过**，其中：
  - 纯函数：`extractJwt` 12 例、`signPath` 9 例、`signedUrl` 5 例（时间戳钉死，逐字符一致）、
    `apiUrl` 7 例（数组参数 / 空值过滤 / 特殊字符）、`termWindow` 13 例、`datesBetween` 6 例、
    `tsOf` 11 例、`dateOf` 5 例、`kindOfSub` / 状态判定 / `filterRecordings` / `formEncode`
  - 解析层：18 个 fixture（`go\testdata\y2-fixtures.json`，由 `tools\parity-y2-fixtures.js`
    用 Node 版真跑生成）—— profile / 学期列表（含学年分组）/ 课程检索（含 `code` 失败、
    `sessions: null`）/ 课程详情（`sub_list`、`origin_sub_list`、讲次藏在 JSON 字符串里）/
    取流（同类型去重、m3u8/mp4/aac、画面排序）/ 周课表 / 按天课表 / 我的课程
- `node tools\parity.js ...` → **40 通过 / 1 失败**，失败项是"两版都有直播且交集占多数"：
  跑的时候两版都返回 0 场直播（线上当时没有直播），与本次改动无关。

## 没能实测的部分（环境里拿不到真 JWT）

这台机器浏览器里只有老平台的 token，`/api/y2/login/auto` 扫到的候选是 0 个，
所以**登录成功之后**的链路没有端到端跑过。下面这些是"代码审查 + fixture 对拍"覆盖，
不是实测：

| 分支 | 验证方式 |
|---|---|
| 四种鉴权写法轮询、哪种过就记住 | 代码审查（`y2Login`），失败路径已实测（假 JWT 返回 200/ok:false，两版文案一致） |
| `profile` → 身份（tenant 回落 21、phone trim） | fixture 对拍 |
| 学期列表三种接口写法、学年分组拍平、1 小时缓存 | fixture 对拍 + 代码审查 |
| 检索 4 种参数写法轮询、`code` 判定 | fixture 对拍（含失败分支） |
| 讲次直读三种变体、`deepSubArray` 递归找讲次 | fixture 对拍（含 JSON 字符串里嵌的） |
| 取流解析（画面去重/排序、m3u8/mp4/aac） | fixture 对拍 |
| 扫课表 30/7 天分段、`maxEmpty` 提前收工、并发 | 代码审查（只跑过 `datesBetween` 这类纯函数） |
| `y2Verify` 的录像判定与"全藏掉就全显示"保险 | 代码审查（依赖上面的扫描链路） |
| `/y2play` 真实 MP4 转发 + Range、`/y2dl` 附件下载 | 代码审查（签名与 Node 逐字符一致，但没有真 token 打不开流） |
| `/diag` 登录后的自检步骤 | 代码审查（未登录的空报告已和 Node 逐字节对拍） |

## 第三批（1.5.0）：按审计报告逐条对齐

两份只读审计（HTTP 路由契约 / 业务逻辑）逐条比对后修掉的差异：

| 项目 | 之前 | 现在 |
|---|---|---|
| 讲次时间戳 | Go 把 `start_at` 日期串解析成时间戳，Node 只认数字（全是 0） | 两边都走 `tsOf`，排序一致 |
| 课程分页请求失败 | 静默 `break`，返回半份讲次表还缓存 60 秒 | 和 Node 一样整体失败（`/api/open` 500），且不写缓存 |
| `/p` 串行粒度 | 全进程一把锁 | 按 CDN 域名各一把（等价 Node 的 per-host `maxSockets=1`） |
| `/p` 播放列表 base | URL 里没有 `/` 时 panic | 有保护，按空 base 处理 |
| base64 参数 | 只认 URL-safe 字母表 | 标准字母表也认（Node 的 Buffer 两种都收） |
| 直播列表顺序 | map 遍历，同 rank 时顺序随机 | 记住首次出现顺序（Node 的 Map 就是插入序） |
| 直播 m3u8 相对地址 | 字符串拼接 | `url.ResolveReference`（等价 `new URL(t, u)`，能处理 `../`） |
| `/api/logout` | 少了 `Max-Age=0` | 明确输出 `Max-Age=0` |
| JSON 字段 | `user.badge/nickname`、`selftest.live.old.*`、课程空串字段被 `omitempty` 吞掉 | 和 Node 一样始终输出 |
| 课程失败对象 | 多一个 `"code":null` | `code` 只在真有值时出现 |
| 搜索 `source` 标记 | 无条件写 `api+teacher` | 真的补到教师结果才写 |
| 搜索排序 | 只按录像数排 | 先按课名相似度、再按录像数（和 Node 一致） |
| 学期表 | 累加所有 `semesters` 组 | 只认第一个（对齐 `.find`） |
| `searchRaw` panic | 静默吞掉 | 补一条 `服务端检索失败：…` note |

另外 y2 的"这门课有几讲有录像"改成**逐讲取流验证**（`y2SessionHasStream` / `y2VerifySessions`）：
平台给的 `sub_status=6` 不可靠（常见 5 讲全是 6、实际只有 1 讲有流），所以不再拿状态码当结论。
讲次 `> 40` 的课只抽最新 5 讲验，卡片上只标「有录像」不写数字。

本轮验收：`go test ./...` 通过；冒烟 17/17（直接打 `dist\yanhekt.exe`）；
`tools/parity.js` 41/41；`tools/parity-y2.js` 13/13；`tools/parity-y2-pure.js` 与 fixtures 全绿。

### 之后补的两处

- **y2 讲次合并**：`y2CourseVideos` 的候选讲次不再只看 `sub_status=6`（已结束 5 / 未知也算），
  `y2Videos` 在讲次直读之后用 `y2MergeKnownSessions` 把课表里已知的讲次并进来再验流 ——
  修"卡片写 5 讲、点进去只有 1 讲"。
- **搜索卡片候选**：`y2Verify` / `pickPlayable` 同样把 5 / 未知状态算进候选。

## 已知差异（都是有意为之，不是漏掉）

1. `x/text` 最小替身 + `nethttpomithttp2`：只为压体积，见上文。
2. 超时语义：Node 是 socket 空闲 20s；Go 是"等首字节 20s + 拨号 10s"，没有整请求超时
   （大分片慢速下载不会被误杀）。y2 的 JSON 接口是 25s 整请求超时，`/play/` 是等首字节 60s。
3. 重定向：两边都是最多 3 跳，超过时 Node 把 3xx 直接返回、Go 返回 504。
4. 少数用 map 拼的响应键顺序不同（JSON 语义一致）。
5. 版本号是 `1.5.0-go`，所以先跑 Node 再跑 Go 时单实例提示会说"两个版本不一样"。
6. y2 里有三处顺序依赖 Node 的"对象插入顺序"：`termList` 的学年分组拍平、`deepSubArray`
   在嵌套对象里找讲次数组、`/diag` 里 `Object.keys` 的字段名清单。Go 的 `map` 无序，
   这几处改成按字段名排序 —— 结果是确定的，但和 Node 的遍历顺序不一定相同（只影响
   多候选时的取舍与显示顺序，不影响单一路径的正确性）。
7. y2 的超时/重试：Node 的 `req` 用 `setTimeout(25s)`（socket 空闲），Go 用
   `http.Client.Timeout=25s`（整请求）。JSON 接口都是几 KB 的响应，实际无差异。
