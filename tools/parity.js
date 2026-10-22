/**
 * 对拍脚本：同时打 Node 版和 Go 版，逐项比较输出是否一致。
 *
 *   node tools/parity.js http://127.0.0.1:8841 http://127.0.0.1:8842
 *   node tools/parity.js                       # 默认 8898（Node）/ 8899（Go）
 *
 * 说明：test/crosscheck.ps1 里写的是 `node test\parity.js`；因为 test/ 目录
 * 约定不改动，这个文件放在了 tools/。想直接用 crosscheck.ps1 的话，
 * 把它复制成 test/parity.js 即可（脚本对参数格式没要求，两个位置都行）。
 *
 * 覆盖：页面/静态资源、登录、课程搜索（含学期过滤/教师名/翻页/课程库兜底）、
 * 大规模 probe 排序、课程详情、/p 播放列表改写与分片、/dl 结构、直播列表、
 * 直播代理、学期表、自检、签名来源。
 */
const http = require('http');

const A = process.argv[2] || process.env.NODE_BASE || 'http://127.0.0.1:8898'; // Node 版
const B = process.argv[3] || process.env.GO_BASE || 'http://127.0.0.1:8899';   // Go 版

function req(base, path, opts = {}) {
  return new Promise((resolve) => {
    const u = new URL(base + path);
    const r = http.request({
      host: u.hostname, port: u.port, path: u.pathname + u.search,
      method: opts.method || 'GET',
      headers: Object.assign({}, opts.headers || {}),
      timeout: opts.timeout || 300000,
    }, (res) => {
      const chunks = [];
      res.on('data', d => chunks.push(d));
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
    });
    r.on('error', e => resolve({ status: 0, headers: {}, body: Buffer.from(String(e.message)) }));
    r.on('timeout', () => { r.destroy(); resolve({ status: 0, headers: {}, body: Buffer.from('timeout') }); });
    if (opts.body) r.write(opts.body);
    r.end();
  });
}

const json = (r) => { try { return JSON.parse(r.body.toString('utf8')); } catch (e) { return null; } };
const jget = async (base, path, opts) => json(await req(base, path, opts));
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

let pass = 0, fail = 0;
const ok = (name, cond, detail) => {
  if (cond) { pass++; console.log('  ✓ ' + name + (detail ? '  → ' + detail : '')); }
  else { fail++; console.log('  ✗ ' + name + (detail ? '  → ' + detail : '')); }
};

async function login(base) {
  const r = await req(base, '/api/login/auto', { method: 'POST' });
  const j = json(r);
  const cookie = ((r.headers['set-cookie'] || [''])[0] || '').split(';')[0];
  return { j, cookie };
}

(async () => {
  console.log('Node 版：' + A + '\nGo 版  ：' + B);

  // ---------- 页面 ----------
  console.log('\n[0] 页面与静态资源');
  for (const p of ['/', '/old', '/index.html', '/y2', '/y2.html', '/hls.min.js']) {
    const ra = await req(A, p), rb = await req(B, p);
    ok(p + ' 状态码/长度一致', ra.status === rb.status && ra.body.length === rb.body.length,
      `${ra.status}/${rb.status}，${ra.body.length}B`);
  }

  // ---------- 1. 未登录搜索（字段必须完全一致）----------
  console.log('\n[1] /api/db/search 未登录');
  const q1 = '/api/db/search?q=' + encodeURIComponent('数值分析') + '&limit=3';
  const s1a = await jget(A, q1), s1b = await jget(B, q1);
  ok('JSON 完全一致', same(s1a, s1b), `Node ${(s1a.courses || []).length} 门 / Go ${(s1b.courses || []).length} 门，total ${s1a.total}/${s1b.total}`);

  // ---------- 2. 登录 ----------
  console.log('\n[2] 自动登录');
  const la = await login(A), lb = await login(B);
  ok('两版都登录成功', !!(la.j && la.j.ok && lb.j && lb.j.ok), `Node ${la.j && la.j.from} / Go ${lb.j && lb.j.from}`);
  ok('两版 cookie 都有', !!la.cookie && !!lb.cookie, lb.cookie);
  const ha = { headers: { cookie: la.cookie } }, hb = { headers: { cookie: lb.cookie } };

  // ---------- 3. probe ----------
  console.log('\n[3] /api/db/search?probe=1');
  const p3 = '/api/db/search?q=' + encodeURIComponent('数值分析') + '&limit=3&probe=1';
  const s3a = await jget(A, p3, ha), s3b = await jget(B, p3, hb);
  const fields = ['id', 'name', 'teacher', 'term', 'room', 'videoCount', 'totalSessions', 'verified', 'latestDate', 'tag', 'note'];
  const pick = (j) => (j.courses || []).map(c => fields.reduce((o, k) => { o[k] = c[k] === undefined ? null : c[k]; return o; }, {}));
  ok('每门课字段一致', same(pick(s3a), pick(s3b)), JSON.stringify(pick(s3b)));
  ok('hidden/probed/cap 一致', s3a.hidden === s3b.hidden && s3a.probed === s3b.probed && s3a.cap === s3b.cap);

  // ---------- 4. 大规模 probe（排序）----------
  console.log('\n[4] probe 60 门（并发探查 + 按录像数降序）');
  const p4 = '/api/db/search?q=' + encodeURIComponent('数学分析') + '&limit=60&probe=1';
  const s4a = await jget(A, p4, ha), s4b = await jget(B, p4, hb);
  const pick4 = (j) => (j.courses || []).map(c => [c.id, c.name, c.room, c.videoCount, c.verified, c.tag || null]);
  ok('门数/顺序/字段一致', same(pick4(s4a), pick4(s4b)), `${pick4(s4b).length} 门，前几门录像数 ${pick4(s4b).slice(0, 6).map(x => x[3]).join(',')}`);

  // ---------- 5. 学期过滤 / 教师名 / 翻页 / 课程库兜底 ----------
  console.log('\n[5] 搜索的各个分支');
  const branches = [
    ['学期过滤', '/api/db/search?q=' + encodeURIComponent('数学分析') + '&term=' + encodeURIComponent('2026-2027 第一学期') + '&limit=5'],
    ['教师名', '/api/db/search?q=' + encodeURIComponent('熊春光') + '&limit=5'],
    ['只给学期', '/api/db/search?term=' + encodeURIComponent('2026-2027 第一学期') + '&limit=3'],
    ['翻页', '/api/db/search?q=' + encodeURIComponent('数值分析') + '&limit=3&page=2'],
    ['limit 上限', '/api/db/search?q=' + encodeURIComponent('数值分析') + '&limit=999'],
    ['没结果', '/api/db/search?q=' + encodeURIComponent('这门课肯定不存在zzz') + '&limit=3'],
    ['课程库兜底', '/api/db/search?q=' + encodeURIComponent('大学物理') + '&term=' + encodeURIComponent('2025-2026 第二学期') + '&limit=5'],
  ];
  for (const [name, path] of branches) {
    const a = await jget(A, path, ha), b = await jget(B, path, hb);
    const pa = (a.courses || []).map(c => [c.id, c.name, c.teacher, c.term, c.room, c.source]);
    const pb = (b.courses || []).map(c => [c.id, c.name, c.teacher, c.term, c.room, c.source]);
    ok(`[${name}] 一致`, same(pa, pb) && a.total === b.total && a.source === b.source,
      `Node ${pa.length} 门(${a.source}) / Go ${pb.length} 门(${b.source})`);
  }

  // ---------- 6. 课程详情 ----------
  console.log('\n[6] /api/open?id=72120');
  const oa = await jget(A, '/api/open?id=72120', ha), ob = await jget(B, '/api/open?id=72120', hb);
  ok('课程名/讲次数一致', !!(oa.ok && ob.ok) && oa.course.name === ob.course.name && (oa.sessions || []).length === (ob.sessions || []).length,
    `${ob.course && ob.course.name}，${(ob.sessions || []).length} 讲`);
  const sessPick = (j) => (j.sessions || []).map(s => [s.id, s.date, s.title, s.week, s.main, s.vga, s.duration]);
  ok('每讲字段一致', same(sessPick(oa), sessPick(ob)));
  ok('stats 一致', same(oa.stats, ob.stats), JSON.stringify(ob.stats));
  ok('main 非空', (ob.sessions || []).every(s => !!s.main), ob.sessions && ob.sessions[0] && ob.sessions[0].main);

  // ---------- 7. /p 播放列表 ----------
  console.log('\n[7] /p 录播代理');
  const u = Buffer.from(ob.sessions[0].main, 'utf8').toString('base64url');
  const pa = await req(A, '/p?u=' + u, ha), pb = await req(B, '/p?u=' + u, hb);
  ok('都返回 200 + m3u8', pa.status === 200 && pb.status === 200 && /mpegurl/.test(pb.headers['content-type'] || ''),
    pb.headers['content-type']);
  const textA = pa.body.toString('utf8'), textB = pb.body.toString('utf8');
  ok('改写结果逐字一致', textA === textB, textB.split('\n').filter(l => l && !l.startsWith('#')).length + ' 个分片');
  const firstSeg = textB.split('\n').find(l => l && !l.startsWith('#'));
  const seg = firstSeg ? await req(B, firstSeg, { ...hb, timeout: 120000 }) : { status: 0, headers: {}, body: Buffer.alloc(0) };
  ok('分片能取到', seg.status === 200 && /video\/mp2t/.test(seg.headers['content-type'] || ''),
    `${seg.status} ${(seg.body.length / 1024).toFixed(0)} KB`);

  // ---------- 8. 下载结构 ----------
  console.log('\n[8] /dl?probe=1');
  const sess = ob.sessions[0];
  const dpA = await jget(A, `/dl?course=72120&session=${sess.id}&kind=main&probe=1`, ha);
  const dpB = await jget(B, `/dl?course=72120&session=${sess.id}&kind=main&probe=1`, hb);
  ok('片数/时长/加密一致', same(
    [dpA.segments, dpA.totalDurationSec, dpA.segmentDurationSec, dpA.encrypted, dpA.hasEndList, dpA.initSegment],
    [dpB.segments, dpB.totalDurationSec, dpB.segmentDurationSec, dpB.encrypted, dpB.hasEndList, dpB.initSegment]),
    `${dpB.segments} 片 / ${dpB.totalDurationSec}s / 加密 ${dpB.encrypted}`);

  // ---------- 9. 直播 ----------
  console.log('\n[9] /api/live + /lp');
  const lva = await jget(A, '/api/live', { timeout: 120000 });
  const lvb = await jget(B, '/api/live', { timeout: 120000 });
  const idsA = new Set((lva.items || []).map(x => x.id));
  const common = (lvb.items || []).filter(x => idsA.has(x.id)).length;
  // 下课以后全校都没直播，两版都返回空列表 —— 这不算不一致
  const emptyBoth = (lva.items || []).length === 0 && (lvb.items || []).length === 0;
  ok(emptyBoth ? '两版都返回空直播列表（当前没课）' : '两版都有直播且交集占多数',
    !!(lva.ok && lvb.ok) && (emptyBoth ||
      ((lvb.items || []).length > 0 && common >= Math.min((lva.items || []).length, (lvb.items || []).length) * 0.5)),
    `Node ${(lva.items || []).length} 场 / Go ${(lvb.items || []).length} 场，交集 ${common}`);
  ok('字段齐全且按 phase 排序', (() => {
    const rank = { live: 0, upcoming: 1, ended: 2 };
    let last = -1;
    for (const x of (lvb.items || [])) {
      if (!x.id || !x.title || !x.phase || !x.main) return false;
      const r = rank[x.phase] === undefined ? 3 : rank[x.phase];
      if (r < last) return false;
      last = r;
    }
    return true;
  })(), [...new Set((lvb.items || []).map(x => x.phase))].join('/'));
  const liveItem = (lvb.items || []).find(x => x.main);
  if (liveItem) {
    const lu = Buffer.from(liveItem.main, 'utf8').toString('base64url');
    const lr = await req(B, '/lp?u=' + lu, { timeout: 60000 });
    ok('/lp 直播流可用', lr.status === 200, `${lr.status} ${lr.headers['content-type']}`);
    if (/mpegurl/.test(lr.headers['content-type'] || '')) {
      const lines = lr.body.toString('utf8').split('\n').filter(l => l && !l.startsWith('#'));
      ok('/lp 把分片行改写成 /lp?u=', lines.length > 0 && lines.every(l => l.startsWith('/lp?u=')), lines.length + ' 行');
    }
  }

  // ---------- 10. 学期表 / 自检 / 签名 ----------
  console.log('\n[10] 学期表 / 自检 / 签名来源');
  const ta = await jget(A, '/api/db/terms', ha), tb = await jget(B, '/api/db/terms', hb);
  ok('学期列表与课程总数一致', same(ta.terms, tb.terms) && ta.count === tb.count, `${tb.terms.length} 个学期 / ${tb.count} 门课`);
  const ca = await jget(A, '/api/selftest?id=72120', ha), cb = await jget(B, '/api/selftest?id=72120', hb);
  ok('自检项名称一致', same((ca.checks || []).map(c => c.name), (cb.checks || []).map(c => c.name)), (cb.checks || []).map(c => c.name).join(' | '));
  ok('两版都 allPass', !!(ca.allPass && cb.allPass));
  ok('live 判定一致', ca.live.verdict === cb.live.verdict, cb.live.verdict);
  const stA = await jget(A, '/api/status'), stB = await jget(B, '/api/status');
  ok('签名来源都是官网实时提取', /^live:/.test(stA.signer) && /^live:/.test(stB.signer), `Node ${stA.signer} / Go ${stB.signer}`);

  // ---------- 11. 错误路径 ----------
  console.log('\n[11] 错误路径');
  const errs = [
    ['/api/open 未登录', '/api/open?id=1', 401],
    ['/dl 未登录', '/dl?course=1&session=2', 401],
    ['/y2dl 未登录', '/y2dl?c=1&s=2', 401],
    ['/p 缺参数', '/p', 400],
    ['未知路径', '/nope', 404],
  ];
  for (const [name, path, code] of errs) {
    const a = await req(A, path), b = await req(B, path);
    ok(`${name} → ${code}`, a.status === code && b.status === code, `${a.status}/${b.status}`);
  }
  const badA = await jget(A, '/api/open?id=999999999', ha), badB = await jget(B, '/api/open?id=999999999', hb);
  ok('不存在的课程 ok=false 且 error/code 一致', badA.ok === badB.ok && badA.error === badB.error && badA.code === badB.code,
    JSON.stringify(badB));
  // 新版平台已移植完成，这里改成两版直接对拍（以前比的是 Go 那边的占位响应）
  const y2a = await jget(A, '/api/y2/status'), y2b = await jget(B, '/api/y2/status');
  ok('/api/y2/status 两版一致', same(y2a, y2b) && 'loggedIn' in y2b, JSON.stringify(y2b));

  console.log(`\n对拍结果：${pass} 通过，${fail} 失败\n`);
  process.exit(fail ? 1 : 0);
})().catch((e) => { console.error('对拍脚本自身出错：' + (e && e.stack || e)); process.exit(1); });
