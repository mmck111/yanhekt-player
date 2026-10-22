/**
 * y2 纯函数交叉验证：用 Node 版 yanhe2.js 算出期望值，输出 JSON。
 *
 *   node tools\parity-y2-pure.js
 *
 * 输出里的每一项都和 go\y2_test.go 里的常量一一对应（那份常量就是用本脚本的结果
 * 抄过去的）。改 yanhe2.js 的纯函数之后，重跑本脚本、更新 go\y2_test.go 即可。
 */
const path = require('path');
const y2 = require(path.join(__dirname, '..', 'yanhe2.js'));

const JWT = 'eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.k3Jh-dEf_9xQ';

const out = {};

// ---------------- extractJwt ----------------
out.extractJwt = [
  ['裸 JWT', JWT],
  ['整段 cookie', '_token=' + JWT + '; locale=zh_cn'],
  ['URL 编码的 cookie', '_token%3D' + encodeURIComponent(JWT) + '%3B%20locale%3Dzh_cn'],
  ['PHP 序列化', 'a:2:{i:0;s:6:"_token";i:1;s:' + JWT.length + ':"' + JWT + '";}'],
  ['URL 编码的 PHP 序列化', encodeURIComponent('a:2:{i:0;s:6:"_token";i:1;s:' + JWT.length + ':"' + JWT + '";}')],
  ['带双引号', '"' + JWT + '"'],
  ['带单引号+空格', "  '" + JWT + "'  "],
  ['空串', ''],
  ['空白串', '   '],
  ['只有 _token= 没有 JWT 形状', '_token=abc.def.ghi'],
  ['认不出的整段', 'hello world'],
  ['eyJ 开头但不是完整 JWT', 'eyJhbGciOiJIUzI1NiJ9'],
].map(([name, input]) => ({ name, input, expect: y2.extractJwt(input) }));

// ---------------- signPath ----------------
const ids = [
  { name: '常规', id: { userId: 12345, tenantId: 21, phone: '13800138000' } },
  { name: '没手机号', id: { userId: 7, tenantId: 21, phone: '' } },
  { name: '零散字段', id: { userId: 1, tenantId: 0, phone: '1' } },
];
out.signPath = [];
for (const { name, id } of ids) {
  for (const [pathname, ts] of [['/play/abc.mp4', 1700000000], ['/play/x/y/z.mp4', 1700000123], ['/live/a.m3u8', 1]]) {
    out.signPath.push({ name, pathname, ...id, ts, expect: y2.signPath(pathname, id, ts) });
  }
}

// ---------------- apiUrl ----------------
out.apiUrl = [
  ['无参数', y2.apiUrl('/vlabpassportapi/v1/course/term-list', undefined)],
  ['空参数对象', y2.apiUrl('/courseapi/v3/multi-search/get-course-list', {})],
  ['数组参数 semesters[]', y2.apiUrl('/v2/course/list', { semesters: [1, 2], page: 1 })],
  ['空数组', y2.apiUrl('/courseapi/v3/multi-search/get-course-list', { title: '数值分析', struc_ids: [] })],
  ['空值过滤', y2.apiUrl('/courseapi/v3/multi-search/get-course-list', {
    term: '', page: 1, per_page: 20, tenant: 21, title: '数值 分析', order: '', publish: '', nil: null,
  })],
  ['数组里有空串', y2.apiUrl('/v2/course/list', { semesters: [1, '', null, 2] })],
  ['中文与符号', y2.apiUrl('/x', { k: 'a~!*()\'b', t: '2024-2025 第一学期' })],
].map(([name, expect]) => ({ name, expect }));

// ---------------- signedUrl（把 Date.now 钉死，才能和 Go 逐字符比） ----------------
const fixedId = { userId: 12345, tenantId: 21, phone: '13800138000' };
const realNow = Date.now;
Date.now = () => 1700000000 * 1000;
out.signedUrl = [
  'https://aita.yanhekt.cn/play/abc.mp4',
  'https://aita.yanhekt.cn/play/abc.mp4?t=old&x=1',
  'https://aita.yanhekt.cn/play/a%2Fb.mp4?name=%E4%B8%AD%E6%96%87&t=old',
  'https://aita.yanhekt.cn/live/a.m3u8?sign=abc%2Bdef%3D&width=1280',
  'https://aita.yanhekt.cn/play/x.mp4?z=1&a=2&t=zzz&m=3',
].map(url => ({ name: url, input: url, ts: 1700000000, expect: y2.signedUrl(url, fixedId) }));
Date.now = realNow;

// ---------------- termWindow ----------------
const terms = [
  '2024-2025 第一学期',
  '2024-2025学年第二学期',
  '2024-2025-1',
  '2024-2025-2',
  '2024-2025 第三学期',
  '2023-2024',
  '',
  'abc',
  '2024—2025秋季学期',
  '2024至2025 春季',
  '2024–2025 第一学期',
  '2024~2025学年夏季学期',
  '2025-2026学年第一学期',
];
out.termWindow = terms.map(s => ({ name: s, expect: y2.termWindow(s) }));

// ---------------- datesBetween ----------------
out.datesBetween = [
  ['2024-09-01', '2024-09-05', 240],
  ['2024-02-27', '2024-03-02', 240],
  ['2024-09-05', '2024-09-01', 240],
  ['2024-01-01', '2024-12-31', 10],
  ['', '', 240],
  ['2023-12-30', '2024-01-02', 240],
].map(([from, to, cap]) => ({ from, to, cap, expect: y2.datesBetween(from, to, cap) }));

// ---------------- tsOf / dateOf ----------------
out.tsOf = [
  1700000000,
  1700000000000,
  '1700000000',
  '2024-09-01 14:30:00',
  '2024-09-01',
  '2024-09-01T14:30:00',
  '',
  'abc',
  -5,
  0,
].map(v => ({ name: String(v), input: v, expect: y2.tsOf(v) }));

out.dateOf = [
  1700000000,
  '2024-09-01 23:30:00',
  '2024-09-01',
  0,
  'abc',
].map(v => ({ name: String(v), expect: y2.dateOf(v) }));

// ---------------- kindOfSub ----------------
out.kindOfSub = ['course_live', 'ILive', 'olive', 'live', 'large_class', 'live_video', 'record', '', null, 'video']
  .map(v => ({ name: String(v), expect: y2.kindOfSub(v) }));

// ---------------- 状态判定 ----------------
out.statusFlags = ['0', '1', '2', '3', '5', '6', '', '9'].map(s => ({
  name: s,
  isRecording: y2.isRecordingStatus(s),
  maybeRecording: y2.maybeRecording(s),
  isLive: y2.isLiveStatus(s),
}));

// ---------------- filterRecordings ----------------
const rawSubs = [
  { subId: 'a', startAt: 100, status: '6' },
  { subId: 'b', startAt: 300, status: '5' },
  { subId: 'c', startAt: 200, status: '1' },
  { subId: 'd', startAt: 400, status: '' },
  { subId: 'e', startAt: 50, status: '3' },
  { subId: 'f', startAt: 500, status: '2' },
  { subId: 'g', startAt: 600, status: '9' },
];
out.filterRecordings = y2.filterRecordings(rawSubs).map(s => s.subId);

console.log(JSON.stringify(out, null, 2));
