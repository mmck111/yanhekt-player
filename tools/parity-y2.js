/**
 * y2（新版平台）匿名/错误路径对拍：Node 版 vs Go 版。
 *
 *   node tools/parity-y2.js http://127.0.0.1:8898 http://127.0.0.1:8899
 *
 * 这些请求都不需要真 JWT —— 比的正是"没登录 / 假 token"时两版返回是否逐字段一致。
 * 真登录链路没法在没有 JWT 的环境里对拍，只能靠单测和代码审查（见 go/y2_*.go 的注释）。
 */
const http = require('http');

const A = (process.argv[2] || 'http://127.0.0.1:8898').replace(/\/$/, '');
const B = (process.argv[3] || 'http://127.0.0.1:8899').replace(/\/$/, '');

function req(base, path, { method = 'GET', body = null, headers = {} } = {}) {
  return new Promise((resolve) => {
    const u = new URL(base + path);
    const h = Object.assign({}, headers);
    let payload = null;
    if (body !== null) {
      payload = Buffer.from(JSON.stringify(body));
      h['content-type'] = 'application/json';
      h['content-length'] = payload.length;
    }
    const r = http.request({ host: u.hostname, port: u.port, path: u.pathname + u.search, method, headers: h, timeout: 60000 }, (res) => {
      const chunks = [];
      res.on('data', d => chunks.push(d));
      res.on('end', () => resolve({ status: res.statusCode, body: Buffer.concat(chunks).toString('utf8') }));
    });
    r.on('error', e => resolve({ status: 0, body: 'ERR ' + e.message }));
    r.on('timeout', () => { r.destroy(); resolve({ status: 0, body: 'timeout' }); });
    if (payload) r.write(payload);
    r.end();
  });
}

const FAKE_JWT = 'eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MX0.' + 'a'.repeat(40);

const cases = [
  ['GET', '/api/y2/status', null],
  ['GET', '/api/y2/terms', null],
  ['GET', '/api/y2/courses', null],
  ['GET', '/api/y2/search?q=' + encodeURIComponent('数值分析'), null],
  ['GET', '/api/y2/find?q=' + encodeURIComponent('数值分析'), null],
  ['GET', '/api/y2/sessions?courseId=96123', null],
  ['GET', '/api/y2/sub?courseId=96123&subId=1', null],
  ['POST', '/api/y2/login', { jwt: FAKE_JWT }],
  ['POST', '/api/y2/login', { jwt: '' }],
  ['POST', '/api/y2/login', { jwt: '_token=' + encodeURIComponent(FAKE_JWT) + '; locale=zh_cn' }],
  ['GET', '/y2dl?c=1&s=2', null],
  ['GET', '/y2play?u=abc', null],
  ['GET', '/y2play', null],
];

// 把 JSON 字符串规范化：解析 → 排序键 → 再序列化，忽略键顺序
function canon(text) {
  try {
    const walk = (v) => {
      if (Array.isArray(v)) return v.map(walk);
      if (v && typeof v === 'object') {
        const out = {};
        for (const k of Object.keys(v).sort()) out[k] = walk(v[k]);
        return out;
      }
      return v;
    };
    return JSON.stringify(walk(JSON.parse(text)));
  } catch (e) {
    return text.trim();
  }
}

(async () => {
  let fail = 0;
  console.log(`Node: ${A}\nGo  : ${B}\n`);
  for (const [method, path, body] of cases) {
    const [ra, rb] = await Promise.all([req(A, path, { method, body }), req(B, path, { method, body })]);
    const same = ra.status === rb.status && canon(ra.body) === canon(rb.body);
    const label = `${method} ${path.slice(0, 64)}${body ? '  +body' : ''}`;
    if (same) {
      console.log(`  ✓ ${label}  (${ra.status})`);
    } else {
      fail++;
      console.log(`  ✗ ${label}`);
      console.log(`     Node ${ra.status}: ${ra.body.slice(0, 240)}`);
      console.log(`     Go   ${rb.status}: ${rb.body.slice(0, 240)}`);
    }
  }
  console.log(`\n${fail ? fail + ' 处不一致' : 'y2 匿名/错误路径完全一致'}\n`);
  process.exit(fail ? 1 : 0);
})();
