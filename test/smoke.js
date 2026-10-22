/**
 * 冒烟测试：真的把 server.js 跑起来，用 HTTP 检查关键接口。
 *
 *   node test/smoke.js
 *
 * 不依赖登录：/api/open 这类要登录的只验"未登录时正确返回 401"。
 * 搜索那一条需要联网（访问延河课堂），断网时会报 SKIP 而不是失败。
 */
const http = require('http');
const { spawn } = require('child_process');
const path = require('path');

const PORT = Number(process.env.SMOKE_PORT || 8897);
const BASE = process.env.SMOKE_URL || `http://127.0.0.1:${PORT}`;
const root = path.join(__dirname, '..');

let pass = 0, fail = 0, skip = 0;
const ok = (name, cond, detail) => {
  if (cond) { pass++; console.log('  ✓ ' + name + (detail ? '  → ' + detail : '')); }
  else { fail++; console.log('  ✗ ' + name + (detail ? '  → ' + detail : '')); }
};
const skipped = (name, why) => { skip++; console.log('  - ' + name + '（跳过：' + why + '）'); };

function get(p, timeout = 30000) {
  return new Promise((resolve) => {
    const req = http.get(BASE + p, { timeout }, (res) => {
      let b = '';
      res.setEncoding('utf8');
      res.on('data', d => (b += d));
      res.on('end', () => resolve({ status: res.statusCode, body: b }));
    });
    req.on('error', e => resolve({ status: 0, body: String(e.message) }));
    req.on('timeout', () => { req.destroy(); resolve({ status: 0, body: 'timeout' }); });
  });
}
const json = (r) => { try { return JSON.parse(r.body); } catch (e) { return null; } };

function post(p, body = '', timeout = 40000) {
  return new Promise((resolve) => {
    const u = new URL(BASE + p);
    const req = http.request({ host: u.hostname, port: u.port, path: u.pathname + u.search, method: 'POST', timeout,
      headers: { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body) } }, (res) => {
      let b = '';
      res.setEncoding('utf8');
      res.on('data', d => (b += d));
      res.on('end', () => resolve({ status: res.statusCode, body: b }));
    });
    req.on('error', e => resolve({ status: 0, body: String(e.message) }));
    req.on('timeout', () => { req.destroy(); resolve({ status: 0, body: 'timeout' }); });
    if (body) req.write(body);
    req.end();
  });
}

async function waitUp(ms = 15000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    const r = await get('/api/status', 1500);
    if (r.status === 200) return true;
    await new Promise(r => setTimeout(r, 200));
  }
  return false;
}

(async () => {
  // SMOKE_URL=http://127.0.0.1:8898 时不去起 node 服务，直接测已经在跑的那个（Go 版对拍用）
  const external = process.env.SMOKE_URL || '';
  const child = external ? null : spawn(process.execPath, [path.join(root, 'server.js')], {
    cwd: root,
    // 不用 pipe：这样在任何环境下都不会卡在管道上，输出丢掉也不影响断言
    stdio: 'ignore',
    env: { ...process.env, PORT: String(PORT), NO_APP: '1', FORCE_NEW: '1' },
  });

  const done = (code) => { try { child && child.kill(); } catch (e) {}; process.exit(code); };

  try {
    if (!await waitUp()) {
      console.log('  ✗ 服务器 15 秒内没起来（端口 ' + PORT + ' 是否被占？）');
      return done(1);
    }

    console.log('\n页面与静态资源');
    const home = await get('/');
    ok('GET / 返回 200', home.status === 200, 'HTTP ' + home.status);
    ok('/ 是播放器页面', home.body.includes('延河课堂') && home.body.includes('loginTpl'));
    ok('/ 内联 hls.min.js', home.body.includes('/hls.min.js'));
    ok('/old 也能打开（同一个页面）', (await get('/old')).status === 200);
    ok('/y2 新版平台页面', (await get('/y2')).status === 200);
    ok('/api/y2/status 可用', !!(json(await get('/api/y2/status')) || {}).hasOwnProperty('loggedIn'));
    ok('/y2dl 未登录返回 401', (await get('/y2dl?c=1&s=2')).status === 401);

    console.log('\n接口');
    const st = json(await get('/api/status'));
    ok('/api/status 有版本号', !!(st && st.build), st && st.build);
    ok('/api/status 未登录', st && st.loggedIn === false);

    const self = json(await get('/api/selftest'));
    ok('/api/selftest 全部通过', !!(self && self.allPass),
      self ? self.checks.filter(c => !c.pass).map(c => c.name).join('；') : '无响应');

    ok('/api/open 未登录返回 401', (await get('/api/open?id=1')).status === 401);
    ok('/dl 未登录返回 401', (await get('/dl?course=1&session=2')).status === 401);

    // 自动登录：本机浏览器里有登录态就该成功，没有也得正常返回 ok:false 而不是报错
    const autoRes = await post('/api/login/auto');
    const auto = json(autoRes);
    if (!auto) ok('自动登录接口可用', false, 'HTTP ' + autoRes.status + ' ' + autoRes.body.slice(0, 60));
    else if (auto.ok) ok('自动登录接口可用（这台机器浏览器里有登录态）', !!(auto.user && auto.user.id), '来自 ' + auto.from);
    else ok('自动登录接口可用（这台机器浏览器里没有登录态）', auto.ok === false, auto.error);

    const terms = json(await get('/api/db/terms'));
    ok('/api/db/terms 有学期列表', !!(terms && terms.ok && terms.terms.length > 0),
      terms ? terms.terms.length + ' 个学期' : '无响应');

    const live = json(await get('/api/live', 40000));
    ok('/api/live 可用', !!(live && typeof live.ok === 'boolean'),
      live && live.ok ? (live.items || []).length + ' 场直播' : '接口正常但没取到（' + ((live && live.error) || '?') + '）');

    console.log('\n联网检索（需要能访问学校服务器）');
    const search = await get('/api/db/search?q=' + encodeURIComponent('数值分析') + '&limit=3');
    const sj = json(search);
    if (!sj || !sj.ok) skipped('按课程名搜索', '联网失败：' + (sj && sj.error || search.body.slice(0, 60)));
    else if (!sj.courses.length) skipped('按课程名搜索', '没搜到（可能学校服务器在维护）');
    else {
      const c = sj.courses[0];
      ok('按课程名搜索有结果', c.name && c.term, c.name + ' · ' + c.term);
      ok('结果带课程编号', /^\d+$/.test(String(c.id)), String(c.id));
    }

    console.log('\n结果：' + pass + ' 通过，' + fail + ' 失败，' + skip + ' 跳过\n');
    return done(fail ? 1 : 0);
  } catch (e) {
    console.log('  ✗ 测试本身出错：' + e.message);
    return done(1);
  }
})();
