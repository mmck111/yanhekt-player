/**
 * 动态签名器
 *
 * 延河课堂 CDN 的 m3u8/ts 需要 4 个参数：
 *   Xvideo_Token / Xclient_Timestamp / Xclient_Signature / Xclient_Version + Platform
 * 签名算法和密钥都在官网的 JS bundle 里。为了不写死（官网一改就挂），
 * 这里在运行时把官网那个混淆模块抠出来直接跑，结果缓存到磁盘；
 * 取不到时才退回上次缓存 / 内置默认值。
 *
 * 对外接口与官网模块一致： t() v() s(ts) p(url)
 */
const https = require('https');
const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');

const SITE = 'https://www.yanhekt.cn';
// 缓存写到用户目录，避免打包成 exe 后往程序目录写文件
const DATA_DIR = process.env.LOCALAPPDATA
  ? path.join(process.env.LOCALAPPDATA, 'yhkt-player')
  : path.join(os.tmpdir(), 'yhkt-player');
const CACHE_FILE = path.join(DATA_DIR, 'signer-cache.json');
const JS_CACHE_DIR = path.join(DATA_DIR, 'js');

try { fs.mkdirSync(DATA_DIR, { recursive: true }); } catch (e) {}

const DEFAULTS = {
  k: '1138b69dfef641d9d7ba49137d2d4875',
  v: 'v1',
  phash: 'c3d47d7b3aa8caf2983b313cb6cd142f', // p() 里插入路径的固定段
};

const md5 = s => crypto.createHash('md5').update(s, 'utf8').digest('hex');

// ---------- 官网 bundle 抓取 ----------
function httpGet(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    https.get(u, {
      headers: { 'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/124.0.0.0 Safari/537.36', Accept: '*/*' },
      rejectUnauthorized: false, servername: u.hostname,
    }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location && redirects < 5) {
        res.resume();
        return resolve(httpGet(new URL(res.headers.location, url).href, redirects + 1));
      }
      if (res.statusCode !== 200) { res.resume(); return reject(new Error('HTTP ' + res.statusCode + ' ' + url)); }
      const c = []; res.on('data', d => c.push(d));
      res.on('end', () => resolve(Buffer.concat(c)));
    }).on('error', reject).setTimeout(30000, function () { this.destroy(new Error('timeout')); });
  });
}

async function listBundles() {
  const html = (await httpGet(SITE + '/')).toString('utf8');
  const files = [...new Set([...html.matchAll(/src="(\/static\/js\/[^"]+\.js)"/g)].map(m => m[1]))];
  // 签名模块在 main-*.js 里，优先试这些，省掉扫全部 vendors 的时间
  return files.sort((a, b) => {
    const am = /\/main-/.test(a) ? 0 : 1;
    const bm = /\/main-/.test(b) ? 0 : 1;
    return am - bm;
  });
}

async function fetchBundle(p) {
  const name = p.split('/').pop();
  // 缓存是纯优化：目录不可写（只读环境 / 权限受限）也不能影响取 bundle
  let cachePath = null;
  try {
    fs.mkdirSync(JS_CACHE_DIR, { recursive: true });
    cachePath = path.join(JS_CACHE_DIR, name);
    if (fs.existsSync(cachePath)) {
      const st = fs.statSync(cachePath);
      if (st.size > 1000 && Date.now() - st.mtimeMs < 6 * 3600 * 1000) return fs.readFileSync(cachePath, 'utf8');
    }
  } catch (e) { cachePath = null; }

  const buf = await httpGet(SITE + p);
  if (cachePath) { try { fs.writeFileSync(cachePath, buf); } catch (e) {} }
  return buf.toString('utf8');
}

// ---------- 从 bundle 里抠出签名模块 ----------
function extractModule(src) {
  // 特征：v:()=>"v1"  +  md5 初始化常量
  const marker = src.indexOf('v:()=>"v1"');
  if (marker < 0) return null;

  // 向前找模块定义头  NNNNNN:(a,b,c)=>{
  const headRe = /(\d{3,7}):\((\w+),(\w+),(\w+)\)=>\{/g;
  let start = -1, params = null;
  let m;
  while ((m = headRe.exec(src)) !== null && m.index < marker) {
    start = m.index + m[0].length - 1; // 指向 "{"
    params = [m[2], m[3], m[4]];
  }
  if (start < 0) return null;

  // 配平大括号
  let depth = 0, i = start;
  for (; i < src.length; i++) {
    const ch = src[i];
    if (ch === '{') depth++;
    else if (ch === '}') { depth--; if (depth === 0) break; }
    else if (ch === '"' || ch === "'" || ch === '`') {
      const q = ch; i++;
      while (i < src.length && src[i] !== q) { if (src[i] === '\\') i++; i++; }
    }
  }
  if (depth !== 0) return null;
  return src.slice(start, i + 1);
}

function instantiate(body) {
  const factory = eval('(function(n,t,e){' + body + '})');
  const t = {};
  const e = { d: (obj, defs) => { for (const k of Object.keys(defs)) Object.defineProperty(obj, k, { get: defs[k], enumerable: true, configurable: true }); } };
  factory({}, t, e);
  if (!t.A) throw new Error('模块未导出 A');
  return new t.A();
}

// ---------- 对外 ----------
let impl = null;
let source = 'pending';

const fallback = {
  t: () => parseInt(Date.now() / 1000),
  v: () => DEFAULTS.v,
  s: (n) => md5(DEFAULTS.k + '_' + DEFAULTS.v + '_' + n),
  p: (u) => {
    const o = u.lastIndexOf('/');
    return o > -1 ? u.slice(0, o) + '/' + DEFAULTS.phash + u.slice(o) : u;
  },
};

function wrap(inst) {
  return {
    t: () => inst.t(),
    v: () => inst.v(),
    s: (n) => inst.s(n),
    p: (u) => inst.p(u),
  };
}

function loadCache() {
  try {
    if (fs.existsSync(CACHE_FILE)) {
      const j = JSON.parse(fs.readFileSync(CACHE_FILE, 'utf8'));
      if (j && j.k) return j;
    }
  } catch (e) {}
  return null;
}

/**
 * 启动时调用一次；之后每 6 小时可再调一次做静默刷新
 */
async function init({ force = false } = {}) {
  // 先从磁盘缓存恢复，保证立刻可用
  const cached = loadCache();
  if (cached && !impl) {
    impl = {
      t: () => parseInt(Date.now() / 1000),
      v: () => cached.v,
      s: (n) => md5(cached.k + '_' + cached.v + '_' + n),
      p: (u) => { const o = u.lastIndexOf('/'); return o > -1 ? u.slice(0, o) + '/' + cached.phash + u.slice(o) : u; },
    };
    source = 'cache';
  }

  try {
    const bundles = await listBundles();
    if (process.env.YHKT_DEBUG) console.log('[signer] 候选 bundle:', bundles.length);
    for (const p of bundles) {
      let src;
      try { src = await fetchBundle(p); }
      catch (e) { if (process.env.YHKT_DEBUG) console.log('[signer] 取不到', p.split('/').pop(), e.message); continue; }
      if (src.indexOf('v:()=>"v1"') < 0) continue;
      if (process.env.YHKT_DEBUG) console.log('[signer] 命中标记', p.split('/').pop(), 'len', src.length);

      const body = extractModule(src);
      if (process.env.YHKT_DEBUG) console.log('[signer] extractModule ->', body ? body.length + ' chars' : 'null');
      if (!body) continue;

      const inst = instantiate(body);
      const wrapped = wrap(inst);
      // 自检：s() 应该是 32 位十六进制
      const probe = wrapped.s(1700000000);
      if (!/^[0-9a-f]{32}$/i.test(probe)) throw new Error('签名自检失败');

      impl = wrapped;
      source = 'live:' + p.split('/').pop();

      const phash = (() => {
        try {
          const out = wrapped.p('https://cvideo.yanhekt.cn/a/b/c.m3u8');
          const seg = out.split('/').slice(-2)[0];
          return /^[0-9a-f]{32}$/.test(seg) ? seg : DEFAULTS.phash;
        } catch (e) { return DEFAULTS.phash; }
      })();

      try {
        fs.writeFileSync(CACHE_FILE, JSON.stringify({
          k: inst.k ? inst.k() : DEFAULTS.k,
          v: wrapped.v(),
          phash,
          source,
          at: new Date().toISOString(),
        }, null, 2));
      } catch (e) { /* 缓存写不进去就跳过 */ }
      break;
    }
  } catch (e) {
    // 网络不通就沿用缓存 / 默认值
    if (process.env.YHKT_DEBUG) console.log('[signer] 动态提取失败:', e && e.stack ? e.stack.split('\n').slice(0, 3).join(' | ') : e);
  }

  if (!impl) { impl = fallback; source = 'default'; }
  return { source, ok: source.startsWith('live') };
}

function get() { return impl || fallback; }

module.exports = {
  init,
  get,
  status: () => source,
  sign: (src, token) => {
    const s = get();
    const ts = s.t();
    return s.p(src) + `?Xvideo_Token=${token}&Xclient_Timestamp=${ts}&Xclient_Signature=${s.s(ts)}&Xclient_Version=${s.v()}&Platform=yhkt_user`;
  },
};

// 直接运行本文件时做一次自检
if (require.main === module) {
  init().then(r => {
    console.log('签名来源:', r.source);
    const s = get();
    console.log('k/v:', JSON.stringify(s.v()));
    console.log('s(1700000000):', s.s(1700000000));
    console.log('p():', s.p('https://cvideo.yanhekt.cn/vod/2026/10/20/67018356/0/Video1/Video1.m3u8'));
    console.log('sign():', module.exports.sign('https://cvideo.yanhekt.cn/vod/2026/10/20/67018356/0/Video1/Video1.m3u8', 'TESTTOKEN'));
  }).catch(e => console.error('FAIL', e));
}
