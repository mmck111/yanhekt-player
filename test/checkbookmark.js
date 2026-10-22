/**
 * 新版「一键登录」书签脚本的行为测试：在 Node 里用假 DOM 跑一遍。
 *
 *   node test/checkbookmark.js
 *
 * 验的是三件事：
 *   1. 在延河页面（有 _token cookie）→ 跳到播放器并带上 t=/c=/h=
 *   2. 在播放器页面上点（hostname 是 127.0.0.1）→ 不跳，弹提示
 *   3. 延河页面但没登录 → 不跳，弹提示
 */
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'y2.html'), 'utf8');
const block = [...html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi)].map(m => m[1]).join('\n');
const fnSrc = /function loginScript\(\)\{[\s\S]*?\n\}/.exec(block);
if (!fnSrc) { console.log('✗ 找不到 loginScript()'); process.exit(1); }

const PLAYER = 'http://127.0.0.1:8788';
const JWT = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6MTQ4MjI2fQ.abc+/def-ghi_jkl=';
const serialized = 'a:2:{i:0;s:6:"_token";i:1;s:' + JWT.length + ':"' + JWT + '";}';

function run({ hostname, cookie, storage, here }) {
  const out = { jumped: '', alerts: [], timers: 0, pending: [] };
  const self = here || (PLAYER + '/y2');
  const sandbox = {
    location: {
      hostname,
      search: '',
      set href(v) { out.jumped = v; },
      get href() { return self; },
    },
    document: { cookie: cookie || '' },
    localStorage: { getItem: () => (storage && storage.local) || null },
    sessionStorage: { getItem: () => (storage && storage.session) || null },
    URLSearchParams,
    fetch: () => new Promise(() => {}),      // 永远不返回：靠 1.5 秒兜底
    alert: m => out.alerts.push(String(m)),
    setTimeout: (fn) => { out.timers++; out.pending.push(fn); return 0; },
    clearTimeout: () => {},
    encodeURIComponent, decodeURIComponent, console,
  };
  vm.createContext(sandbox);
  // 让生成脚本时用的 origin 是播放器地址
  sandbox.location.origin = PLAYER;
  vm.runInContext(fnSrc[0], sandbox);
  const code = vm.runInContext('loginScript()', sandbox);
  vm.runInContext(code.replace(/^javascript:/, ''), sandbox);
  // 把兜底定时器真的执行一次（真实环境里 1.5 秒后也是如此）
  for (const fn of out.pending) { try { fn(); } catch (e) {} }
  return out;
}

let pass = 0, fail = 0;
const check = (name, cond, extra) => {
  if (cond) { pass++; console.log('  ✓ ' + name); }
  else { fail++; console.log('  ✗ ' + name + (extra ? '  → ' + extra : '')); }
};

// 1) 正常：延河页面，有 _token
{
  const r = run({ hostname: 'aita.yanhekt.cn', cookie: '_token=' + encodeURIComponent(serialized) + '; cmc_version=1' });
  const url = r.jumped || '';
  const m = /#t=([^&]*)&c=([^&]*)(?:&h=(\w+))?/.exec(url);
  check('延河页面会跳回播放器', url.startsWith(PLAYER + '/'), url.slice(0, 60));
  check('跳回的是 /y2（不能落到老平台根页面）', url.startsWith(PLAYER + '/y2#t='), url.slice(0, 60));
  check('带回的是纯 JWT', m && decodeURIComponent(m[1]) === JWT, m ? m[1].slice(0, 24) : '无');
  check('带回整段 cookie', m && decodeURIComponent(m[2]).includes('cmc_version=1'));
  check('没弹提示', r.alerts.length === 0, r.alerts[0]);
  check('留了兜底定时器', r.timers === 1);
}

// 2) 坑：在播放器页面上点了这个按钮
{
  const r = run({ hostname: '127.0.0.1', cookie: '' });
  const said = r.alerts.join('');
  check('播放器页面点击 → 不跳', r.jumped === '');
  check('播放器页面点击 → 说明要拖到书签栏', /书签栏/.test(said), said.slice(0, 40));
  check('播放器页面点击 → 指向延河页面', /延河/.test(said), said.slice(0, 40));
}

// 3) 在完全别的网站上点（比如百度）
{
  const r = run({ hostname: 'www.baidu.com', cookie: '' });
  const said = r.alerts.join('');
  check('别的网站点击 → 不跳', r.jumped === '');
  check('别的网站点击 → 报出当前站点', said.includes('www.baidu.com'), said.slice(0, 40));
  check('别的网站点击 → 让去 aita 登录', said.includes('aita.yanhekt.cn'), said.slice(0, 60));
}

// 4) 延河页面但还没登录
{
  const r = run({ hostname: 'aita.yanhekt.cn', cookie: 'other=1' });
  const said = r.alerts.join('');
  check('没登录 → 不跳', r.jumped === '');
  check('没登录 → 提示先登录', /登录/.test(said), said.slice(0, 50));
  check('没登录 → 提醒再点一次书签', /再点一次/.test(said), said.slice(0, 60));
}

// 5) 裸 JWT 直接放在 cookie 里
{
  const r = run({ hostname: 'aita.yanhekt.cn', cookie: '_token=' + JWT });
  check('cookie 里是裸 JWT 也能认', (r.jumped || '').includes(encodeURIComponent(JWT).slice(0, 20)));
}

// 6) 老平台页面（同域）也能用
{
  const r = run({ hostname: 'www.yanhekt.cn', cookie: '_token=' + encodeURIComponent(serialized) });
  check('老平台页面点击也能登录新版', (r.jumped || '').startsWith(PLAYER + '/y2#t='), (r.jumped || '').slice(0, 40));
}

console.log('\n结果：' + pass + ' 通过，' + fail + ' 失败\n');
process.exit(fail ? 1 : 0);
