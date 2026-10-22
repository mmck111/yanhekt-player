/**
 * 延河课堂 在线播放器（多用户版）
 *
 *   node server.js                  只在本机   http://127.0.0.1:8788
 *   LAN=1 node server.js            局域网可访问 http://<本机IP>:8788
 *   PORT=9000 LAN=1 node server.js
 *
 * 设计取舍
 *   - 服务器不保存任何人的身份：token 存在各人自己浏览器的 cookie 里（HttpOnly）
 *   - 签名算法运行时从官网 bundle 现抠（signer.js），官网改版可自愈
 *   - 上游单连接串行（实测 CDN 只容忍极少并发，并发会被挂起），本地缓存分片
 */
const https = require('https');
const http = require('http');
const os = require('os');
const fs = require('fs');
const path = require('path');
const { URL } = require('url');
const signer = require('./signer');
const y2 = require('./yanhe2');

const PORT = Number(process.env.PORT || 8788);
const LAN = process.env.LAN === '1' || process.env.HOST === '0.0.0.0';
const HOST = LAN ? '0.0.0.0' : '127.0.0.1';
const PACKAGED = (() => { try { return require('node:sea').isSea(); } catch (e) { return false; } })();
const DEBUG = process.env.YHKT_DEBUG === '1';

const ORIGIN = 'https://www.yanhekt.cn';
const UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36';
const COOKIE = 'yht';
const COOKIE_TTL = 6 * 24 * 3600;
// 版本号：启动横幅、页面右下角、/api/status 都会显示，用来确认"跑的是哪一版"
const BUILD = '1.5.0';

// 打包后从 exe 内读资源，开发时读 public/
function loadAsset(name) {
  if (PACKAGED) return Buffer.from(require('node:sea').getAsset(name));
  const tries = [
    path.join(__dirname, 'public', name),      // node server.js
    path.join(__dirname, '..', 'public', name), // node dist/app.js
  ];
  for (const p of tries) {
    if (fs.existsSync(p)) return fs.readFileSync(p);
  }
  throw new Error('找不到资源: ' + name);
}

// ---------------- 内置课程库（从"延河课堂-全部课程信息.xlsx"压出来） ----------------
// 老平台按课程ID取数据，但没地方按名字搜；这张表把 79k 门课的「名字 ↔ ID ↔ 学期 ↔ 教师」带进来了。
// 578 KB gzip，随 exe 分发，搜索/筛学期都在本地做，不发请求。
let catalogCache = null;

function loadCatalog() {
  if (catalogCache) return catalogCache;
  try {
    const zlib = require('zlib');
    const raw = zlib.gunzipSync(loadAsset('catalog.json.gz')).toString('utf8');
    const j = JSON.parse(raw);
    const names = j.names, teachers = j.teachers, terms = j.terms;
    const rows = j.rows.map(r => ({ n: names[r[0]] || '', t: teachers[r[1]] || '', term: terms[r[2]] || '', id: String(r[3]) }));
    catalogCache = { terms, rows, meta: j.meta || {}, byId: new Map(rows.map(r => [r.id, r])) };
  } catch (e) {
    console.log('  [warn] 课程库加载失败: ' + e.message);
    catalogCache = { terms: [], rows: [], meta: {}, byId: new Map(), error: e.message };
  }
  return catalogCache;
}

/** 在课程库里搜（名字或老师包含关键词），可按学期过滤 */
function catalogSearch(q, term, limit = 40) {
  const cat = loadCatalog();
  const key = String(q || '').replace(/\s+/g, '').toLowerCase();
  const out = [];
  let total = 0;
  for (const r of cat.rows) {
    if (term && r.term !== term) continue;
    if (key) {
      const n = r.n.toLowerCase();
      const t = r.t.toLowerCase();
      if (!n.includes(key) && !t.includes(key)) continue;
    }
    total++;
    if (out.length < limit) out.push({ id: r.id, name: r.n, teacher: r.t, term: r.term });
  }
  return { total, courses: out };
}

/** 只看教师名（课程库离线用） */
function catalogByTeacher(name, limit = 20) {
  const cat = loadCatalog();
  const key = String(name || '').trim().toLowerCase();
  if (!key) return [];
  const seen = new Set();
  const out = [];
  for (const r of cat.rows) {
    if (!r.t.toLowerCase().includes(key)) continue;
    const k = r.id;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push({ id: r.id, name: r.n, teacher: r.t, term: r.term });
    if (out.length >= limit) break;
  }
  return out;
}

/** 用离线课程库的老师索引，找出这个人教过的课名（去重、限量） */
function teacherCourseTitles(name, limit = 12) {
  const key = String(name || '').trim().toLowerCase();
  if (!key) return [];
  const seen = new Set();
  const out = [];
  for (const r of catalogByTeacher(name, 60)) {
    const n = String(r.name || '').trim();
    if (!n) continue;
    const k = n.replace(/\s+/g, '').toLowerCase();
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(n);
    if (out.length >= limit) break;
  }
  return out;
}

// ---------------- 老平台的课程检索 / 学期表（服务端接口，匿名可用） ----------------
let tagCache = null;

/** 学期名 → {year:'2026-2027', sem:1} */
function parseTermName(name) {
  const s = String(name || '');
  const m = s.match(/(20\d{2})\s*[-—~]\s*(20\d{2})/);
  if (!m) return null;
  const sem = /第三|3学期/.test(s) ? 3 : /第二|2学期/.test(s) ? 2 : /第一|1学期/.test(s) ? 1 : 0;
  return { year: `${m[1]}-${m[2]}`, sem };
}

/** 这门课是不是这个学期的 */
function courseInTerm(c, want) {
  if (!want) return true;
  const y = String(c.school_year || '').trim();
  if (y && y !== want.year) return false;
  const s = Number(c.semester) || 0;
  if (want.sem && s && s !== want.sem) return false;
  return true;
}

/** 学期表：GET /v1/tag/list?with_sub=true → param==='semesters' 的那一组 */
async function oldSemesters(token) {
  if (tagCache && Date.now() - tagCache.at < 3600e3) return tagCache.list;
  const j = await apiCall('/v1/tag/list?with_sub=true', token || null);
  const tags = Array.isArray(j && j.data) ? j.data : [];
  const tag = tags.find(t => t && t.param === 'semesters');
  const list = (tag && Array.isArray(tag.children) ? tag.children : [])
    .map(c => ({ id: c.id, name: String(c.name || '').trim(), sort: Number(c.sort) || 0 }))
    .filter(x => x.name);
  list.sort((a, b) => b.sort - a.sort);
  // 只有真的拿到了才缓存，否则下次登录后再试
  if (list.length) tagCache = { at: Date.now(), list };
  return list;
}

/** 课程检索：GET /v2/course/list?keyword=&semesters[]=&page=&page_size=（带 classrooms 真教室名） */
async function oldSearch({ keyword = '', semesterIds = [], page = 1, pageSize = 30 } = {}) {
  const p = new URLSearchParams();
  for (const id of semesterIds) p.append('semesters[]', String(id));
  p.append('page', String(page));
  p.append('page_size', String(pageSize));
  if (keyword) p.append('keyword', keyword);
  const j = await apiCall('/v2/course/list?' + p.toString(), null);
  if (!j || (j.code !== 0 && j.code !== '0')) throw new Error((j && j.message) || '课程检索被拒绝');
  const d = j.data || {};
  const list = Array.isArray(d.data) ? d.data : (Array.isArray(d.list) ? d.list : []);
  return {
    list, total: Number(d.total) || list.length,
    page: Number(d.current_page) || page, lastPage: Number(d.last_page) || 1,
  };
}

/** 按课程编号反查（keyword=id），用来补齐 classrooms / 学期 / 教师 */
const lookupCache = new Map();
async function lookupCourse(id) {
  const hit = lookupCache.get(String(id));
  if (hit && Date.now() - hit.at < 10 * 60 * 1000) return hit.c;
  const r = await oldSearch({ keyword: String(id), pageSize: 5 });
  const c = r.list.find(x => String(x.id) === String(id)) || r.list[0] || null;
  if (lookupCache.size > 500) lookupCache.clear();
  lookupCache.set(String(id), { at: Date.now(), c });
  return c;
}

/** 老平台课程对象 → 统一条目 */
function oldCourseRow(c) {
  if (!c) return null;
  const id = String(c.id || '');
  if (!id) return null;
  // classrooms: [{ id, name, number }] —— name 是真名，number 才是节次 location 里存的那个编码
  const roomList = (Array.isArray(c.classrooms) ? c.classrooms : []).filter(r => r && (r.name || r.number));
  const rooms = roomList.map(r => r.name || '').filter(Boolean);
  const byNumber = {};
  for (const r of roomList) if (r.number && r.name) byNumber[String(r.number).trim().toUpperCase()] = r.name;
  const profs = (Array.isArray(c.professors) ? c.professors : [])
    .map(p => (typeof p === 'string' ? p : (p && (p.name || p.realname)) || '')).filter(Boolean);
  const y = c.school_year || '';
  const s = c.semester || '';
  return {
    id,
    name: String(c.name_zh || c.name || '').trim(),
    nameEn: String(c.name_en || ''),
    code: String(c.code || ''),
    number: String(c.number || ''),
    teacher: profs.join('、'),
    term: y ? `${y} 第${['', '一', '二', '三'][Number(s)] || s}学期` : '',
    room: rooms[0] || '',
    rooms,
    roomByNumber: byNumber,
    college: String(c.college_name || ''),
    participants: Number(c.participant_count) || 0,
    source: 'old-api',
  };
}

/** 节次里的 location 可能是编码，用课程自带的 classrooms 换成真名 */
function roomName(loc, row) {
  const t = String(loc || '').trim();
  if (!t) return '';
  const map = (row && row.roomByNumber) || {};
  const hit = map[t.toUpperCase()];
  if (hit) return hit;
  return looksLikeRoomCode(t) ? '' : t;
}

/** 看起来像房间编码（280101004X）而不是教室名 */
function looksLikeRoomCode(s) {
  const t = String(s || '').trim();
  if (!t) return true;
  if (/[\u4e00-\u9fa5]/.test(t)) return false;              // 有中文 → 名字
  if (/\s/.test(t)) return false;
  if (/楼|室|馆|教|厅|场|中心|实验/.test(t)) return false;
  return /^[0-9A-Za-z_-]{5,}$/.test(t);                     // 纯编码
}

// ---------------- 基础工具 ----------------
function parseCookies(req) {
  const out = {};
  (req.headers.cookie || '').split(';').forEach(p => {
    const i = p.indexOf('=');
    if (i > 0) out[p.slice(0, i).trim()] = decodeURIComponent(p.slice(i + 1).trim());
  });
  return out;
}
function tokenOf(req) {
  const c = parseCookies(req);
  if (c[COOKIE]) return c[COOKIE];
  const h = req.headers.authorization || '';
  if (/^Bearer\s+/i.test(h)) return h.replace(/^Bearer\s+/i, '').trim();
  return '';
}
function json(res, code, obj, extraHeaders) {
  res.writeHead(code, Object.assign({ 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store' }, extraHeaders || {}));
  res.end(JSON.stringify(obj));
}
function readBody(req) {
  return new Promise((resolve) => {
    const c = []; req.on('data', d => c.push(d));
    req.on('end', () => { try { resolve(JSON.parse(Buffer.concat(c).toString('utf8') || '{}')); } catch (e) { resolve({}); } });
  });
}

// ---------------- 上游 API ----------------
function apiCall(p, token) {
  return new Promise((resolve, reject) => {
    const url = 'https://cbiz.yanhekt.cn' + p;
    const headers = {
      Accept: 'application/json', 'Xdomain-Client': 'web_user', 'xclient-version': 'v1',
      'xclient-timestamp': String(Math.floor(Date.now() / 1000)),
      Origin: ORIGIN, Referer: ORIGIN + '/',
    };
    if (token) headers.Authorization = 'Bearer ' + token;
    const r = https.get(url, { headers, rejectUnauthorized: false, servername: 'cbiz.yanhekt.cn' }, (res) => {
      const c = []; res.on('data', d => c.push(d));
      res.on('end', () => { try { resolve(JSON.parse(Buffer.concat(c).toString('utf8'))); } catch (e) { reject(new Error('API 返回非 JSON')); } });
    });
    r.on('error', reject);
    r.setTimeout(30000, () => r.destroy(new Error('API 超时')));
  });
}

const userCache = new Map();
async function verifyToken(token) {
  if (!token) return null;
  const hit = userCache.get(token);
  if (hit && Date.now() - hit.at < 10 * 60 * 1000) return hit.user;
  const r = await apiCall('/v1/user', token);
  if (r && r.code === 0 && r.data && r.data.id) {
    const user = { id: r.data.id, badge: r.data.badge, nickname: r.data.nickname };
    if (userCache.size > 500) userCache.clear();
    userCache.set(token, { at: Date.now(), user });
    return user;
  }
  userCache.delete(token);
  return null;
}

// ---------------- 自动登录：从本机浏览器已存的登录态里找 ----------------
// Edge / Chrome 的 localStorage 落在 leveldb 文件里，值是明文。
// 只读本机文件；找到的凭证只写进本机 cookie，别无他用。
const LS_MAX_BYTES = 6 * 1024 * 1024;

function browserRoots() {
  if (process.platform !== 'win32') return [];
  const la = process.env.LOCALAPPDATA || '';
  if (!la) return [];
  return [
    ['Edge', path.join(la, 'Microsoft', 'Edge', 'User Data')],
    ['Chrome', path.join(la, 'Google', 'Chrome', 'User Data')],
    ['Brave', path.join(la, 'BraveSoftware', 'Brave-Browser', 'User Data')],
    ['Vivaldi', path.join(la, 'Vivaldi', 'User Data')],
  ];
}

/** 各浏览器所有 profile 的 Local Storage 文件，最近改过的排前面 */
function localStoreFiles() {
  const out = [];
  for (const [name, root] of browserRoots()) {
    let profs = [];
    try { profs = fs.readdirSync(root, { withFileTypes: true }).filter(e => e.isDirectory()).map(e => e.name); } catch (e) { continue; }
    for (const prof of profs) {
      const dir = path.join(root, prof, 'Local Storage', 'leveldb');
      let names = [];
      try { names = fs.readdirSync(dir); } catch (e) { continue; }
      for (const f of names) {
        if (!/\.(log|ldb)$/i.test(f)) continue;
        const full = path.join(dir, f);
        try {
          const st = fs.statSync(full);
          if (!st.isFile() || st.size > LS_MAX_BYTES) continue;
          out.push({ browser: name, file: full, mtime: st.mtimeMs });
        } catch (e) {}
      }
    }
  }
  out.sort((a, b) => b.mtime - a.mtime);
  return out.slice(0, 200);
}

/**
 * 扫一遍浏览器本地存储，捞出两边的登录凭证。
 * 老平台：www.yanhekt.cn 下 localStorage 的 auth.token（32 位十六进制）
 * 新平台：aita.yanhekt.cn 下的 JWT（_token）
 */
function scanBrowserStore() {
  const oldTokens = new Map();   // token -> 浏览器
  const jwts = new Map();        // jwt -> 浏览器
  let files = 0;
  for (const f of localStoreFiles()) {
    let buf;
    try { buf = fs.readFileSync(f.file); } catch (e) { continue; }
    files++;
    const s = buf.toString('latin1');
    if (s.indexOf('yanhekt') === -1) continue;
    let i = -1;
    while ((i = s.indexOf('www.yanhekt.cn', i + 1)) !== -1) {
      const win = s.slice(i, i + 12000);
      for (const m of win.matchAll(/token\\?"\s*:\s*\\?"([0-9a-fA-F]{20,64})/g)) {
        if (!oldTokens.has(m[1])) oldTokens.set(m[1], f.browser);
      }
    }
    i = -1;
    while ((i = s.indexOf('aita.yanhekt.cn', i + 1)) !== -1) {
      const win = s.slice(i, i + 20000);
      for (const m of win.matchAll(/eyJ[\w-]{10,}\.[\w-]{10,}\.[\w-]{10,}/g)) {
        if (!jwts.has(m[0])) jwts.set(m[0], f.browser);
      }
    }
  }
  return { oldTokens: [...oldTokens], jwts: [...jwts], files };
}

let autoScanCache = { at: 0, data: null };
function scanBrowserStoreCached() {
  if (autoScanCache.data && Date.now() - autoScanCache.at < 10000) return autoScanCache.data;
  let data;
  try { data = scanBrowserStore(); } catch (e) { data = { oldTokens: [], jwts: [], files: 0, error: e.message }; }
  autoScanCache = { at: Date.now(), data };
  return data;
}

/** 自动登录老平台：验证候选 token，返回第一个有效的 */
async function autoLoginOld() {
  const scan = scanBrowserStoreCached();
  for (const [token, browser] of scan.oldTokens.slice(0, 40)) {
    const user = await verifyToken(token);
    if (user) return { token, user, from: browser, candidates: scan.oldTokens.length };
  }
  return { token: '', user: null, candidates: scan.oldTokens.length, files: scan.files };
}

/** 自动登录新平台：验证候选 JWT */
async function autoLoginNew() {
  const scan = scanBrowserStoreCached();
  for (const [jwt, browser] of scan.jwts.slice(0, 8)) {
    const id = await y2Who(jwt);
    if (id) return { jwt, id, from: browser, candidates: scan.jwts.length };
  }
  return { jwt: '', id: null, candidates: scan.jwts.length, files: scan.files };
}

// ---------------- 视频 token（按用户） ----------------
const vtCache = new Map();
async function videoToken(userToken, badge) {
  const hit = vtCache.get(userToken);
  const now = Date.now() / 1000;
  if (hit && hit.expiredAt - now > 120) return hit.vidToken;
  const res = await apiCall('/v1/auth/video/token?id=' + badge, userToken);
  if (!res || !res.data || !res.data.token) throw new Error('取视频 token 失败（登录可能已过期）');
  if (vtCache.size > 500) vtCache.clear();
  vtCache.set(userToken, { vidToken: res.data.token, expiredAt: res.data.expired_at });
  return res.data.token;
}

// ---------------- 拉流控制 ----------------
// 实测：CDN 只容忍极少量并发，多了会长时间挂起后 403。
// 所以串行化（maxSockets=1），本地排队；单连接就能跑满 4.5MB/s，播放只用到约 1/4。
const agent = new https.Agent({ keepAlive: true, maxSockets: 1, maxFreeSockets: 1, timeout: 20000 });

const segCache = new Map();
const playlistCache = new Map();
let segBytes = 0;
const SEG_CACHE_MAX = 96 * 1024 * 1024;   // 约 20 片，6~7 分钟画面

function cacheGet(u) {
  const b = segCache.get(u);
  if (!b) return null;
  segCache.delete(u); segCache.set(u, b);   // LRU
  return b;
}
function cachePut(u, buf) {
  if (buf.length > SEG_CACHE_MAX / 4) return;
  if (segCache.has(u)) return;
  segCache.set(u, buf); segBytes += buf.length;
  while (segBytes > SEG_CACHE_MAX && segCache.size > 1) {
    const k = segCache.keys().next().value;
    const v = segCache.get(k);
    segCache.delete(k); segBytes -= v.length;
  }
}

const sleep = ms => new Promise(r => setTimeout(r, ms));

function upstreamOnce(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const r = https.get(u, {
      headers: { 'User-Agent': UA, Referer: ORIGIN + '/', Origin: ORIGIN, Accept: '*/*' },
      rejectUnauthorized: false, servername: u.hostname, agent,
    }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location && redirects < 3) {
        res.resume();
        return resolve(upstreamOnce(new URL(res.headers.location, url).href, redirects + 1));
      }
      resolve(res);
    });
    r.on('error', reject);
    r.setTimeout(20000, () => r.destroy(new Error('上游超时')));
  });
}

// 非 200 就重试，别让播放器卡在退避里
async function upstreamGet(url, tries = 3) {
  let last = null;
  for (let i = 0; i < tries; i++) {
    const t0 = Date.now();
    try {
      const res = await upstreamOnce(url);
      if (DEBUG) console.log(`[up] try${i + 1} ${res.statusCode} ${Date.now() - t0}ms ${url.split('/').pop().slice(0, 30)}`);
      if (res.statusCode === 200) return res;
      res.resume();
      last = res;
      if (i < tries - 1) await sleep(250 * (i + 1));
    } catch (e) {
      if (DEBUG) console.log(`[up] try${i + 1} ERR ${e.message} ${Date.now() - t0}ms`);
      if (i >= tries - 1) throw e;
      await sleep(300 * (i + 1));
    }
  }
  return last;
}

/** 只取某门课的节次原始列表（老平台，1~2 个请求），用于搜索时快速判断有没有录像 / 教室 */
const rawSessionCache = new Map();

async function rawSessions(courseId, token) {
  const key = 's:' + String(token).slice(-10) + ':' + courseId;
  const hit = rawSessionCache.get(key);
  if (hit && Date.now() - hit.at < 5 * 60 * 1000) return hit.list;

  const raw = [];
  for (let page = 1; page <= 20; page++) {
    const j = await apiCall(`/v1/course/session/list?course_id=${courseId}&order_type=asc&page=${page}&page_size=100`, token);
    const list = j && Array.isArray(j.data) ? j.data : (j && j.data && j.data.data) || [];
    if (!list.length) break;
    raw.push(...list);
    if (list.length < 100) break;
  }
  if (rawSessionCache.size > 500) rawSessionCache.clear();
  rawSessionCache.set(key, { at: Date.now(), list: raw });
  return raw;
}

/**
 * 给课程库的搜索结果补上「有没有录像 / 教室」。
 * 老平台的节次列表一条请求就够了，所以这里敢对 20 门课直接探。
 */
async function probeOldCourses(courses, token, { concurrency = 6, onlyPlayable = true } = {}) {
  const out = [];
  let hidden = 0;
  const queue = courses.slice();

  const worker = async () => {
    while (queue.length) {
      const c = queue.shift();
      try {
        const raw = await rawSessions(c.id, token);
        // 有能播的讲次就算有录像：videos 可能是数组也可能是对象，video_ids 也算
        const playable = raw.filter(s => {
          const v = Array.isArray(s.videos) ? s.videos[0] : (s.videos && typeof s.videos === 'object' ? s.videos : null);
          return (v && (v.main || v.vga)) || (Array.isArray(s.video_ids) ? s.video_ids.length > 0 : !!s.video_ids);
        });
        // 节次列表压根没给（没权限 / 还没排课）→ 不下结论，标"未确认"，别把课藏了
        if (!raw.length) {
          out.push({ ...c, verified: false, videoCount: null, tag: '未确认', note: '节次列表为空，可能没有权限或还没排课' });
          continue;
        }
        if (!playable.length) {
          // 有节次但没有能用的播放地址。这有两种完全不同的情况，别混成一句"没有录像"：
          //  1) 有 video_ids → 是"取不到地址"（登录过期 / 该接口没给），重登就好
          //  2) 真的一个视频记录都没有 → 本学期的课仍然不下结论（录像可能还在传）
          const hasVideoRecord = raw.some(s => (Array.isArray(s.video_ids) ? s.video_ids.length > 0 : !!s.video_ids));
          if (hasVideoRecord) {
            out.push({
              ...c, verified: false, videoCount: null, totalSessions: raw.length, tag: '取不到地址',
              note: '这门课有录像记录，但没拿到播放地址 —— 多半是登录过期了，重新登录再搜一次',
            });
            continue;
          }
          if (termIsRecent(c.term)) {
            out.push({
              ...c, verified: false, videoCount: null, totalSessions: raw.length, tag: '本学期无录像',
              note: '本学期的课，录像可能还在上传（过几天再看）',
            });
            continue;
          }
          hidden++;
          continue;
        }
        playable.sort((a, b) => String(b.started_at || '').localeCompare(String(a.started_at || '')));
        // 教室：课程自带的 classrooms 最准；节次的 location 可能是编码，换成名字
        const ONLINE = /线上|网络|直播|云课堂|腾讯|钉钉|zoom/i;
        const fromSessions = playable.map(s => roomName(s.location, c)).filter(Boolean).filter(r => !ONLINE.test(r));
        const room = (Array.isArray(c.rooms) ? c.rooms.find(Boolean) : '') || fromSessions[0] || '';
        out.push({
          ...c,
          videoCount: playable.length, totalSessions: raw.length,
          room, verified: true,
          latestDate: (playable[0].started_at || '').slice(0, 10),
        });
      } catch (e) {
        // 探测失败（没权限 / 网络）→ 保留，但不给结论
        out.push({ ...c, verified: false, videoCount: null, note: e.message });
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(concurrency, courses.length) }, worker));

  const order = new Map(courses.map((c, i) => [c.id, i]));
  out.sort((a, b) => (order.get(a.id) || 0) - (order.get(b.id) || 0));
  if (!onlyPlayable) return { courses: out, hidden };
  return { courses: out, hidden };
}

/**
 * 搜索结果的"匹配度"打分（和录像多少无关）：
 *   - 关键词按空格拆词，每个词都要在**课程名 / 教师名 / 课程号**里至少命中一处
 *   - 完全相等 6 分，前缀匹配 4 分，包含 3 分（教师名按顿号/逗号拆开逐个比）
 *   - 整串和课程名/教师名完全相等再加分
 * 排序时先用它，再用录像数；录像再多但名字不像的课不会排到前面。
 */
function searchScore(c, q) {
  const qn = String(q || '').toLowerCase().trim();
  if (!qn) return 0;
  const tokens = qn.split(/\s+/).filter(Boolean);
  const name = String(c.name || c.title || '').toLowerCase();
  const code = String(c.code || '').toLowerCase();
  const teachers = String(c.teacher || '').toLowerCase().split(/[、,，;；/\s]+/).filter(Boolean);
  const fieldScore = (f, t) => {
    if (!f) return 0;
    if (f === t) return 6;
    if (f.startsWith(t)) return 4;
    if (t.length >= 2 && f.includes(t)) return 3;
    return 0;
  };
  let total = 0;
  for (const t of tokens) {
    let best = Math.max(fieldScore(name, t), fieldScore(code, t) - 1);
    for (const tn of teachers) best = Math.max(best, fieldScore(tn, t));
    if (best <= 0) return 0;   // 有一个词没命中，就不算匹配
    total += best;
  }
  if (name && name === qn) total += 4;
  if (teachers.some(tn => tn === qn)) total += 3;
  return total;
}

// ---------------- 直播（旧平台，免登录可读） ----------------
// 实测：/v1/live/hot/list 与 /v2/live/list 匿名就能读，直播流是公开的 m3u8
// （clive*.yanhekt.cn/live/xxx.m3u8，带 Access-Control-Allow-Origin: *）
const LIVE_TTL = 30 * 1000;
let liveCache = { at: 0, data: null };

const unescapeUrl = s => String(s || '').replace(/\\\//g, '/');
function parseLocalTime(s) {
  const t = Date.parse(String(s || '').replace(/-/g, '/'));
  return Number.isFinite(t) ? t : 0;
}

function liveRow(l) {
  const c = l.course || {};
  const profs = (c.professors || []).map(p => p.name).filter(Boolean);
  const teacher = profs.slice(0, 3).join('、') || (l.session && l.session.professor && l.session.professor.name) || '';
  const startedAt = l.schedule_started_at || l.started_at || (l.session && l.session.started_at) || '';
  const endedAt = l.schedule_ended_at || l.ended_at || (l.session && l.session.ended_at) || '';
  const s = parseLocalTime(startedAt), e = parseLocalTime(endedAt);
  const now = Date.now();
  const phase = s && now < s ? 'upcoming' : (e && now > e ? 'ended' : 'live');
  return {
    id: String(l.id || (l.session && l.session.id) || ''),
    title: l.title || c.name_zh || '',
    teacher,
    course: c.name_zh || '',
    room: l.subtitle || (l.session && l.session.section_group_title) || '',
    startedAt, endedAt, phase,
    main: unescapeUrl(l.target),
    vga: unescapeUrl(l.target_vga),
    roomCam: unescapeUrl(l.target_room),
    views: Number(l.participant_count || l.views || 0) || 0,
  };
}

async function liveListOld() {
  if (liveCache.data && Date.now() - liveCache.at < LIVE_TTL) return liveCache.data;
  const [hot, all] = await Promise.all([
    apiCall('/v1/live/hot/list?page=1&page_size=60').catch(() => null),
    apiCall('/v2/live/list?page=1&page_size=200').catch(() => null),
  ]);
  const map = new Map();
  const put = (l) => {
    if (!l) return;
    const row = liveRow(l);
    if (!row.id || !row.main) return;
    const prev = map.get(row.id);
    map.set(row.id, prev ? Object.assign(prev, row, { main: row.main || prev.main, vga: row.vga || prev.vga, roomCam: row.roomCam || prev.roomCam }) : row);
  };
  for (const it of (hot && Array.isArray(hot.data) ? hot.data : [])) put(it.live || it);
  const allRows = (all && all.data && Array.isArray(all.data.data)) ? all.data.data : (Array.isArray(all && all.data) ? all.data : []);
  for (const it of allRows) put(it);

  const now = Date.now();
  const items = [...map.values()]
    // 只要"现在在播 / 今天马上开播 / 刚结束半小时内"，别把 5000 多条历史全端出来
    .filter(x => {
      const e = parseLocalTime(x.endedAt);
      const s = parseLocalTime(x.startedAt);
      if (e && now - e > 30 * 60 * 1000) return false;
      if (s && s - now > 6 * 3600 * 1000) return false;
      return true;
    })
    .sort((a, b) => {
      const rank = x => (x.phase === 'live' ? 0 : x.phase === 'upcoming' ? 1 : 2);
      const d = rank(a) - rank(b);
      if (d) return d;
      return (parseLocalTime(a.startedAt) || 0) - (parseLocalTime(b.startedAt) || 0);
    });
  const data = { items, at: Date.now() };
  liveCache = { at: Date.now(), data };
  return data;
}

// 直播代理：m3u8 里的分片改成本地地址，分片直接转发；不缓存（直播要实时）
const liveAgent = new https.Agent({ keepAlive: true, maxSockets: 4, maxFreeSockets: 2, timeout: 20000 });
function proxyLive(res, req, rawUrl) {
  return new Promise((resolve) => {
    const u = new URL(rawUrl);
    const r = https.get(u, {
      headers: { 'User-Agent': UA, Referer: ORIGIN + '/', Origin: ORIGIN, Accept: '*/*' },
      rejectUnauthorized: false, servername: u.hostname, agent: liveAgent,
    }, (up) => {
      const ct = String(up.headers['content-type'] || '');
      if (/mpegurl|m3u8/i.test(ct) || /\.m3u8($|\?)/i.test(u.pathname + u.search)) {
        const c = []; up.on('data', d => c.push(d));
        up.on('end', () => {
          const txt = Buffer.concat(c).toString('utf8');
          const out = txt.split('\n').map(line => {
            const t = line.trim();
            if (!t || t.startsWith('#')) return line;
            try { return '/lp?u=' + enc(new URL(t, u).href); } catch (e) { return line; }
          }).join('\n');
          res.writeHead(200, { 'content-type': 'application/vnd.apple.mpegurl', 'cache-control': 'no-store', 'access-control-allow-origin': '*' });
          res.end(out);
          resolve();
        });
        return;
      }
      res.writeHead(up.statusCode || 200, {
        'content-type': ct || 'video/mp2t',
        'cache-control': 'no-store',
        'access-control-allow-origin': '*',
        'content-length': up.headers['content-length'] || undefined,
      });
      up.pipe(res);
      up.on('end', resolve);
      up.on('error', () => { try { res.end(); } catch (e) {} resolve(); });
    });
    r.on('error', (e) => {
      if (!res.headersSent) { res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' }); res.end('直播流取不到：' + e.message); }
      else { try { res.end(); } catch (x) {} }
      resolve();
    });
    r.setTimeout(20000, () => r.destroy(new Error('直播流超时')));
    req.on('close', () => { try { r.destroy(); } catch (e) {} });
  });
}

// ---------------- 课程数据 ----------------
const courseCache = new Map();

async function buildCourse(id, token) {
  const cRes = await apiCall('/v1/course?id=' + encodeURIComponent(id), token);
  if (!cRes || cRes.code !== 0 || !cRes.data) {
    return { ok: false, error: (cRes && cRes.message) || '课程不存在或无权访问', code: cRes && cRes.code };
  }
  const c = cRes.data;

  // 注意：v2 的 session/list 对多数账号返回空数组，v1 才带 videos
  const raw = [];
  for (let page = 1; page <= 20; page++) {
    const j = await apiCall(`/v1/course/session/list?course_id=${c.id}&order_type=asc&page=${page}&page_size=100`, token);
    const arr = j && Array.isArray(j.data) ? j.data : (j && j.data && j.data.data) || [];
    if (!arr.length) break;
    raw.push(...arr);
    if (arr.length < 100) break;
  }

  const sessions = [];
  let unresolved = 0;
  for (const s of raw) {
    let v = (s.videos && s.videos[0]) || null;
    const ids = Array.isArray(s.video_ids) ? s.video_ids : (s.video_ids ? [s.video_ids] : []);
    if (!(v && (v.main || v.vga)) && ids.length) {
      // 一次课可能有多个视频记录（全景 / 课件 / 教室），一个不行就试下一个，
      // 以前只看 video_ids[0]，正好碰到没有 main 的那条就会误判成"没有录像"
      for (const vid of ids.slice(0, 3)) {
        try {
          const r = await apiCall(`/v1/video?id=${vid}&course_id=${c.id}`, token);
          if (r && r.code === 0 && r.data) {
            const cand = r.data;
            if (cand.main || cand.vga) { v = cand; break; }
            if (!v) v = cand;
          }
        } catch (e) {}
      }
    }
    if ((!v || !(v.main || v.vga)) && ids.length) unresolved++;
    sessions.push({
      id: s.id, title: s.section_group_title || s.title || '', week: s.week_number || null,
      date: (s.started_at || '').slice(0, 10), started_at: s.started_at || null,
      location: s.location || '', duration: v && v.duration ? Number(v.duration) : null,
      main: (v && v.main) || null, vga: (v && v.vga) || null,
    });
  }
  sessions.sort((a, b) => String(b.started_at || '').localeCompare(String(a.started_at || '')));

  return {
    ok: true,
    course: {
      id: c.id, name: c.name_zh, code: c.code, college: c.college_name,
      year: c.school_year, semester: c.semester,
      professors: (c.professors || []).map(p => p.name || p).slice(0, 5),
    },
    sessions,
    stats: { total: sessions.length, playable: sessions.filter(s => s.main || s.vga).length, unresolved },
  };
}

async function getCourse(id, token, { force = false } = {}) {
  const key = token + ':' + id;
  const hit = courseCache.get(key);
  if (!force && hit && Date.now() - hit.at < 60000) return hit.data;
  const data = await buildCourse(id, token);
  if (courseCache.size > 300) courseCache.clear();
  courseCache.set(key, { at: Date.now(), data });
  return data;
}

// ---------------- HLS 代理 ----------------
function abs(u, base) {
  if (/^https?:\/\//i.test(u)) return u;
  if (u.startsWith('/')) return new URL(u, 'https://cvideo.yanhekt.cn').href;
  return base + '/' + u;
}
const enc = s => Buffer.from(s, 'utf8').toString('base64url');
const dec = s => Buffer.from(s, 'base64url').toString('utf8');

async function proxyHls(res, req, rawUrl) {
  const token = tokenOf(req);
  const user = await verifyToken(token);
  if (!user) { res.writeHead(401, { 'content-type': 'text/plain; charset=utf-8' }); return res.end('need login'); }

  const isPlaylist = rawUrl.split('?')[0].endsWith('.m3u8');

  if (isPlaylist) {
    const p = playlistCache.get(rawUrl);
    if (p && Date.now() - p.at < 60000) {
      res.writeHead(200, { 'content-type': 'application/vnd.apple.mpegurl', 'cache-control': 'no-store' });
      return res.end(p.text);
    }
  } else {
    const hit = cacheGet(rawUrl);
    if (hit) {
      res.writeHead(200, {
        'content-type': 'video/mp2t', 'content-length': hit.length,
        'cache-control': 'public, max-age=86400, immutable',
      });
      return res.end(hit);
    }
  }

  const vid = await videoToken(token, user.badge);

  let up;
  try { up = await upstreamGet(signer.sign(rawUrl, vid)); }
  catch (e) {
    res.writeHead(504, { 'content-type': 'text/plain; charset=utf-8' });
    return res.end('upstream error: ' + e.message);
  }
  if (!up || up.statusCode !== 200) {
    const code = up ? up.statusCode : 504;
    res.writeHead(code, { 'content-type': 'text/plain; charset=utf-8' });
    return res.end('CDN ' + code);
  }

  if (isPlaylist) {
    const chunks = [];
    up.on('data', d => chunks.push(d));
    up.on('end', () => {
      const text = Buffer.concat(chunks).toString('utf8');
      const base = rawUrl.slice(0, rawUrl.lastIndexOf('/'));
      const out = text.split('\n').map(line => {
        const t = line.trim();
        if (!t) return line;
        if (t.startsWith('#')) return line.replace(/URI="([^"]+)"/g, (_m, u) => `URI="/p?u=${enc(abs(u, base))}"`);
        return `/p?u=${enc(abs(t, base))}`;
      }).join('\n');
      if (playlistCache.size > 40) playlistCache.clear();
      playlistCache.set(rawUrl, { at: Date.now(), text: out });
      res.writeHead(200, { 'content-type': 'application/vnd.apple.mpegurl', 'cache-control': 'no-store' });
      res.end(out);
    });
    up.on('error', () => { try { res.end(); } catch (e) {} });
    return;
  }

  // 分片：边转发边攒进缓存；浏览器一断（拖动会 abort）就掐断上游，
  // 免得那条唯一的连接还被旧分片占着，拖动要白等一秒多。
  let clientGone = false;
  res.on('close', () => {
    if (!res.writableEnded) {
      clientGone = true;
      try { up.destroy(); } catch (e) {}
    }
  });

  const chunks = [];
  up.on('data', d => chunks.push(d));
  up.on('end', () => { if (!clientGone) { try { cachePut(rawUrl, Buffer.concat(chunks)); } catch (e) {} } });

  res.writeHead(200, {
    'content-type': up.headers['content-type'] || 'video/mp2t',
    'cache-control': 'public, max-age=86400, immutable',
  });
  up.pipe(res);
  up.on('error', () => { try { res.end(); } catch (e) {} });
}

// ---------------- 下载（把一个讲次拼成单个文件） ----------------
function readAll(stream) {
  return new Promise((resolve, reject) => {
    const c = [];
    stream.on('data', d => c.push(d));
    stream.on('end', () => resolve(Buffer.concat(c)));
    stream.on('error', reject);
  });
}

/** 取一个播放列表（如果是"多码率总表"就顺着往下找到真正的分片列表） */
async function resolveMedia(src, vid) {
  let url = src, text = '', base = '';
  for (let guard = 0; guard < 4; guard++) {
    const up = await upstreamGet(signer.sign(url, vid));
    if (!up || up.statusCode !== 200) {
      throw new Error('取播放列表失败' + (up ? '（CDN ' + up.statusCode + '）' : ''));
    }
    text = (await readAll(up)).toString('utf8');
    base = url.slice(0, url.lastIndexOf('/'));
    if (!/#EXT-X-STREAM-INF:/i.test(text)) break;
    const variant = text.split('\n').map(l => l.trim()).find(l => l && !l.startsWith('#'));
    if (!variant) throw new Error('播放列表里找不到可用的清晰度');
    url = abs(variant, base);
  }
  const lines = text.split('\n').map(l => l.trim()).filter(l => l && !l.startsWith('#'));
  const mapUri = (text.match(/#EXT-X-MAP:[^\n]*?URI="([^"]+)"/i) || [])[1];
  const dur = [...text.matchAll(/#EXTINF:\s*([\d.]+)/g)].reduce((a, m) => a + parseFloat(m[1]), 0);
  return {
    segments: lines.map(l => abs(l, base)),
    mapUri: mapUri ? abs(mapUri, base) : null,
    duration: dur,
    encrypted: /#EXT-X-KEY:[^\n]*METHOD=(?!NONE)/i.test(text),
    endlist: /#EXT-X-ENDLIST/i.test(text),
    mediaSequence: (text.match(/#EXT-X-MEDIA-SEQUENCE:\s*(\d+)/i) || [])[1] || null,
    raw: text,
  };
}

/** 拉一个分片，失败重试；实在不行就放弃（返回 null），不要因为一片就中止整个下载 */
async function fetchSegment(url, vid, tries = 5) {
  const cached = cacheGet(url);
  if (cached) return cached;
  for (let i = 0; i < tries; i++) {
    let up = null;
    try {
      up = await upstreamGet(signer.sign(url, vid), 1);
      if (up && up.statusCode === 200) {
        const buf = await readAll(up);
        try { cachePut(url, buf); } catch (e) {}
        return buf;
      }
      if (up && up.statusCode === 404) { try { up.resume(); } catch (e) {} return null; }  // 404 重试没意义
      // 403 / 429 多半是 CDN 嫌请求太密 → 退得久一点再试
      if (up && (up.statusCode === 403 || up.statusCode === 429)) {
        try { up.resume(); } catch (e) {}
        await sleep(1200 * (i + 1));
        continue;
      }
      try { if (up) up.resume(); } catch (e) {}
    } catch (e) { /* 网络抖动，重试 */ }
    if (i < tries - 1) await sleep(300 * (i + 1));
  }
  return null;
}

/**
 * 课程录像是 HLS（一节课被切成几百个分片），浏览器里没法右键另存。
 * 这里按顺序把分片拉下来、边拉边写回浏览器，得到一个能直接播放的整文件。
 *
 * 两个坑：
 *  1. 分片多（两小时的课 700+ 片），中间偶发失败很常见 —— 必须重试 + 跳过，
 *     以前是"失败就 break"，结果两小时的课只能下出前 30 分钟
 *  2. 播放列表可能是多码率总表，要先顺着 #EXT-X-STREAM-INF 找到真正的分片列表
 */
async function downloadSession(res, req, opt, token, user) {
  const fail = (code, msg) => {
    if (!res.headersSent) res.writeHead(code, { 'content-type': 'text/plain; charset=utf-8' });
    try { res.end(msg); } catch (e) {}
  };
  try {
    const d = await getCourse(opt.course, token);
    if (!d || !d.ok) return fail(404, '读取课程失败：' + ((d && d.error) || ''));
    const s = (d.sessions || []).find(x => String(x.id) === String(opt.session));
    if (!s) return fail(404, '找不到这一讲');
    const src = opt.kind === 'vga' ? s.vga : s.main;
    if (!src) return fail(404, '这一讲没有' + (opt.kind === 'vga' ? '课件' : '全景') + '画面');

    const vid = await videoToken(token, user.badge);
    const media = await resolveMedia(src, vid);

    // 诊断模式：不下文件，只把这一讲的结构报出来（片数、时长、是否加密……）
    if (opt.probe) {
      return json(res, 200, {
        ok: true,
        session: { id: s.id, date: s.date, title: s.title, duration: s.duration },
        segments: media.segments.length,
        totalDurationSec: Math.round(media.duration),
        segmentDurationSec: media.segments.length ? +(media.duration / media.segments.length).toFixed(2) : null,
        encrypted: media.encrypted,
        hasEndList: media.endlist,
        mediaSequence: media.mediaSequence,
        initSegment: media.mapUri ? 'yes' : 'no',
        firstSegment: media.segments[0] || null,
        lastSegment: media.segments[media.segments.length - 1] || null,
      });
    }

    if (!media.segments.length) return fail(502, '播放列表里没有分片');
    if (media.encrypted) return fail(501, '这批录像用了加密的播放列表，暂时不支持下载');

    const order = media.mapUri ? [media.mapUri, ...media.segments] : media.segments;
    const title = `${d.course.name}_${(s.date || '')}_${opt.kind === 'vga' ? '课件' : '全景'}`
      .replace(/[\\/:*?"<>|\r\n\t]/g, '_').slice(0, 80);
    const tsLike = !/\.(m4s|mp4)(\?|$)/i.test(order[0] || '');
    const ext = tsLike ? '.ts' : '.mp4';

    console.log('下载开始：' + title + '，共 ' + media.segments.length + ' 片，约 '
      + Math.round(media.duration / 60) + ' 分钟');

    let gone = false;
    res.on('close', () => { if (!res.writableEnded) gone = true; });

    res.writeHead(200, {
      'content-type': tsLike ? 'video/mp2t' : 'video/mp4',
      'content-disposition': `attachment; filename="lecture${ext}"; filename*=UTF-8''${encodeURIComponent(title + ext)}`,
      'cache-control': 'no-store',
    });

    let doneSegs = 0, failedSegs = 0;
    for (let i = 0; i < order.length; i++) {
      if (gone) break;
      const buf = await fetchSegment(order[i], vid);
      if (!buf) {
        failedSegs++;
        if (i === 0) break;          // 第一片就取不到：多半是地址/权限问题，别硬下
        continue;                     // 中间断一片：跳过，接着下
      }
      doneSegs++;
      try { res.write(buf); } catch (e) { break; }
      if ((i + 1) % 100 === 0) console.log('  下载进度 ' + (i + 1) + '/' + order.length);
      if ((i + 1) % 10 === 0) await sleep(200);   // 稍微放慢，别把 CDN 惹急
    }
    try { res.end(); } catch (e) {}
    console.log('下载结束：成功 ' + doneSegs + ' 片，失败 ' + failedSegs + ' 片');
  } catch (e) {
    fail(500, '下载出错：' + e.message);
  }
}

function termKeyOf(s) {
  const t = String(s || '');
  const y = t.match(/(\d{4})\s*[-–—~]\s*(\d{4})/);
  const sem = /第一|第1|秋季|秋学期/.test(t) ? 1
    : /第二|第2|春季|春学期/.test(t) ? 2
    : /第三|第3|夏季|夏学期/.test(t) ? 3 : 0;
  return (y ? y[1] + '-' + y[2] : '') + '|' + sem;
}

/** 这门课的学期大概什么时候结束（用来判断"是不是还在进行中"） */
function termEndDate(termStr) {
  const t = parseTermName(termStr);
  if (!t) {
    const m = String(termStr || '').match(/(20\d{2})\s*[-–—~]\s*(20\d{2})/);
    return m ? new Date(`${m[2]}-09-01`) : null;
  }
  const yEnd = Number(t.year.slice(5, 9));
  const end = t.sem === 1 ? `${yEnd}-02-15` : t.sem === 2 ? `${yEnd}-07-20` : `${yEnd}-09-15`;
  return new Date(end);
}

/**
 * 学期是不是"近的"：本学期的课，老平台上没录像很常见（还在传 / 只在讲台上录），
 * 这种不能当成"没有录像"藏掉，否则就是"明明有课却搜不到"。
 */
function termIsRecent(termStr, slackDays = 100) {
  const end = termEndDate(termStr);
  if (!end || isNaN(end.getTime())) return true;   // 学期不明 → 宁可不藏
  return end.getTime() + slackDays * 864e5 > Date.now();
}
/** 搜一批课（只搜列表，不探"有没有录像"）。同一关键词的结果缓存 5 分钟，翻页时不用重跑 */
const searchRawCache = new Map();
async function searchRaw(q, term, { token, user, limit, page = 1, notes = [] } = {}) {
  const key = 's|' + q + '|' + term + '|' + limit;
  const hit = searchRawCache.get(key);
  if (hit && Date.now() - hit.at < 5 * 60 * 1000) return hit.data;

  const want = parseTermName(term);
  const norm = s => String(s || '').replace(/\s+/g, '');
  let courses = [];
  let total = 0;
  let src = 'api';

  // 1) 老平台服务端检索（课程名 / 课程编号；带 classrooms 真教室名）
  try {
    let semesterIds = [];
    if (term) {
      const terms = await oldSemesters(token).catch(() => []);
      const hit = terms.find(t => norm(t.name) === norm(term));
      if (hit) semesterIds = [hit.id];
    }

    if (want && !semesterIds.length) {
      // 学期表里没有 semesters 这项（有的部署就是这样）→ 客户端按学年/学期筛，最多翻 4 页
      const collected = [];
      const seen = new Set();
      for (let p = 1; p <= 4; p++) {
        const r = await oldSearch({ keyword: q, page: p, pageSize: 50 });
        const rows = r.list.filter(c => courseInTerm(c, want));
        for (const c of rows) {
          const row = oldCourseRow(c);
          if (row && !seen.has(row.id)) { seen.add(row.id); collected.push(row); }
        }
        total = r.total;
        if (collected.length >= limit) break;
        if (!rows.length && p >= 2) break;   // 已经是更早的学期了，别再翻
      }
      courses = collected.slice(0, limit);
      if (!courses.length) {
        // 还是空 → 用内置课程库按学期挑（**全都给出来**，以前只给 12 门，
        // 搜"数学分析"会漏掉一大批有录像的课）。前 10 门反查一下拿教室，剩下的由"探节次"顺手补
        const cat = catalogSearch(q, term, limit);
        courses = cat.courses.map(mc => ({ ...mc, room: '', source: 'catalog' }));
        total = Math.max(total, cat.total || courses.length);
        await Promise.all(courses.slice(0, 10).map(async (row) => {
          try {
            const r = oldCourseRow(await lookupCourse(row.id));
            if (!r) return;
            if (r.room) row.room = r.room;
            if (Array.isArray(r.rooms) && r.rooms.length) row.rooms = r.rooms;
            if (r.roomByNumber) row.roomByNumber = r.roomByNumber;
            if (!row.teacher && r.teacher) row.teacher = r.teacher;
            if (!row.term && r.term) row.term = r.term;
          } catch (e) {}
        }));
        src = 'catalog+lookup';
      }
    } else {
      const r = await oldSearch({ keyword: q, semesterIds, page, pageSize: Math.min(limit, 50) });
      courses = r.list.map(oldCourseRow).filter(Boolean);
      total = r.total;
    }
  } catch (e) {
    notes.push('服务端检索失败：' + e.message);
  }

  // 2) 教师名：服务端 keyword 只匹配课程名，所以再用课程库按教师找，反查补齐
  if (courses.length < 5 && q) {
    const byTeacher = catalogByTeacher(q, 20);
    if (byTeacher.length) {
      const have = new Set(courses.map(c => c.id));
      const missing = byTeacher.filter(c => !have.has(c.id)).slice(0, 12);
      const add = [];
      await Promise.all(missing.map(async (mc) => {
        try {
          const full = await lookupCourse(mc.id);
          const row = oldCourseRow(full) || { id: mc.id, name: mc.name, teacher: mc.teacher, term: mc.term, room: '', source: 'catalog' };
          if (!row.teacher) row.teacher = mc.teacher;
          if (!row.term) row.term = mc.term;
          add.push(row);
        } catch (e) {
          add.push({ id: mc.id, name: mc.name, teacher: mc.teacher, term: mc.term, room: '', source: 'catalog' });
        }
      }));
      if (add.length) { courses = courses.concat(add); total = Math.max(total, courses.length); src = 'api+teacher'; }
    }
  }

  // 3) 服务端完全没有结果 → 退回内置课程库（离线）
  if (!courses.length) {
    const r = catalogSearch(q, term, limit);
    courses = r.courses.map(c => ({ ...c, room: '', source: 'catalog' }));
    total = r.total;
    src = 'catalog';
  }


  const data = { courses, total, src };
  if (searchRawCache.size > 60) searchRawCache.clear();
  searchRawCache.set(key, { at: Date.now(), data });
  return data;
}
const Y2_COOKIE = 'y2tok';const Y2_COOKIE_TTL = 24 * 3600;

function y2Token(req) {
  const c = parseCookies(req);
  return c[Y2_COOKIE] || '';
}

/** JWT 里的 exp（秒）；解析不了就返回 0 */
function jwtExp(jwt) {
  try {
    const p = String(jwt || '').split('.')[1] || '';
    const json = Buffer.from(p.replace(/-/g, '+').replace(/_/g, '/'), 'base64').toString('utf8');
    const exp = Number(JSON.parse(json).exp);
    return Number.isFinite(exp) ? exp : 0;
  } catch (e) { return 0; }
}

// JWT -> 身份（含签名用的 phone），只放内存
const y2Identity = new Map();
const y2Probed = new Set();
async function y2Who(jwt) {
  if (!jwt) return null;
  const hit = y2Identity.get(jwt);
  if (hit) return hit;
  try {
    const id = await y2.profile(jwt);
    if (y2Identity.size > 50) y2Identity.clear();
    y2Identity.set(jwt, id);
    return id;
  } catch (e) {
    // 可能是网关的鉴权写法变了（比如只认 cookie）：探测一次，成功就把这种方式记住
    if (y2Probed.has(jwt)) return null;
    y2Probed.add(jwt);
    try {
      const r = await y2.login(jwt);
      if (y2Identity.size > 50) y2Identity.clear();
      y2Identity.set(jwt, r.id);
      return r.id;
    } catch (e2) {
      return null;
    }
  }
}

// 讲次缓存：jwt+courseId -> {at, data}；data.sessions 只含「有录像」的讲次
const y2VideoCache = new Map();
const Y2_TTL = 5 * 60 * 1000;

function y2CacheKey(jwt, courseId) { return jwt.slice(-12) + ':' + courseId; }

// 我的课表缓存：jwt -> Map(courseId -> 讲次[])
const y2SchedCache = new Map();

/** 自己课表里近 150 天的讲次（讲次直读失败时的救命通道，一次覆盖所有课） */
async function y2Schedule(jwt, id) {
  const key = jwt.slice(-12);
  const hit = y2SchedCache.get(key);
  if (hit && Date.now() - hit.at < Y2_TTL) return hit.map;
  const map = await y2.scheduleMap(jwt, id.userId);
  if (y2SchedCache.size > 50) y2SchedCache.clear();
  y2SchedCache.set(key, { at: Date.now(), map });
  return map;
}

/** 课表行里能确定有录像的（只认 status=6，别的宁可待确认） */
function verifiedFromSchedule(list) {
  return (Array.isArray(list) ? list : []).filter(s => s && s.status === '6');
}

// 关键词扫描结果缓存：jwt+关键词+学期窗口 -> Map(courseId -> 讲次[])
const y2ScanCache = new Map();

/** 从一批课程里提炼出要扫的学期窗口（去重，最多 3 个） */
function windowsFor(courses, termNames, max = 3) {
  const seen = new Map();
  for (const c of courses) {
    // 课程条目上没写学期名时，用学期表按 termId 反查
    const name = c.termName || (termNames && termNames.get(String(c.termId))) || '';
    const w = y2.termWindow(name);
    if (!w) continue;
    const key = w.from + '~' + w.to;
    if (!seen.has(key)) seen.set(key, { key, from: w.from, to: w.to, terms: [] });
    if (!seen.get(key).terms.includes(name)) seen.get(key).terms.push(name);
  }
  return [...seen.values()].slice(0, max);
}

/**
 * 按关键词扫这些学期窗口，合并成 Map(courseId -> 讲次[])。
 * 一次搜索只扫一遍，结果是"关键词 → 这些学期里所有课程的讲次"。
 */
async function y2ScanWindows(jwt, q, windows) {
  if (!windows.length) return new Map();
  const cacheKey = jwt.slice(-12) + '|' + q + '|' + windows.map(w => w.key).join(',');
  const hit = y2ScanCache.get(cacheKey);
  if (hit && Date.now() - hit.at < Y2_TTL) return hit.map;

  const map = new Map();
  for (const w of windows) {
    const dates = y2.datesBetween(w.from, w.to, 170);
    if (!dates.length) continue;
    const part = await y2.scanDates(jwt, q, dates, { concurrency: 10, maxEmpty: 30 });
    for (const [k, rows] of part) {
      const cur = map.get(k) || [];
      for (const r of rows) if (!cur.some(x => x.subId === r.subId)) cur.push(r);
      cur.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
      map.set(k, cur);
    }
  }
  if (y2ScanCache.size > 30) y2ScanCache.clear();
  y2ScanCache.set(cacheKey, { at: Date.now(), map });
  return map;
}

// 单讲"到底有没有流"的探测缓存
const y2ProbeCache = new Map();

/** 一讲到底有没有视频流（缓存由 yanhe2 管，按 jwt+subId） */
async function probeStream(jwt, courseId, subId) {
  const r = await y2.sessionHasStream(jwt, courseId, subId);
  return r.ok;
}

/**
 * 从课表行里挑出真正能播的讲次：
 *  - 有 status=6 的，直接就是它们
 *  - 全是 5 / 未知：抽一条实际取流，要到才算（要不到 = 这门课没录像）
 */
/**
 * 从课表行里挑出真正能播的讲次。
 * sub_status=6 **不能**直接当"有录像"用：平台上一门课 5 讲全是 6、实际只有 1 讲有流的
 * 情况很常见，所以这里逐讲真取一次视频流，取到才算。
 * 全部请求都失败（网络/权限）时退回状态判断，别把故障当成"没录像"。
 */
async function pickPlayable(jwt, courseId, rows) {
  const list = Array.isArray(rows) ? rows : [];
  if (!list.length) return [];
  const six = list.filter(r => y2.isRecordingStatus(r.status) || y2.maybeRecording(r.status));
  const cand = six.length ? six : list;
  const v = await y2.verifySessions(jwt, courseId, cand);
  if (v.playable.length || v.failed < v.probed) return v.playable;
  return six;
}

/**
 * 一门课的可看录像（带缓存）。三级来源**依次真试**，任何一级拿到就返回：
 *   1. get-course-detail（最快，但很多账号拿不到）
 *   2. 自己的课表 get-week-schedules（一次覆盖所有课，自带教室）
 *   3. allowScan=true 时，按天扫课表
 * 关键：某一级"成功但 0 条"时**不能就此结束**，要继续试下一级。
 */
async function y2Videos(jwt, id, courseId, { allowScan = true, q = '', termName = '' } = {}) {
  const key = y2CacheKey(jwt, courseId);
  const hit = y2VideoCache.get(key);
  if (hit && Date.now() - hit.at < Y2_TTL) return hit.data;

  const notes = [];
  let detail = null;

  // 1. 讲次直读
  try {
    detail = await y2.courseVideos(jwt, courseId, id.account);
    if (detail.videoCount) {
      // 讲次直读经常只给一部分（常见：5 讲里只有 1 讲是"可播放"状态），
      // 再和自己的课表 / 搜索时扫过的课表合并一次，避免"卡片 5 讲、进去 1 讲"
      const merged = await mergeKnownSessions(jwt, id, courseId, detail.sessions, q, termName);
      const sessions = merged.length ? merged : detail.sessions;
      return y2Remember(key, {
        source: sessions.length > detail.sessions.length ? 'detail+schedule' : 'detail',
        verified: detail.verified !== false, note: '',
        sessions, room: (sessions[0] && sessions[0].room) || detail.room || '',
        videoCount: sessions.length, totalSessions: Math.max(detail.totalSessions, sessions.length),
        termName: detail.termName || '',
      });
    }
    notes.push('讲次直读没给到可播放讲次');
  } catch (e) {
    if (/会话被拒绝/.test(e.message)) throw e;
    notes.push('讲次直读：' + e.message);
  }

  // 2. 自己的课表
  try {
    const map = await y2Schedule(jwt, id);
    const list = map.get(String(courseId));
    const playable = await pickPlayable(jwt, courseId, list);
    if (playable.length) {
      return y2Remember(key, {
        source: 'schedule-table', verified: true, note: notes.join('；'),
        sessions: playable, room: playable[0].room || '',
        videoCount: playable.length, totalSessions: (list || []).length, termName: '',
      });
    }
    if (list && list.length) notes.push('自己的课表里有课，但没有一讲是"可播放"状态');
  } catch (e) {}

  // 3. 按天扫课表（只在用户真的点开这门课时）
  if (allowScan) {
    // 3a. 搜索时扫过的学期窗口 / 这个学期自己扫一遍
    if (q) {
      try {
        const w = y2.termWindow(termName);
        const windows = w ? [{ key: w.from + '~' + w.to, from: w.from, to: w.to }] : [];
        const map = windows.length
          ? await y2ScanWindows(jwt, q, windows)
          : await y2.scanByKeyword(jwt, q, { days: 100, concurrency: 8 });
        const rows = map.get(String(courseId));
        if (rows && rows.length) {
          const list = await pickPlayable(jwt, courseId, rows);
          return y2Remember(key, {
            source: 'schedule-keyword', verified: list.length > 0, note: notes.join('；'),
            sessions: list, room: (list[0] && list[0].room) || '',
            videoCount: list.length, totalSessions: rows.length, termName,
          });
        }
      } catch (e) {}
    }
    // 3b. 单独扫这门课
    try {
      const raw = await y2.scanSessions(jwt, courseId, undefined, { days: 60, maxEmpty: 30 });
      const list = await pickPlayable(jwt, courseId, y2.filterRecordings(raw));
      if (list.length) {
        return y2Remember(key, {
          source: 'schedule', verified: true, note: notes.join('；'),
          sessions: list, room: (list[0] && list[0].room) || '',
          videoCount: list.length, totalSessions: raw.length, termName: '',
        });
      }
      notes.push('按天扫课表也没扫到讲次');
    } catch (e) { notes.push('按天扫课表：' + e.message); }
  }

  return y2Remember(key, {
    source: 'none', verified: false, note: notes.join('；'),
    sessions: [], room: '',
    videoCount: 0, totalSessions: detail ? detail.totalSessions : 0, termName: '',
  });
}

function y2Remember(key, data) {
  if (y2VideoCache.size > 200) y2VideoCache.clear();
  y2VideoCache.set(key, { at: Date.now(), data });
  return data;
}

/** 只从"关键词扫描"缓存里取结果；没扫过就返回 null（不现场扫） */
function y2ScanCached(jwt, q, termName) {
  if (!q) return null;
  const tail = String(jwt).slice(-12) + '|' + q + '|';
  const w = y2.termWindow(termName);
  const want = w ? tail + (w.from + '~' + w.to) : '';
  let hit = null;
  for (const [k, v] of y2ScanCache) {
    if (!k.startsWith(tail)) continue;
    if (Date.now() - v.at >= Y2_TTL) continue;
    if (want && !k.includes(want)) continue;
    hit = v.map;
    break;
  }
  return hit;
}

/**
 * 把"讲次直读"的结果和课表里已知的讲次合并，再逐讲验流。
 *
 * 为什么需要：讲次直读经常只返回一部分 —— 实测一门 5 讲的课只有 1 讲是
 * `sub_status=6`，其余 4 讲是 5 或空，于是旧逻辑只认那 1 讲，卡片写 5 讲、
 * 点进去只剩 1 讲。课表（自己的课表 / 搜索时扫过的课表）里 5 讲都在，
 * 合并后再验流，两边就一致了。
 */
async function mergeKnownSessions(jwt, id, courseId, sessions, q, termName) {
  const base = Array.isArray(sessions) ? sessions : [];
  const seen = new Set(base.map(s => String(s.subId)));
  const extra = [];
  const add = (rows) => {
    for (const r of rows || []) {
      const k = String(r.subId || '');
      if (!k || seen.has(k)) continue;
      seen.add(k);
      extra.push(r);
    }
  };
  try { add((await y2Schedule(jwt, id)).get(String(courseId))); } catch (e) {}
  // 搜索时已经扫过的课表直接拿来用（缓存里有就用）
  if (!extra.length && q) {
    try {
      const cached = y2ScanCached(jwt, q, termName);
      if (cached) add(cached.get(String(courseId)));
    } catch (e) {}
  }
  // 直读只给了一两讲（典型：5 讲里只有 1 讲是可播放状态）→ 单独把这门课的课表扫一遍
  if (!extra.length && base.length < 2) {
    try {
      const raw = await y2.scanSessions(jwt, courseId, undefined, { days: 90, maxEmpty: 20 });
      add(y2.filterRecordings(raw));
    } catch (e) {}
  }
  if (!extra.length) return base;
  const v = await y2.verifySessions(jwt, courseId, extra);
  if (!v.playable.length) return base;
  const out = base.concat(v.playable);
  out.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
  return out;
}

/** 并发跑一批任务（上游对并发不敏感，6 路足够快） */
async function pool(items, limit, worker) {
  const queue = items.slice();
  const run = async () => { while (queue.length) await worker(queue.shift()); };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, run));
}

/**
 * 搜索结果的"有没有录像"校验 + 带教室名。
 *
 * 证据优先级（哪条先给出结论就用哪条）：
 *  1. **按关键词扫一遍课表**（一次覆盖列表里所有课程，`sub_status`、`room_name` 都在里面）
 *     - 有 status=6 的讲次 → 有录像，教室直接有了
 *     - 有讲次但一条都不是可播状态 → 抽最新一条**真的要一次视频流**：要不到就判定没录像
 *  2. 课表里扫不到的课（往届课程等）→ 走 detail / 自己的课表；都不行就标「待确认」
 *
 * 总保险：如果一轮下来全部被藏掉，说明判定出了系统性偏差，这时一门都不藏。
 */
/** 课表扫描用的关键词：多词查询取最长的一段（通常是课程名） */
function scanKeyword(q) {
  const t = String(q || '').split(/\s+/).map(x => x.trim()).filter(Boolean);
  if (!t.length) return '';
  return t.slice().sort((a, b) => b.length - a.length)[0];
}

async function y2Verify(jwt, id, courses, { q = '', scanQ = '' } = {}) {
  const kw = scanQ || scanKeyword(q) || q;
  const kept = [];
  let hidden = 0, unverified = 0;

  // 1. 按结果里出现的学期，各自扫一遍课表（一次搜索最多 3 个学期）
  let termNames = new Map();
  try {
    const terms = await y2.termList(jwt);
    for (const t of terms) termNames.set(String(t.id), t.name);
  } catch (e) {}
  const windows = windowsFor(courses, termNames, 2);
  let scan = null;
  if (kw && windows.length) {
    try { scan = await y2ScanWindows(jwt, kw, windows); }
    catch (e) { if (/会话被拒绝/.test(e.message)) throw e; }
  } else if (kw) {
    // 学期名解析不出来 → 退回"近 100 天"
    try { scan = await y2.scanByKeyword(jwt, kw, { days: 100, concurrency: 8 }); }
    catch (e) { if (/会话被拒绝/.test(e.message)) throw e; }
  }

  const pending = [];
  for (const c of courses) {
    if (c.sessions === 0) { hidden++; continue; }   // 课程库明确说 0 讲
    const rows = scan ? (scan.get(String(c.courseId)) || []) : [];
    pending.push({ c, rows });
  }

  // 2. 剩下的并行确认
  await pool(pending, 6, async ({ c, rows }) => {
    const name = c.termName || termNames.get(String(c.termId)) || '';
    const w = y2.termWindow(name);
    const scannedThisTerm = !!(scan && w && windows.some(x => x.from === w.from && x.to === w.to));

    if (rows.length) {
      // sub_status=6 不等于"真有流"：一门课 5 讲全是 6、实际只有 1 讲有流的情况很常见。
      // 所以逐讲真取一次流：讲次少的课全验（卡片上的数字就是准确讲数），
      // 讲次多的只抽最新 5 讲验，卡片上只标"有录像"不写数字。
      const six = rows.filter(r => y2.isRecordingStatus(r.status) || y2.maybeRecording(r.status));
      const cand = six.length ? six : rows;
      const exact = cand.length <= 12;
      const v = await y2.verifySessions(jwt, c.courseId, cand, { limit: exact ? 0 : 3 });
      if (v.playable.length) {
        kept.push({
          ...c, verified: true, totalSessions: rows.length, source: 'scan',
          videoCount: exact ? v.playable.length : null,
          room: v.playable[0].room || cand[0].room || '',
        });
        return;
      }
      if (v.failed === v.probed && v.probed > 0) {   // 请求全失败 → 不能当成没录像
        unverified++;
        kept.push({ ...c, verified: false, note: '取视频流超时，未能确认' });
        return;
      }
      hidden++;                                       // 验过的讲次一条流都没有
      return;
    }
    // 扫不到讲次不等于没录像：常见原因是这门课不在你的课表里（没选它）。
    // 继续往下走 detail / 自己的课表确认，别在这里直接藏掉。

    // 学期窗口没扫到（往届课程 / 学期名认不出来）→ 试 detail / 自己的课表
    try {
      const v = await y2Videos(jwt, id, c.courseId, { allowScan: false });
      if (v.videoCount) {
        kept.push({ ...c, verified: true, videoCount: v.videoCount, totalSessions: v.totalSessions, room: v.room || '', source: v.source });
        return;
      }
      if (v.totalSessions > 0) { hidden++; return; }   // 有讲次，但一讲能播的都没有
    } catch (e) {}
    unverified++;
    kept.push({ ...c, verified: false, note: '这个学期的时间窗没扫到讲次，接口也没给' });
  });

  const order = new Map(courses.map((c, i) => [c.courseId, i]));
  kept.sort((a, b) => {
    const d = searchScore(b, q) - searchScore(a, q);
    if (d) return d;
    const vc = (b.videoCount || 0) - (a.videoCount || 0);
    if (vc) return vc;
    return (order.get(a.courseId) || 0) - (order.get(b.courseId) || 0);
  });

  return { courses: kept, hidden, unverified, fallback: false };
}

async function y2DownloadHls(res, req, streamUrl, name) {
  const hp = String(req.headers.host || '').split(':')[1] || String(PORT);
  const base = 'http://127.0.0.1:' + hp;
  const cookie = req.headers.cookie || '';
  const get = (url) => new Promise((resolve, reject) => {
    const u = new URL(url);
    const r = http.request({
      host: u.hostname, port: u.port, path: u.pathname + u.search, method: 'GET',
      headers: cookie ? { cookie } : {},
    }, (up) => {
      const chunks = [];
      up.on('data', d => chunks.push(d));
      up.on('end', () => resolve({ status: up.statusCode, buf: Buffer.concat(chunks) }));
    });
    r.on('error', reject);
    r.setTimeout(90000, () => r.destroy(new Error('timeout')));
    r.end();
  });

  // 全部走本地 /y2play 代理：签名、地址改写、上游行为都和在线播放一模一样
  const readPlaylist = async (url) => {
    const { status, buf } = await get(url);
    if (status !== 200) throw new Error('播放列表 HTTP ' + status);
    return buf.toString('utf8');
  };
  const absLocal = (u) => (u.startsWith('http') ? u : base + u);

  let text = await readPlaylist(base + '/y2play?u=' + enc(streamUrl));

  // 多码率总表：挑第一路子列表
  if (/#EXT-X-STREAM-INF/i.test(text)) {
    const child = text.split('\n').map(l => l.trim()).find(l => l && !l.startsWith('#'));
    if (!child) throw new Error('主播放列表里没有子列表');
    text = await readPlaylist(absLocal(child));
  }

  if (/#EXT-X-KEY/i.test(text) && /METHOD=AES-128/i.test(text)) {
    throw new Error('这讲的播放列表是 AES-128 加密的，暂时不能离线拼片');
  }

  let init = '';
  const segs = [];
  for (const line of text.split('\n')) {
    const t = line.trim();
    if (!t) continue;
    if (t.startsWith('#EXT-X-MAP')) {
      const m = /URI="([^"]+)"/.exec(t);
      if (m) init = absLocal(m[1]);
      continue;
    }
    if (t.startsWith('#')) continue;
    segs.push(absLocal(t));
  }
  if (!segs.length) throw new Error('播放列表里没有分片');

  const order = init ? [init, ...segs] : segs;

  // 先取第一片：取不到就直接报错，别给用户一个空文件
  const first = await get(order[0]);
  if (first.status !== 200 || !first.buf.length) {
    throw new Error('第一片取不到（HTTP ' + first.status + '）');
  }

  res.writeHead(200, {
    'content-type': 'video/mp2t',
    'content-disposition': 'attachment; filename="lecture.ts"; filename*=UTF-8\'\'' + encodeURIComponent(name + '.ts'),
    'cache-control': 'no-store',
  });
  res.write(first.buf);

  let done = 1, failed = 0;
  for (let i = 1; i < order.length; i++) {
    let got = null;
    for (let n = 0; n < 4 && !got; n++) {
      try {
        const g = await get(order[i]);
        if (g.status === 200 && g.buf.length) got = g;
      } catch (e) {}
      if (!got) await sleep(300 * (n + 1));
    }
    if (!got) { failed++; continue; }
    done++;
    try { res.write(got.buf); } catch (e) { break; }
    if (done % 10 === 0) await sleep(100);
  }
  try { res.end(); } catch (e) {}
  console.log('新版下载结束：成功 ' + done + ' 片，失败 ' + failed + ' 片');
}

/** /play/ 转发：透传 Range；m3u8 会把分片地址改回本代理（每片现签） */
async function y2ProxyPlay(res, req, rawUrl, id, opts = {}) {
  const target = y2.signedUrl(rawUrl, id);
  const headers = { 'User-Agent': y2.UA, Referer: y2.Y2 + '/', Accept: '*/*' };
  // 下载模式下不要 Range，要整份文件
  if (!opts.filename && req.headers.range) headers.Range = req.headers.range;

  const up = await new Promise((resolve, reject) => {
    const u = new URL(target);
    const r = https.request(u, { method: 'GET', headers, rejectUnauthorized: false, servername: u.hostname }, resolve);
    r.on('error', reject);
    r.setTimeout(60000, () => r.destroy(new Error('play 超时')));
    r.end();
  });

  const isPlaylist = /\.m3u8$/i.test(rawUrl.split('?')[0]);
  if (isPlaylist && up.statusCode === 200) {
    const chunks = [];
    up.on('data', d => chunks.push(d));
    up.on('end', () => {
      const text = Buffer.concat(chunks).toString('utf8');
      const base = rawUrl.slice(0, rawUrl.lastIndexOf('/') + 1);
      const out = text.split('\n').map(line => {
        const t = line.trim();
        if (!t) return line;
        if (t.startsWith('#')) return line.replace(/URI="([^"]+)"/g, (_m, x) => `URI="/y2play?u=${enc(abs(x, base))}"`);
        return `/y2play?u=${enc(abs(t, base))}`;
      }).join('\n');
      res.writeHead(200, { 'content-type': 'application/vnd.apple.mpegurl', 'cache-control': 'no-store' });
      res.end(out);
    });
    up.on('error', () => { try { res.end(); } catch (e) {} });
    return;
  }

  const pass = ['content-type', 'content-length', 'content-range', 'accept-ranges', 'last-modified', 'etag'];
  const out = {};
  for (const k of pass) if (up.headers[k]) out[k] = up.headers[k];
  out['cache-control'] = 'public, max-age=3600';
  if (opts.filename) {
    delete out['content-range'];
    delete out['accept-ranges'];
    out['content-type'] = up.headers['content-type'] || 'video/mp4';
    out['content-disposition'] = `attachment; filename="lecture${opts.ext || ''}"; filename*=UTF-8''${encodeURIComponent(opts.filename)}`;
    out['cache-control'] = 'no-store';
  }
  res.writeHead(up.statusCode || 200, out);
  up.pipe(res);
  res.on('close', () => { try { up.destroy(); } catch (e) {} });
}

/** 新版里找同名同学期的课（没有学期就只按名字） */
/** 学期名 → "起止年|第几学期"，两个平台的写法（学年/秋季学期）都能对上 */


const y2MatchCache = new Map();
async function y2Match(jwt, name, term) {
  const norm = s => String(s || '').replace(/\s+/g, '').replace(/（/g, '(').replace(/）/g, ')').toLowerCase();
  const key = jwt.slice(-12) + '|' + norm(name) + '|' + termKeyOf(term);
  const hit = y2MatchCache.get(key);
  if (hit !== undefined && Date.now() - hit.at < 10 * 60 * 1000) return hit.id;
  let result = null;
  try {
    const r = await y2.searchCourses(jwt, { q: name, perPage: 40 });
    const list = r.courses || [];
    const sameName = list.filter(c => norm(c.title) === norm(name));
    if (!sameName.length) {
      // 名字不完全一样时，只接受"一边包含另一边"且唯一的那个
      const loose = list.filter(c => {
        const a = norm(c.title), b = norm(name);
        return a && b && (a.includes(b) || b.includes(a));
      });
      if (loose.length === 1) result = loose[0].courseId;
    } else if (term) {
      const want = termKeyOf(term);
      const t = sameName.find(c => c.termName && termKeyOf(c.termName) === want);
      result = t ? t.courseId : (sameName.length === 1 ? sameName[0].courseId : null);
    } else {
      result = sameName.length === 1 ? sameName[0].courseId : null;
    }
  } catch (e) { result = null; }
  if (y2MatchCache.size > 400) y2MatchCache.clear();
  y2MatchCache.set(key, { at: Date.now(), id: result });
  return result;
}

/**
 * 这门课在新版平台上到底存不存在 / 有没有录像。
 * 搜索结果里"老平台没录像"的课，必须先问过新版才能下"没有"的结论。
 */
const y2RescueCache = new Map();
async function y2Rescue(jwt, c) {
  if (!jwt || !c) return null;
  const key = jwt.slice(-12) + '|' + String(c.id) + '|' + termKeyOf(c.term);
  const hit = y2RescueCache.get(key);
  if (hit && Date.now() - hit.at < 10 * 60 * 1000) return hit.data;
  let data = null;
  try {
    const idn = await y2Who(jwt);
    if (idn) {
      let y2id = c.y2CourseId || await y2Match(jwt, c.name || c.title, c.term || '');
      // 名字对不上时，只有"新近学期"才值得再按教师找一次（每次要多发一个请求）
      if (!y2id && termIsRecent(c.term)) {
        const t = String(c.teacher || '').split(/[、,，\/]/)[0].trim();
        if (t) y2id = await y2MatchByTeacher(jwt, c.name || c.title, t, c.term || '');
      }
      if (y2id) {
        // 只取讲次清单，不扫课表：搜索要快。点开课程时 /api/open 会再深挖
        let videoCount = null, room = '';
        try {
          const v = await y2Videos(jwt, idn, y2id, { allowScan: false });
          if (v && v.videoCount) { videoCount = v.videoCount; room = v.room || ''; }
        } catch (e) {}
        data = { y2Id: y2id, videoCount, room };
      }
    }
  } catch (e) { data = null; }
  if (y2RescueCache.size > 400) y2RescueCache.clear();
  y2RescueCache.set(key, { at: Date.now(), data });
  return data;
}

/**
 * 名字对不上时的第二招：按教师搜，再从结果里挑"标题像这门课"且学期对的那个。
 * 只接受唯一候选，宁可不合并，也不要把 A 课的录像挂到 B 课下面。
 */
async function y2MatchByTeacher(jwt, name, teacher, term) {
  const norm = s => String(s || '').replace(/\s+/g, '').replace(/（/g, '(').replace(/）/g, ')').toLowerCase();
  if (!jwt || !teacher) return null;
  try {
    const r = await y2.searchCourses(jwt, { q: teacher, perPage: 40 });
    let list = r.courses || [];
    if (term) {
      const want = termKeyOf(term);
      const sameTerm = list.filter(c => termKeyOf(c.termName) === want);
      if (sameTerm.length) list = sameTerm;
    }
    const a = norm(name);
    const like = list.filter(c => {
      const b = norm(c.title);
      return b && a && (b === a || a.includes(b) || b.includes(a));
    });
    if (like.length === 1) return like[0].courseId;
    // 多个候选但只有一个带了这门课的字样 → 取最像的那个
    if (like.length > 1) {
      const best = like.reduce((x, y) => (norm(y.title).length > norm(x.title).length ? y : x));
      if (like.filter(c => norm(c.title) === norm(best.title)).length === 1) return best.courseId;
    }
    return null;
  } catch (e) {
    return null;
  }
}



// ---------------- 路由 ----------------
const server = http.createServer(async (req, res) => {
  const u = new URL(req.url, `http://${req.headers.host || HOST + ':' + PORT}`);
  try {
    // 主页面：课程库 + 老平台（覆盖 2004 年至今全部课程）
    if (u.pathname === '/' || u.pathname === '/old' || u.pathname === '/index.html') {
      const buf = loadAsset('index.html');
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
      return res.end(buf);
    }
    if (u.pathname === '/hls.min.js') {
      const buf = loadAsset('hls.min.js');
      res.writeHead(200, { 'content-type': 'application/javascript; charset=utf-8', 'cache-control': 'public, max-age=86400' });
      return res.end(buf);
    }

    if (u.pathname === '/y2' || u.pathname === '/y2.html') {
      // 新版平台单独一个页面：手动进、单独登录，只用来找/看新学期的课（那边快 3~5 倍）
      const buf = loadAsset('y2.html');
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
      return res.end(buf);
    }

    // ---- Yanhe 2.0 ----
    if (u.pathname === '/api/y2/status') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      return json(res, 200, { loggedIn: !!id, userId: id ? id.userId : null, expiresAt: id ? jwtExp(jwt) : 0 });
    }

    if (u.pathname === '/api/y2/login') {
      if (req.method !== 'POST') return json(res, 405, { ok: false, error: 'method not allowed' });
      const body = await readBody(req);
      // 用户可能粘的是：裸 JWT / 整段 document.cookie / DevTools 里 _token 的 Value（有时带引号）
      const pasted = String(body.jwt || '').trim().replace(/^["'`]+|["'`]+$/g, '').trim();
      const cookieHeader = String(body.cookie || '').trim().replace(/^["'`]+|["'`]+$/g, '').replace(/[\r\n]+/g, ' ').trim();
      const raw = y2.extractJwt(pasted || cookieHeader);
      if (!raw) return json(res, 400, { ok: false, error: '没找到 JWT（粘的内容里没有 _token）' });
      let cookieOut = cookieHeader || pasted;
      if (cookieOut && !/(?:^|;\s*)_token=/.test(cookieOut)) cookieOut = '_token=' + cookieOut;
      try {
        const r = await y2.login(raw, cookieOut);
        if (y2Identity.size > 50) y2Identity.clear();
        y2Identity.set(raw, r.id);
        const cookie = `${Y2_COOKIE}=${encodeURIComponent(raw)}; Path=/; Max-Age=${Y2_COOKIE_TTL}; HttpOnly; SameSite=Lax`;
        return json(res, 200, { ok: true, userId: r.id.userId, auth: r.style }, { 'set-cookie': cookie });
      } catch (e) {
        return json(res, 200, { ok: false, error: e.message || 'Yanhe2 不认这个会话（过期或复制不全）' });
      }
    }

    if (u.pathname === '/api/y2/courses') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) return json(res, 401, { ok: false, error: '尚未登录 Yanhe 2.0', needLogin: true });
      try { return json(res, 200, { ok: true, courses: await y2.myCourses(jwt) }); }
      catch (e) { return json(res, 200, { ok: false, error: e.message }); }
    }

    // 学期列表（按学期筛选课程用）
    if (u.pathname === '/api/y2/terms') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) return json(res, 401, { ok: false, error: '尚未登录 Yanhe 2.0', needLogin: true });
      try {
        const terms = await y2.termList(jwt);
        return json(res, 200, { ok: true, terms });
      } catch (e) {
        // 拿不到学期表不算致命：前端只显示「全部学期」
        return json(res, 200, { ok: true, terms: [], warn: e.message });
      }
    }

    // 课程检索：q=课程名, term=学期 id, kind=all|record|live
    if (u.pathname === '/api/y2/search' || u.pathname === '/api/y2/find') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) return json(res, 401, { ok: false, error: '尚未登录 Yanhe 2.0', needLogin: true });
      const q = (u.searchParams.get('q') || '').trim();
      const term = (u.searchParams.get('term') || '').trim();
      const page = Math.max(1, Number(u.searchParams.get('page') || 1) || 1);
      if (!q && !term) return json(res, 400, { ok: false, error: '请输入课程名' });

      const notes = [];
      let courses = [];
      let total = 0;
      let source = 'catalog';
      let hidden = 0, unverified = 0;

      try {
        const r = await y2.searchCourses(jwt, { q, term, page, perPage: 20 });
        courses = r.courses;
        total = r.total;
      } catch (e) { notes.push('课程检索：' + e.message); }

      // title 是"精确/前缀"匹配：搜「大学物理」查不到「大学物理AII」。
      // 查不到时用课程库里的课名做包含匹配，再逐门回查（限 8 个课名）
      if (!courses.length && q) {
        try {
          const cat = catalogSearch(tokens.slice().sort((a, b) => b.length - a.length)[0] || q, '', 200);
          const titles = [];
          const seenT = new Set();
          for (const r of cat.courses) {
            const n = String(r.name || '').trim();
            const k = n.replace(/\s+/g, '').toLowerCase();
            if (!n || seenT.has(k)) continue;
            seenT.add(k);
            titles.push(n);
            if (titles.length >= 8) break;
          }
          if (titles.length) {
            const found = await pool(titles, 4, async (title) => {
              try {
                const r2 = await y2.searchCourses(jwt, { q: title, term, page: 1, perPage: 20 });
                return r2.courses || [];
              } catch (e) { return []; }
            });
            const seenC = new Set(courses.map(c => c.courseId));
            const words = tokens.map(t => t.toLowerCase());
            for (const list of found) {
              for (const c of list) {
                if (seenC.has(c.courseId)) continue;
                const hay = (String(c.title || '') + ' ' + String(c.teacher || '')).toLowerCase();
                if (!words.every(t => hay.includes(t))) continue;
                seenC.add(c.courseId);
                courses.push(c);
              }
            }
            if (courses.length) {
              total = courses.length;
              notes.push('按课程名包含匹配：' + titles.length + ' 个课名');
            }
          }
        } catch (e) { notes.push('课程名包含匹配：' + e.message); }
      }

      // 新版检索接口只吃课程名。这里把关键词按空格拆开：
      //   「数值分析 孙新」→ 拿最长的词当课程名查，其余词要求出现在课名或老师里
      //   「孙新」→ 课程名查不到，就用离线课程库的老师索引找出他教的课名，逐门回查
      //   「孙新 数值分析」同理，顺序不影响
      const tokens = q.split(/\s+/).map(t => t.trim()).filter(Boolean);
      if (tokens.length > 1 && courses.length) {
        const titleQ = tokens.slice().sort((a, b) => b.length - a.length)[0];
        const rest = tokens.filter(t => t !== titleQ).map(t => t.toLowerCase());
        courses = courses.filter(c => {
          const hay = (String(c.title || '') + ' ' + String(c.teacher || '') + ' ' + String(c.courseName || '')).toLowerCase();
          return rest.every(t => hay.includes(t));
        });
        total = courses.length;
      }

      if (!courses.length && tokens.length) {
        // 挑一个"像老师名"的词：在课程库教师索引里能命中的那个
        const tname = tokens.slice().sort((a, b) => catalogByTeacher(b, 1).length - catalogByTeacher(a, 1).length)[0];
        if (tname && catalogByTeacher(tname, 1).length) {
          // 先试试新版接口自己认不认"老师名"这个参数（认了就能直接拿到全部）
          const rest = tokens.filter(t => t !== tname).map(t => t.toLowerCase());
          try {
            const direct = await y2.searchByTeacher(jwt, tname, { term });
            const seen0 = new Set(courses.map(c => c.courseId));
            for (const c of direct) {
              if (seen0.has(c.courseId)) continue;
              const hay = (String(c.title || '') + ' ' + String(c.teacher || '')).toLowerCase();
              if (!hay.includes(tname.toLowerCase())) continue;
              if (!rest.every(t => hay.includes(t))) continue;
              seen0.add(c.courseId);
              courses.push(c);
            }
            if (direct.length) notes.push('新版接口按老师名直接命中 ' + direct.length + ' 门');
          } catch (e) {}

          const titles = teacherCourseTitles(tname);
          const found = await pool(titles, 4, async (title) => {
            try {
              const r2 = await y2.searchCourses(jwt, { q: title, term, page: 1, perPage: 20 });
              return r2.courses || [];
            } catch (e) { return []; }
          });
          const seen = new Set(courses.map(c => c.courseId));
          for (const list of found) {
            for (const c of list) {
              if (seen.has(c.courseId)) continue;
              const hay = (String(c.title || '') + ' ' + String(c.teacher || '')).toLowerCase();
              if (!hay.includes(tname.toLowerCase())) continue;
              if (!rest.every(t => hay.includes(t))) continue;
              seen.add(c.courseId);
              courses.push(c);
            }
          }
          if (courses.length) {
            total = courses.length;
            notes.push('按教师名匹配：' + tname + (titles.length > 1 ? '（' + titles.length + ' 门课）' : ''));
          }
        }
      }

      // 全部失败才退回「扫最近三周课表」的老办法（那边已经只统计有录像的讲次）
      if (!courses.length) {
        try {
          const list = await y2.findCourses(jwt, q || undefined);
          if (list.length) { courses = list; total = list.length; source = 'schedule'; }
        } catch (e) { notes.push('课表扫描：' + e.message); }
      }

      // 把"没有录像"的课筛掉（这一步会顺带拿到教室名）
      if (courses.length) {
        try {
          const v = await y2Verify(jwt, id, courses, { q, scanQ: scanKeyword(q) });
          courses = v.courses;
          hidden = v.hidden;
          unverified = v.unverified;
        } catch (e) { notes.push('录像校验：' + e.message); }
      }

      return json(res, 200, { ok: true, courses, total, shown: courses.length, hidden, unverified, source, notes });
    }

    if (u.pathname === '/api/y2/sessions') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) return json(res, 401, { ok: false, error: '尚未登录 Yanhe 2.0', needLogin: true });
      const courseId = (u.searchParams.get('courseId') || '').trim();
      const term = (u.searchParams.get('term') || '').trim();
      const q = (u.searchParams.get('q') || '').trim();
      const termName = (u.searchParams.get('termName') || '').trim();
      if (!courseId) return json(res, 400, { ok: false, error: '缺少 courseId' });
      const notes = [];
      let data;
      try {
        data = await y2Videos(jwt, id, courseId, { allowScan: true, q: scanKeyword(q), termName });
      } catch (e) {
        return json(res, 200, { ok: false, error: e.message });
      }
      if (data.note) notes.push(data.note);

      let list = data.sessions.slice();
      if (term) {
        const scoped = list.filter(s => !s.termId || s.termId === term);
        if (scoped.length) list = scoped;
      }
      list.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
      return json(res, 200, {
        ok: true, source: data.source, notes, hidden: data.totalSessions - data.videoCount,
        total: data.totalSessions,
        sessions: list.map(s => ({
          subId: s.subId, title: s.title, period: s.period || '', date: s.date || '',
          duration: s.duration, status: s.status, room: s.room, teacher: s.teacher,
          termId: s.termId || '', termName: s.termName || '',
        })),
      });
    }

    if (u.pathname === '/api/y2/sub') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) return json(res, 401, { ok: false, error: '尚未登录 Yanhe 2.0', needLogin: true });
      const courseId = (u.searchParams.get('courseId') || '').trim();
      const subId = (u.searchParams.get('subId') || '').trim();
      let info = null, err = '';
      try { info = await y2.subInfo(jwt, courseId, subId); }
      catch (e) { err = e.message; }
      if (!info) return json(res, 200, { ok: false, error: err || '取流失败' });
      if (!info.streams || !info.streams.length) {
        return json(res, 200, { ok: false, error: '这一讲没有录像文件（可能还没生成回放）' });
      }
      return json(res, 200, { ok: true, info });
    }

    if (u.pathname === '/y2play') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      if (!id) { res.writeHead(401); return res.end('need login'); }
      const raw = u.searchParams.get('u');
      if (!raw) { res.writeHead(400); return res.end('missing u'); }
      try { return await y2ProxyPlay(res, req, dec(raw), id); }
      catch (e) { res.writeHead(502); return res.end('play error: ' + e.message); }
    }

    // 自检页：登录后打开 /diag，能看到每个接口的真实状态与字段名（不含 token / 手机号）
    if (u.pathname === '/api/y2/diag' || u.pathname === '/diag') {
      const jwt = y2Token(req);
      const id = await y2Who(jwt);
      const report = { at: new Date().toISOString(), loggedIn: !!id, steps: [] };
      if (id) {
        const shape = (j) => {
          if (!j || typeof j !== 'object') return { type: typeof j };
          const o = { keys: Object.keys(j).slice(0, 25) };
          if (j.code !== undefined) o.code = j.code;
          if (j.msg !== undefined) o.msg = String(j.msg).slice(0, 120);
          if (j.message !== undefined) o.message = String(j.message).slice(0, 120);
          const d = j.data && typeof j.data === 'object' ? j.data : null;
          if (d) { o.dataKeys = Object.keys(d).slice(0, 30); }
          const cands = [d && d.list, d, j.list, j.params && j.params.list, j.result && j.result.data];
          for (const c of cands) {
            const a = Array.isArray(c) ? c : (c && typeof c === 'object' ? Object.values(c) : null);
            if (a && a.length && a[0] && typeof a[0] === 'object') { o.itemKeys = Object.keys(a[0]).slice(0, 40); o.count = a.length; break; }
          }
          return o;
        };
        const step = async (name, fn) => {
          const t0 = Date.now();
          try { report.steps.push({ name, ok: true, ms: Date.now() - t0, ...(await fn()) }); }
          catch (e) { report.steps.push({ name, ok: false, ms: Date.now() - t0, error: e.message }); }
        };

        await step('账号', async () => ({ userId: id.userId, tenantId: id.tenantId, hasAccount: !!id.account }));
        await step('学期列表', async () => {
          const raw = await y2.req('GET', y2.apiUrl('/vlabpassportapi/v1/course/term-list'), jwt);
          const terms = await y2.termList(jwt).catch(() => []);
          return { http: raw.status, shape: (() => { try { return shape(JSON.parse(raw.body.toString('utf8'))); } catch (e) { return { parse: 'fail' }; } })(), terms: terms.slice(0, 12), termCount: terms.length };
        });
        let foundCourses = [];
        await step('课程检索「数值分析」', async () => {
          const raw = await y2.req('GET', y2.apiUrl('/courseapi/v3/multi-search/get-course-list', { title: '数值分析', page: 1, per_page: 20, tenant: 21 }), jwt);
          let parsed = null;
          try { parsed = JSON.parse(raw.body.toString('utf8')); } catch (e) {}
          const r = await y2.searchCourses(jwt, { q: '数值分析', perPage: 20 }).catch((e) => ({ courses: [], err: e.message }));
          foundCourses = r.courses || [];
          return {
            http: raw.status, shape: shape(parsed), count: foundCourses.length, err: r.err,
            学期字段: foundCourses.slice(0, 5).map(c => ({ id: c.courseId, title: (c.title || '').slice(0, 18), termId: c.termId, termName: c.termName, 节次数: c.sessions })),
          };
        });

        // 关键诊断：学期时间窗 + 扫课表到底扫到多少
        await step('学期时间窗 + 扫课表', async () => {
          const termNames = new Map();
          let terms = [];
          try {
            terms = await y2.termList(jwt);
            for (const t of terms) termNames.set(String(t.id), t.name);
          } catch (e) {}
          const windows = windowsFor(foundCourses, termNames, 2);
          const out = { 学期表: terms.slice(0, 10), 待扫窗口: windows.map(w => ({ 学期: w.terms, 起止: w.from + ' ~ ' + w.to })), 扫描结果: [] };
          for (const w of windows) {
            const map = await y2ScanWindows(jwt, '数值分析', [w]).catch(() => new Map());
            let rows = 0, courses = 0;
            for (const [, v] of map) { courses++; rows += v.length; }
            out.扫描结果.push({ 学期: w.terms, 窗口: w.from + ' ~ ' + w.to, 扫到的课程数: courses, 扫到的讲次数: rows });
          }
          return out;
        });

        // 关键诊断：前 3 门课的讲次接口到底返回了什么
        for (const c of foundCourses.slice(0, 3)) {
          await step('讲次 courseId=' + c.courseId + ' 《' + (c.title || '').slice(0, 16) + '》', async () => {
            const out = { 课程库节次数: c.sessions };
            // 1) 原样请求，记录 HTTP + code/msg + 结构
            const tries = [
              { tag: 'student', params: { course_id: c.courseId, student: id.account || '' } },
              { tag: 'plain', params: { course_id: c.courseId } },
            ];
            for (const t of tries) {
              const raw = await y2.req('GET', y2.apiUrl('/courseapi/v3/multi-search/get-course-detail', t.params), jwt);
              let parsed = null;
              try { parsed = JSON.parse(raw.body.toString('utf8')); } catch (e) {}
              const entry = { http: raw.status, 变体: t.tag, shape: shape(parsed) };
              const d = await y2.courseDetail(jwt, c.courseId, id.account).catch((e) => ({ err: e.message, sessions: [] }));
              const statuses = {};
              for (const s of d.sessions || []) statuses[s.status || '(空)'] = (statuses[s.status || '(空)'] || 0) + 1;
              entry.解析出讲次 = (d.sessions || []).length;
              entry.状态分布 = statuses;
              entry.有教室名 = (d.sessions || []).some(s => s.room);
              entry.err = d.err;
              entry.讲次字段 = (d.sessions && d.sessions[0]) ? Object.keys(d.sessions[0]) : [];
              out.detail = entry;
              break;  // courseDetail 内部已做变体探测，看一次就够
            }
            // 2) 有没有录像 + 教室
            const v = await y2.courseVideos(jwt, c.courseId, id.account).catch((e) => ({ err: e.message, sessions: [] }));
            out.有录像 = (v.sessions || []).length;
            out.教室 = v.room || '';
            out.抽检 = (v.sessions || []).slice(0, 3);
            if (v.err) out.录像判定错误 = v.err;
            // 3) 最新一讲取流是否真的有文件
            if (v.sessions && v.sessions.length) {
              const first = v.sessions[0];
              const raw = await y2.req('GET', y2.apiUrl('/courseapi/v3/portal-home-setting/get-sub-info', { course_id: c.courseId, sub_id: first.subId }), jwt);
              let parsed = null;
              try { parsed = JSON.parse(raw.body.toString('utf8')); } catch (e) {}
              const info = await y2.subInfo(jwt, c.courseId, first.subId).catch((e) => ({ err: e.message, streams: [] }));
              out.取流 = { http: raw.status, shape: shape(parsed), 画面数: (info.streams || []).length, 教室: info.room || '', err: info.err };
            }
            return out;
          });
        }
      }
      if (u.pathname === '/diag') {
        res.writeHead(200, { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' });
        return res.end('<!doctype html><meta charset="utf-8"><title>自检</title>'
          + '<style>body{background:#0f1115;color:#e8eaed;font:13px/1.7 ui-monospace,Consolas,monospace;padding:20px}'
          + 'pre{white-space:pre-wrap;word-break:break-word}</style>'
          + '<h3 style="font-family:system-ui">接口自检（把这一整段发给开发者即可，里面没有你的 token）</h3>'
          + '<pre>' + JSON.stringify(report, null, 2).replace(/</g, '&lt;') + '</pre>');
      }
      return json(res, 200, report);
    }


    // ---- 内置课程库（本地，不需要登录）----
    if (u.pathname === '/api/db/terms') {
      const cat = loadCatalog();
      let live = [];
      try { live = (await oldSemesters(tokenOf(req))).map(t => t.name); } catch (e) {}
      // 老平台实时学期表 + 课程库里的（离线兜底），去重
      const seen = new Set();
      const terms = [];
      for (const t of [...live, ...cat.terms]) {
        if (!t || seen.has(t)) continue;
        seen.add(t); terms.push(t);
      }
      return json(res, 200, { ok: true, terms, live: live.length, count: cat.rows.length, meta: cat.meta, error: cat.error });
    }

    if (u.pathname === '/api/db/search') {
      const q = (u.searchParams.get('q') || '').trim();
      const term = (u.searchParams.get('term') || '').trim();
      const limit = Math.min(100, Math.max(1, Number(u.searchParams.get('limit') || 40) || 40));
      const page = Math.max(1, Number(u.searchParams.get('page') || 1) || 1);
      if (!q && !term) return json(res, 400, { ok: false, error: '请输入课程名或教师名' });

      const notes = [];
      const token = tokenOf(req);
      const user = token ? await verifyToken(token).catch(() => null) : null;
      const raw = await searchRaw(q, term, { token, user, limit, page, notes });

      // 要"有几讲有录像"就得逐门探节次（每门 1 个请求）。
      // **全都探**（单次最多 120 门），再按录像多少排序 —— 只给前 N 门会漏掉有录像的课
      if (u.searchParams.get('probe') === '1' && user) {
        const CAP = 120;
        const list = raw.courses.slice(0, CAP);
        const pr = await probeOldCourses(list, token, { concurrency: 8 });
        // 排序：先看课名/教师名的匹配度，再按录像数从多到少。
        // 不然搜「数值分析」，名字里只有「分析」的课（刚好录像多）会排到真正的「数值分析」前面。
        const sorted = pr.courses.slice().sort((a, b) => {
          const d = searchScore(b, q) - searchScore(a, q);
          return d || (b.videoCount || 0) - (a.videoCount || 0);
        });
        return json(res, 200, {
          ok: true, source: raw.src, total: raw.total, courses: sorted,
          hidden: pr.hidden, probed: true, notes,
          truncated: raw.courses.length > CAP, cap: CAP,
        });
      }

      return json(res, 200, {
        ok: true, source: raw.src, total: raw.total, courses: raw.courses,
        probed: false, notes, needLogin: !user,
      });
    }

    // 打开一门课：讲次 + 每讲的播放地址（都走本地 /p 代理）
    if (u.pathname === '/api/open') {
      const token = tokenOf(req);
      const user = await verifyToken(token);
      if (!user) return json(res, 401, { ok: false, error: '尚未登录', needLogin: true });
      const id = (u.searchParams.get('id') || '').trim();
      if (!id) return json(res, 400, { ok: false, error: '缺少课程编号' });
      const d = await getCourse(id, token, { force: u.searchParams.get('refresh') === '1' });
      return json(res, 200, d);
    }

    // 新版下载：aila 的流本身就是一个 MP4，直接带 Content-Disposition 转发整份就行
    if (u.pathname === '/y2dl') {
      const jwt = y2Token(req);
      const idn = await y2Who(jwt);
      if (!idn) { res.writeHead(401, { 'content-type': 'text/plain; charset=utf-8' }); return res.end('need login'); }
      const c = u.searchParams.get('c') || '';
      const s = u.searchParams.get('s') || '';
      const k = u.searchParams.get('k') || 'camera';
      try {
        const info = await y2.subInfo(jwt, c, s);
        const st = (info.streams || []).find(x => x.type === k) || (info.streams || [])[0];
        if (!st) { res.writeHead(404); return res.end('这一讲没有可下载的画面'); }
        const name = (u.searchParams.get('title') || ('course-' + c))
          .replace(/[\\/:*?"<>|\r\n\t]/g, '_').slice(0, 80);
        if (/\.m3u8/i.test(String(st.url).split('?')[0])) {
          return await y2DownloadHls(res, req, st.url, name);
        }
        return await y2ProxyPlay(res, req, st.url, idn, { filename: name + '.mp4', ext: '.mp4' });
      } catch (e) {
        res.writeHead(502, { 'content-type': 'text/plain; charset=utf-8' });
        return res.end('下载出错：' + e.message);
      }
    }

    if (u.pathname === '/y2stream') {
      const jwt = y2Token(req);
      const idn = await y2Who(jwt);
      if (!idn) { res.writeHead(401); return res.end('need login'); }
      const c = u.searchParams.get('c') || '';
      const s = u.searchParams.get('s') || '';
      const k = u.searchParams.get('k') || 'camera';
      try {
        const info = await y2.subInfo(jwt, c, s);
        const st = (info.streams || []).find(x => x.type === k) || (info.streams || [])[0];
        if (!st) { res.writeHead(404); return res.end('no stream'); }
        res.writeHead(302, { location: '/y2play?u=' + enc(st.url) });
        return res.end();
      } catch (e) {
        res.writeHead(502);
        return res.end('stream error');
      }
    }

    // 自检：把容易出错的判断（学期解析、教室编码）跑一遍，出问题一眼看出来
    if (u.pathname === '/api/selftest') {
      const out = [];
      const ok = (name, cond, detail) => out.push({ name, pass: !!cond, detail: detail == null ? '' : String(detail) });

      ok('学期写法归一：第一学期', termKeyOf('2026-2027 第一学期') === termKeyOf('2026-2027学年秋季学期'),
        termKeyOf('2026-2027 第一学期') + ' / ' + termKeyOf('2026-2027学年秋季学期'));
      ok('学期写法归一：第二学期', termKeyOf('2025-2026 第二学期') === termKeyOf('2025-2026春季学期'),
        termKeyOf('2025-2026 第二学期'));
      ok('本学期算"近的"', termIsRecent('2026-2027 第一学期'), String(termIsRecent('2026-2027 第一学期')));
      ok('两年前的学期不算近', !termIsRecent('2019-2020 第二学期'), String(termIsRecent('2019-2020 第二学期')));
      ok('学期解析', !!parseTermName('2026-2027 第一学期'), JSON.stringify(parseTermName('2026-2027 第一学期')));
      ok('教室编码→名字', roomName('280101004X', { roomByNumber: { '280101004X': '综教A406' } }) === '综教A406',
        roomName('280101004X', { roomByNumber: { '280101004X': '综教A406' } }));

      const oldToken = tokenOf(req);
      const oldUser = oldToken ? await verifyToken(oldToken).catch(() => null) : null;

      const live = {
        build: BUILD,
        oldLogin: !!oldUser,
      };

      // 老平台这边同一条课的实际情况（有 id 才能查）
      const cid = (u.searchParams.get('id') || '').trim();
      if (cid && oldUser) {
        try {
          const d = await getCourse(cid, oldToken, {});
          if (d && d.ok) {
            live.old = {
              name: d.course && d.course.name,
              term: d.course && (d.course.year ? d.course.year + ' 第' + d.course.semester + '学期' : ''),
              total: d.stats && d.stats.total,
              playable: d.stats && d.stats.playable,
            };
          } else {
            live.old = { error: (d && d.error) || '读取失败' };
          }
        } catch (e) { live.old = { error: e.message }; }
      }

      if (live.old && 'playable' in live.old) {
        const o = live.old.playable > 0;
        live.verdict = o
          ? 'ok（有 ' + live.old.playable + ' 讲可播）'
          : (live.old.total > 0 ? 'no-video（老平台这边确实没有可播录像）' : 'no-session（一门课都没排/没权限）');
      }

      // 下载情况：第一讲在 CDN 上到底有多少片、多长、是不是"滑动窗口"
      // （如果片数对应的时长明显短于讲次时长，就是 CDN 只给了一段，不是下载写错了）
      if (cid && oldUser && live.old && live.old.playable > 0) {
        try {
          const d = await getCourse(cid, oldToken, {});
          const first = (d.sessions || []).find(x => x.main || x.vga);
          if (first) {
            const vid = await videoToken(oldToken, oldUser.badge);
            const m = await resolveMedia(first.main || first.vga, vid);
            live.download = {
              session: { date: first.date, durationMin: first.duration ? Math.round(first.duration / 60) : null },
              segments: m.segments.length,
              playlistMinutes: Math.round(m.duration / 60),
              hasEndList: m.endlist,
              mediaSequence: m.mediaSequence,
              encrypted: m.encrypted,
            };
          }
        } catch (e) { live.download = { error: e.message }; }
      }

      return json(res, 200, { ok: true, checks: out, allPass: out.every(x => x.pass), live });
    }

    if (u.pathname === '/api/status') {
      const user = await verifyToken(tokenOf(req));
      return json(res, 200, { loggedIn: !!user, user, signer: signer.status(), build: BUILD });
    }

    if (u.pathname === '/api/login') {
      if (req.method !== 'POST') return json(res, 405, { ok: false, error: 'method not allowed' });
      const body = await readBody(req);
      const token = String(body.token || '').replace(/^Bearer\s+/i, '').trim();
      if (!token) return json(res, 400, { ok: false, error: 'token 为空' });
      const user = await verifyToken(token);
      if (!user) return json(res, 200, { ok: false, error: 'token 无效或已过期，请重新获取' });
      const cookie = `${COOKIE}=${encodeURIComponent(token)}; Path=/; Max-Age=${COOKIE_TTL}; HttpOnly; SameSite=Lax`;
      return json(res, 200, { ok: true, user }, { 'set-cookie': cookie });
    }

    // 自动登录：去本机浏览器里找已登录的凭证，找到就直接种 cookie
    if (u.pathname === '/api/login/auto') {
      if (req.method !== 'POST' && req.method !== 'GET') return json(res, 405, { ok: false, error: 'method not allowed' });
      const r = await autoLoginOld();
      if (!r.user) {
        return json(res, 200, { ok: false, error: '没在浏览器里找到登录状态', candidates: r.candidates, files: r.files });
      }
      const cookie = `${COOKIE}=${encodeURIComponent(r.token)}; Path=/; Max-Age=${COOKIE_TTL}; HttpOnly; SameSite=Lax`;
      return json(res, 200, { ok: true, user: r.user, from: r.from }, { 'set-cookie': cookie });
    }

    if (u.pathname === '/api/y2/login/auto') {
      if (req.method !== 'POST' && req.method !== 'GET') return json(res, 405, { ok: false, error: 'method not allowed' });
      const r = await autoLoginNew();
      if (!r.id) return json(res, 200, { ok: false, error: '没在浏览器里找到登录状态', candidates: r.candidates, files: r.files });
      const cookie = `${Y2_COOKIE}=${encodeURIComponent(r.jwt)}; Path=/; Max-Age=${Y2_COOKIE_TTL}; HttpOnly; SameSite=Lax`;
      return json(res, 200, { ok: true, userId: r.id.userId, from: r.from }, { 'set-cookie': cookie });
    }

    if (u.pathname === '/api/logout') {
      return json(res, 200, { ok: true }, { 'set-cookie': `${COOKIE}=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax` });
    }

    if (u.pathname === '/api/course') {
      const token = tokenOf(req);
      const user = await verifyToken(token);
      if (!user) return json(res, 401, { ok: false, error: '尚未登录', needLogin: true });
      const id = (u.searchParams.get('id') || '').trim();
      if (!id) return json(res, 400, { ok: false, error: '缺少课程编号' });
      return json(res, 200, await getCourse(id, token, { force: u.searchParams.get('refresh') === '1' }));
    }

    if (u.pathname === '/p') {
      const raw = u.searchParams.get('u');
      if (!raw) { res.writeHead(400); return res.end('missing u'); }
      return await proxyHls(res, req, dec(raw));
    }

    // 直播列表（旧平台，匿名即可）
    if (u.pathname === '/api/live') {
      try {
        const d = await liveListOld();
        return json(res, 200, { ok: true, items: d.items, at: d.at });
      } catch (e) {
        return json(res, 200, { ok: false, items: [], error: e.message });
      }
    }

    // 直播流代理（不缓存、不排队）
    if (u.pathname === '/lp') {
      const raw = u.searchParams.get('u');
      if (!raw) { res.writeHead(400); return res.end('missing u'); }
      return await proxyLive(res, req, dec(raw));
    }

    // 下载一个讲次：把 HLS 分片拼成单文件直接给浏览器
    if (u.pathname === '/dl') {
      const token = tokenOf(req);
      const user = await verifyToken(token);
      if (!user) { res.writeHead(401, { 'content-type': 'text/plain; charset=utf-8' }); return res.end('need login'); }
      return await downloadSession(res, req, {
        course: (u.searchParams.get('course') || '').trim(),
        session: (u.searchParams.get('session') || '').trim(),
        kind: (u.searchParams.get('kind') || 'main') === 'vga' ? 'vga' : 'main',
        probe: u.searchParams.get('probe') === '1',
      }, token, user);
    }

    res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8' });
    res.end('404');
  } catch (e) {
    if (res.headersSent) { try { res.end(); } catch (x) {} return; }
    json(res, 500, { ok: false, error: e.message });
  }
});

// ---------------- 启动 ----------------
// Windows 控制台默认 GBK，先把代码页切成 UTF-8，中文才不会变乱码
function setUtf8Console() {
  if (process.platform !== 'win32') return;
  try {
    const { execFileSync } = require('child_process');
    execFileSync('cmd', ['/c', 'chcp', '65001'], { stdio: 'ignore', windowsHide: true });
  } catch (e) {}
}

function probeExisting(port) {
  return new Promise((resolve) => {
    const req = http.get({ host: '127.0.0.1', port, path: '/api/status', timeout: 1500 }, (res) => {
      let b = '';
      res.on('data', d => b += d);
      res.on('end', () => {
        try {
          const j = JSON.parse(b);
          resolve(('loggedIn' in j) ? j : null);
        } catch (e) { resolve(null); }
      });
    });
    req.on('error', () => resolve(null));
    req.on('timeout', () => { req.destroy(); resolve(null); });
  });
}

const signerReady = signer.init()
  .then(s => s)
  .catch(e => { console.log('signer init failed, using cache/default: ' + e.message); });

process.on('uncaughtException', (e) => {
  console.log('\n  [FATAL] ' + (e && e.stack ? e.stack : e));
  console.log('  Window stays open. Press Ctrl+C to quit.\n');
});

/** 把地址塞进剪贴板：省得用户手抄（不开浏览器，避免和已开的浏览器打架） */
function copyToClipboard(text) {
  if (process.platform !== 'win32') return false;
  try {
    const { spawn } = require('child_process');
    const p = spawn('clip', [], { stdio: ['pipe', 'ignore', 'ignore'], windowsHide: true });
    p.on('error', () => {});
    p.stdin.end(text + '\r\n');
    return true;
  } catch (e) { return false; }
}

(async () => {
  setUtf8Console();

  const running = process.env.FORCE_NEW === '1' ? null : await probeExisting(PORT);
  if (running) {
    const url = `http://127.0.0.1:${PORT}/`;
    console.log('');
    console.log('  ═══════════════════════════════════════════════════');
    console.log('   播放器已经在运行了，不用再开一个。');
    console.log('');
    console.log('   正在运行的版本：' + (running.build || '更早的旧版（没有版本号）'));
    console.log('   这个文件是版本：' + BUILD);
    if (running.build !== BUILD) {
      console.log('');
      console.log('   ⚠ 两个版本不一样 —— 你现在看到的是旧的那个！');
      console.log('     先把那个黑窗口关掉（浏览器里的旧页面也关掉），再双击本文件。');
    }
    console.log('');
    console.log('   请复制以下链接到浏览器打开：');
    console.log('');
    console.log('        ' + url);
    console.log('');
    console.log('   （如果浏览器里已经开着播放器，刷新一下页面就行）');
    console.log('  ═══════════════════════════════════════════════════');
    // 不自动关窗，等用户看完再关；20 秒后自行退出，免得留一个空窗口
    setTimeout(() => process.exit(0), 20000);
    return;
  }

  let port = PORT;
  const listen = (p) => new Promise((resolve, reject) => {
    const onErr = (e) => { server.removeListener('listening', onOk); reject(e); };
    const onOk = () => { server.removeListener('error', onErr); resolve(p); };
    server.once('error', onErr);
    server.once('listening', onOk);
    server.listen(p, HOST);
  });

  for (let i = 0; i < 12; i++) {
    try { await listen(port); break; }
    catch (e) {
      if (e.code !== 'EADDRINUSE') throw e;
      port++;
    }
  }

  const urls = [`http://127.0.0.1:${port}/`];
  if (LAN) {
    for (const list of Object.values(os.networkInterfaces())) {
      for (const ni of list || []) {
        if (ni.family === 'IPv4' && !ni.internal) urls.push(`http://${ni.address}:${port}/`);
      }
    }
  }

  console.log('');
  console.log('  ═══════════════════════════════════════════════════');
  console.log('   延河课堂 · 在线播放器          版本 ' + BUILD);
  console.log('  ═══════════════════════════════════════════════════');
  console.log('');
  const copied = copyToClipboard(urls[0]);
  if (copied) console.log('   地址已经复制到剪贴板 —— 打开浏览器，地址栏 Ctrl+V 回车：');
  else console.log('   请复制以下链接到浏览器打开：');
  console.log('');
  urls.forEach(x => console.log('        ' + x));
  console.log('');
  console.log('   请勿关闭此窗口，此窗口为服务器。');
  console.log('   关闭窗口 ＝ 关闭播放器。');
  console.log('');
  if (LAN) console.log('   （局域网模式：同学用上面那个 10.x 地址也能打开）');
  console.log('  ═══════════════════════════════════════════════════');
  console.log('');

  try { process.title = 'Yanhekt Player  ->  ' + urls[0]; } catch (e) {}

  setInterval(() => signer.init({ force: true }).catch(() => {}), 6 * 3600 * 1000);
})();
