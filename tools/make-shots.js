/**
 * 生成两张"界面示例"用的静态页（数据是假的，界面是真的），只用于 README 截图。
 * 用法：node tools/make-shots.js   → 输出到 %TEMP%\yhkt-shots\
 */
const fs = require('fs');
const os = require('os');
const path = require('path');

const root = path.join(__dirname, '..');
const out = path.join(os.tmpdir(), 'yhkt-shots');
fs.mkdirSync(out, { recursive: true });

const page = fs.readFileSync(path.join(root, 'public', 'index.html'), 'utf8');

const TERMS = ['2026-2027 第一学期', '2025-2026 第二学期', '2025-2026 第一学期'];
const COURSES = [
  { id: 30552, name: '数学分析II', term: '2025-2026 第二学期', teacher: '兰天', room: '文萃楼A305', videoCount: 48, verified: true },
  { id: 30561, name: '数学分析II', term: '2025-2026 第二学期', teacher: '刘正阳', room: '文萃楼B102', videoCount: 46, verified: true },
  { id: 30553, name: '数学分析II', term: '2025-2026 第二学期', teacher: '李军刚', room: '理教楼201', videoCount: 45, verified: true },
  { id: 30570, name: '数学分析II', term: '2025-2026 第二学期', teacher: '杨林', room: '8号楼1001', videoCount: 12, verified: true },
  { id: 30577, name: '数学分析II', term: '2025-2026 第二学期', teacher: '黄厚兵', room: '研楼506', videoCount: 3, verified: true },
  { id: 30579, name: '数学分析II', term: '2025-2026 第二学期', teacher: '于灏', verified: false, tag: '老平台无录像', note: '这门课平台上没有可播放的录像' }
];
const SESSIONS = [
  { id: 1, date: '2026-09-08', week: 1, title: '绪论 · 误差分析', duration: 5820, location: '文萃楼H130', main: 'x', vga: 'y' },
  { id: 2, date: '2026-09-15', week: 2, title: '插值法（一）', duration: 5640, location: '文萃楼H130', main: 'x', vga: 'y' },
  { id: 3, date: '2026-09-22', week: 3, title: '插值法（二）', duration: 5700, location: '文萃楼H130', main: 'x', vga: 'y' },
  { id: 4, date: '2026-09-29', week: 4, title: '函数逼近与曲线拟合', duration: 5580, location: '文萃楼H130', main: 'x', vga: 'y' },
  { id: 5, date: '2026-10-13', week: 6, title: '数值积分', duration: 5760, location: '文萃楼H130', main: 'x', vga: 'y' }
];

const inject = `
<script>
/* —— 截图用的假数据：不联网，界面代码本身是真的 —— */
window.Hls = { isSupported: () => true,
  Events: { MANIFEST_PARSED: 'mp', ERROR: 'er' }, ErrorTypes: { NETWORK_ERROR: 'net' },
  Hls: function(){ this.on = () => {}; this.loadSource = () => {}; this.attachMedia = () => {}; this.destroy = () => {}; } };
const __J = (d) => Promise.resolve(new Response(JSON.stringify(d), { headers: { 'content-type': 'application/json' } }));
const __COURSES = ${JSON.stringify(COURSES)};
const __SESSIONS = ${JSON.stringify(SESSIONS)};
const __TERMS = ${JSON.stringify(TERMS)};
const __f = window.fetch;
window.fetch = function (u, o) {
  const s = String(u);
  if (s.indexOf('/api/status') >= 0) return __J({ loggedIn: true, user: { nickname: '示例同学', badge: '2026' }, build: '1.4.0' });
  if (s.indexOf('/api/db/terms') >= 0) return __J({ ok: true, terms: __TERMS, count: 79564, meta: { count: 79564 } });
  if (s.indexOf('/api/db/search') >= 0) return __J({ ok: true, source: 'api', total: 24, courses: __COURSES, hidden: 12, probed: true, notes: [], truncated: false, cap: 120 });
  if (s.indexOf('/api/open') >= 0) {
    const c = __COURSES[0];
    return __J({ ok: true,
      course: { id: c.id, name: c.name, code: 'B073791', college: '数学与统计学院', year: '2026-2027', semester: 1, professors: ['孙新'] },
      sessions: __SESSIONS, stats: { total: __SESSIONS.length, playable: __SESSIONS.length, unresolved: 0 } });
  }
  return __f.apply(window, arguments);
};
</script>
`;

const shipped = page.replace('</head>', inject + '</head>');
fs.writeFileSync(path.join(out, 'search.html'), shipped);
fs.writeFileSync(path.join(out, 'course.html'), shipped);
console.log(out);
