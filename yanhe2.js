/**
 * Yanhe 2.0（aita.yanhekt.cn）通道
 *
 * 和老平台完全是两套东西：
 *   - 登录：24 小时 JWT（浏览器里的 `_token` cookie）
 *   - 课程：/courseapi/v3/multi-search/get-course-list（按名称 + 学期检索，跨所有学期）
 *   - 讲次：/courseapi/v3/multi-search/get-course-detail 直接给全量 sub_list
 *           （拿不到时退回按天扫课表 search-role-course-list）
 *   - 直播：/courseapi/v2/course-live/search-live-course-list（真正的直播课列表）
 *   - 播放：/play/ 下是 **支持 Range 的 MP4**，拖动可以直接定位
 *   - 鉴权：每个请求带 t={userId}-{unix}-md5(pathname+userId+tenantId+reverse(phone)+unix)
 *
 * 手机号字段是签名密钥，只留在内存里，不写盘、不进日志。
 */
const https = require('https');
const crypto = require('crypto');

const Y2 = 'https://aita.yanhekt.cn';
const TENANT = 21;
const UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.3';

const md5 = s => crypto.createHash('md5').update(s, 'utf8').digest('hex');
const text = v => (typeof v === 'string' ? v.trim() : typeof v === 'number' ? String(v) : '');
const num = v => { const n = Number(v); return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0; };
const pick = (o, ...keys) => { for (const k of keys) { const v = o && o[k]; if (v !== undefined && v !== null && v !== '') return v; } return ''; };
const arr = v => Array.isArray(v) ? v : (v && typeof v === 'object' ? Object.values(v) : []);

/** epoch 秒 / 毫秒 / "YYYY-MM-DD HH:mm:ss" 都能转成秒 */
function tsOf(v) {
  if (v === undefined || v === null || v === '') return 0;
  if (typeof v === 'number' || /^\d+$/.test(String(v))) {
    const n = Number(v);
    if (!Number.isFinite(n) || n <= 0) return 0;
    return n > 1e12 ? Math.floor(n / 1000) : Math.floor(n);
  }
  const s = String(v).trim().replace('T', ' ');
  const t = Date.parse(s.replace(/-/g, '/') + (/^\d{4}\/\d{1,2}\/\d{1,2}$/.test(s.replace(/-/g, '/')) ? ' 00:00:00' : ''));
  return Number.isFinite(t) ? Math.floor(t / 1000) : 0;
}

function dateOf(v) {
  const ts = tsOf(v);
  if (!ts) return '';
  const d = new Date(ts * 1000);
  return new Date(d.getTime() + 8 * 3600 * 1000).toISOString().slice(0, 10);
}

// ---------------- 基础请求 ----------------
function req(method, url, jwt, extraHeaders, body) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const headers = Object.assign({
      'User-Agent': UA,
      Accept: 'application/json, text/plain, */*',
      Referer: Y2 + '/',
      Origin: Y2,
      'tenant-id': String(TENANT),
    }, extraHeaders || {});
    if (jwt) headers.Authorization = 'Bearer ' + jwt;
    const r = https.request(u, { method, headers, rejectUnauthorized: false, servername: u.hostname }, (res) => {
      const c = []; res.on('data', d => c.push(d));
      res.on('end', () => {
        const buf = Buffer.concat(c);
        let json = null;
        try { json = JSON.parse(buf.toString('utf8')); } catch (e) {}
        resolve({ status: res.statusCode, headers: res.headers, json, body: buf });
      });
    });
    r.on('error', reject);
    r.setTimeout(25000, () => r.destroy(new Error('aita 超时')));
    if (body) r.write(body);
    r.end();
  });
}

function apiUrl(path, params) {
  const url = new URL(path, Y2);
  for (const [k, v] of Object.entries(params || {})) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) {
      if (!v.length) continue;
      for (const x of v) if (x !== '' && x !== null && x !== undefined) url.searchParams.append(k + '[]', String(x));
      continue;
    }
    url.searchParams.set(k, String(v));
  }
  return url.toString();
}

async function getJson(jwt, path, params) {
  const res = await req('GET', apiUrl(path, params), jwt);
  if (res.status === 401 || res.status === 403) throw new Error('Yanhe2 会话被拒绝（已过期？）');
  if (res.status < 200 || res.status >= 300) throw new Error(`Yanhe2 HTTP ${res.status}`);
  if (!res.json) throw new Error('Yanhe2 返回非 JSON');
  return res.json;
}

/** 不抛错的探测：失败时返回 {__err}，便于上层自动降级 */
async function tryJson(jwt, path, params) {
  try { return { ok: true, body: await getJson(jwt, path, params) }; }
  catch (e) { return { ok: false, err: e.message }; }
}

// ---------------- 账号信息 ----------------
async function profile(jwt) {
  const body = await getJson(jwt, '/userapi/v1/infosimple');
  const p = body && body.params;
  if (!p) throw new Error('infosimple 返回结构异常');
  const userId = num(p.id);
  const tenantId = num(p.tenant_id) || TENANT;
  const phone = text(p.phone);
  if (!userId) throw new Error('infosimple 缺少 id');
  return { userId, tenantId, phone, account: text(p.account), realname: text(p.realname) };
}

/**
 * 变体探测：接口一次能试几种参数写法，成功一次就记住（不同学校/版本部署差异很大）。
 * 返回第一个"有内容"的结果；全都不行就抛最后一次的错误。
 * 注意：单个接口返回 401/403 只说明这个接口的写法不对，不代表登录态失效 —— 除非全部尝试都是鉴权失败。
 */
async function probeVariants(jwt, tries, extract) {
  const errs = [];
  let empty = null;
  for (const t of tries) {
    try {
      const body = await getJson(jwt, t.path, t.params);
      const got = extract(body, t);
      // 空数组也算"没拿到"，继续试下一种写法（[] 本身是 truthy，得显式判）
      const has = got && got.value && (Array.isArray(got.value) ? got.value.length > 0 : true);
      if (has) {
        t.__ok = true;
        return { ...got, variant: t };
      }
      if (!empty) empty = { ...got, variant: t };
    } catch (e) {
      t.__fail = e.message;
      errs.push(e);
    }
  }
  if (empty) return empty;
  const authOnly = errs.length && errs.every(e => /会话被拒绝/.test(e.message));
  if (authOnly) throw errs[errs.length - 1];
  throw errs[errs.length - 1] || new Error('接口不可用');
}

// ---------------- 学期列表 ----------------
let termCache = { at: 0, list: [] };

function termsFromList(raw) {
  const out = [];
  for (const t of arr(raw)) {
    const id = text(pick(t, 'id', 'term_id', 'termId'));
    const name = text(pick(t, 'term_name', 'termName', 'name', 'title'));
    if (id && name) out.push({ id, name });
  }
  return out;
}

/** 学年分组结构 {schoolYear:[{id,term_name}]} 拍平 */
function flatTermYears(data) {
  if (Array.isArray(data)) return termsFromList(data);
  const out = [];
  for (const year of Object.keys(data || {})) {
    for (const t of arr(data[year])) {
      const id = text(pick(t, 'id', 'term_id'));
      const name = text(pick(t, 'term_name', 'termName', 'name'));
      if (id && name) out.push({ id, name: /^\d{4}/.test(name) ? name : `${year} ${name}`.trim() });
    }
  }
  return out;
}

async function termList(jwt) {
  if (termCache.list.length && Date.now() - termCache.at < 3600e3) return termCache.list;
  const got = await probeVariants(jwt, [
    { name: 'course/term-list', path: '/vlabpassportapi/v1/course/term-list' },
    { name: 'get-term-years', path: '/courseapi/index.php/v2/course/get-term-years', params: { tenant: TENANT } },
    { name: 'schedule/search-term', path: '/courseapi/index.php/v2/schedule/search-term', params: { tenant: TENANT } },
  ], (body) => {
    const p = (body && body.params) || {};
    const a = termsFromList(p.list || p.data || (body && body.data) || (body && body.list));
    if (a.length) return { value: a };
    const b = flatTermYears((body && body.result && body.result.data) || (body && body.data && body.data.data));
    return { value: b };
  });
  const list = got.value || [];
  if (list.length) termCache = { at: Date.now(), list };
  return list;
}

// ---------------- 课程 / 讲次 ----------------
async function myCourses(jwt) {
  const body = await getJson(jwt, '/vlabpassportapi/v1/account-profile/course');
  // 结构：{code:1000, params:{result:{data:[{Id,Title,Teacher,TermName,...}]}}}
  const result = (body && body.params && body.params.result) || (body && body.result) || {};
  const list = Array.isArray(result.data) ? result.data
    : Array.isArray(result) ? result
    : (Array.isArray(body && body.data) ? body.data : []);
  return list.map(c => ({
    id: text(c.Id) || text(c.course_id) || text(c.id),
    name: text(c.Title) || text(c.course_title) || text(c.name),
    teacher: text(c.Teacher) || text(c.lecturer_name),
    term: text(c.TermName),
    cover: text(c.Thumb) || '',
  })).filter(c => c.id);
}

/** 一周的课表：每行 course_id + sub_id + sub_status */
async function weekSchedule(jwt, userId, startAt, endAt) {
  const body = await getJson(jwt, '/courseapi/v2/schedule/get-week-schedules', {
    user_id: userId, tenant_id: TENANT, start_at: startAt, end_at: endAt,
  });
  const result = (body && (body.result || body.data)) || {};
  const days = result.list || result.schedule || [];
  const out = [];
  for (const day of arr(days)) {
    const rows = day.course || day.list || [];
    for (const row of arr(rows)) {
      const subId = text(row.id) || text(row.sub_id);
      const courseId = text(row.course_id);
      if (!subId || !courseId) continue;
      out.push({
        subId, courseId,
        title: text(row.sub_title) || text(row.title) || '',
        courseName: text(row.course_title) || text(row.course_name) || '',
        date: text(day.day) || text(row.start_at).slice(0, 10),
        startAt: num(row.start_at),
        endAt: num(row.end_at),
        duration: num(row.duration) || num(row.sub_duration),
        status: text(row.sub_status) || text(row.status),
        room: text(row.room_name),
        teacher: text(row.lecturer_name),
      });
    }
  }
  return out;
}

/**
 * 把自己课表里近 N 天的所有讲次抓成一个 Map（courseId -> 讲次[]）。
 * 这是"讲次直读"失败时的救命通道：课表接口是用户自己的数据，一定给，
 * 而且每行就带 sub_status 和 room_name —— 正好是判定有没有录像、教室在哪要的东西。
 * 一次覆盖所有课程，比按课程扫便宜得多。
 */
async function scheduleMap(jwt, userId, { days = 150 } = {}) {
  const today = new Date();
  const to = new Date(today.getTime() + 86400 * 1000);
  const from = new Date(today.getTime() - days * 86400 * 1000);

  const collect = async (span) => {
    const rows = [];
    let anyData = false;
    for (let t = new Date(from); t < to; t = new Date(t.getTime() + span * 86400 * 1000)) {
      const end = new Date(Math.min(t.getTime() + (span - 1) * 86400 * 1000, to.getTime()));
      try {
        const list = await weekSchedule(jwt, userId, ymdOf(t), ymdOf(end));
        if (list.length) anyData = true;
        rows.push(...list);
      } catch (e) {
        if (/会话被拒绝/.test(e.message)) throw e;
      }
    }
    return { rows, anyData };
  };

  // 先试 30 天一块（一般接口都吃），拿不到日期再用 7 天；两者都没数据就返回空表
  let got = await collect(30);
  if (!got.rows.length) {
    const w = await collect(7);
    if (w.rows.length) got = w;
    else if (!got.anyData && !w.anyData) return new Map();
  }

  const map = new Map();
  for (const r of got.rows) {
    if (isLiveStatus(r.status)) continue;
    const key = String(r.courseId);
    if (!map.has(key)) map.set(key, []);
    const list = map.get(key);
    if (!list.some(x => x.subId === r.subId)) list.push({
      subId: r.subId,
      title: r.title,
      startAt: r.startAt,
      duration: r.duration,
      status: r.status,
      room: r.room,
      teacher: r.teacher,
      date: r.date,
      termId: '',
      termName: '',
    });
  }
  for (const list of map.values()) list.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
  return map;
}

function ymdOf(d) { return d.toISOString().slice(0, 10); }

const STATUS_NAME = { '1': 'live', '2': 'upcoming', '3': 'processing', '5': 'ended', '6': 'playable' };
const RECORDED_TYPE = { '3': 'camera', '2': 'screen', '4': 'room' };

/** 讲次类型：直播类 vs 录播类 */
const LIVE_SUB_TYPES = ['course_live', 'ilive', 'olive', 'live', 'large_class', 'live_video'];
function kindOfSub(type) {
  const t = text(type).toLowerCase();
  if (!t) return 'record';
  if (LIVE_SUB_TYPES.includes(t)) return 'live';
  return 'record';
}

async function subInfo(jwt, courseId, subId) {
  const body = await getJson(jwt, '/courseapi/v3/portal-home-setting/get-sub-info', {
    course_id: courseId, sub_id: subId,
  });
  if (!body || body.code !== 0 || !body.data) throw new Error('get-sub-info 失败');
  const d = body.data;
  const status = STATUS_NAME[text(d.sub_status)] || 'unknown';

  const streams = [];
  const items = arr(d.video_list);
  for (const item of items) {
    if (!item) continue;
    const type = RECORDED_TYPE[text(item.type)];
    const url = text(item.preview_url);
    if (!type || !url) continue;
    const clean = url.split('?')[0];
    const format = /\.m3u8$/i.test(clean) ? 'hls' : /\.mp4$/i.test(clean) ? 'mp4' : (/\/play\/|\/live\//.test(url) ? 'hls' : null);
    if (format && !streams.some(s => s.type === type)) streams.push({ type, format, url });
  }
  const order = { camera: 0, screen: 1, room: 2 };
  streams.sort((a, b) => order[a.type] - order[b.type]);

  let audio = '';
  for (const item of arr(d.audio_list)) {
    const u = item && text(item.url);
    if (u && /\.aac$/i.test(u.split('?')[0])) { audio = u; break; }
  }

  return {
    status,
    kind: kindOfSub(pick(d, 'type', 'sub_type')),
    title: text(d.course_title),
    subTitle: text(d.sub_title),
    teacher: text(d.lecturer_name),
    room: text(pick(d, 'room_name', 'sub_room_name', 'classroom', 'class_room', 'room')),
    startAt: num(d.start_at),
    endAt: num(d.end_at),
    duration: num(d.duration),
    streams,
    audio,
  };
}

/**
 * 讲次能不能当"录像"看：
 *   6 = 可播放录像            → 直接算
 *   5 = 已结束 / '' = 未知     → 有可能已有回放，需要实测一次视频流
 *   1 = 直播中  2 = 未开始  3 = 处理中 → 不算（不提供直播，也不显示）
 */
function isRecordingStatus(status) {
  return status === '6';
}
function maybeRecording(status) {
  return status === '5' || status === '' || status === undefined;
}
function isLiveStatus(status) {
  return status === '1' || status === '2' || status === '3';
}

// ---------------- 课程检索（跨学期） ----------------
const COURSE_LIST = '/courseapi/v3/multi-search/get-course-list';
const COURSE_DETAIL = '/courseapi/v3/multi-search/get-course-detail';

function normCourse(c) {
  const id = text(pick(c, 'id', 'course_id', 'Id', 'courseId'));
  if (!id) return null;
  const teachers = arr(c.teachers).map(t => text(pick(t, 'realname', 'name'))).filter(Boolean);
  const rawCount = pick(c, 'sub_count', 'subject_count', 'num');
  return {
    courseId: id,
    title: text(pick(c, 'title', 'course_title', 'Title', 'name')),
    teacher: teachers.join('、') || text(pick(c, 'realname', 'teacher', 'lecturer_name', 'Teacher')),
    termId: text(pick(c, 'term', 'term_id', 'termId')),
    termName: text(pick(c, 'term_name', 'termName', 'TermName')),
    kind: kindOfSub(pick(c, 'type', 'course_type', 'sub_type')) === 'live' ? 'live' : 'record',
    // 接口没给节次数时用 null，别和"确实是 0 讲"混在一起
    sessions: rawCount === '' ? null : num(rawCount),
    cover: text(pick(c, 'thumb', 'Thumb', 'cover')),
    room: '',
    videoCount: null,
    verified: false,
  };
}

/**
 * 按课程名检索（覆盖平台上所有学期，不只是"我的课程"）
 * 参数名在各版本部署里不一致，这里一次试几种写法，成功即记住。
 * @returns {Promise<{courses:Array, total:number}>}
 */
async function searchCourses(jwt, { q = '', term = '', page = 1, perPage = 40 } = {}) {
  const base = { term, page, per_page: perPage, tenant: TENANT };
  const got = await probeVariants(jwt, [
    { name: 'title', path: COURSE_LIST, params: { ...base, title: q, order: '', publish: '', struc_ids: [] } },
    { name: 'title-plain', path: COURSE_LIST, params: { ...base, title: q } },
    { name: 'like_title', path: COURSE_LIST, params: { ...base, like_title: q } },
    { name: 'title-no-term', path: COURSE_LIST, params: { page, per_page: perPage, tenant: TENANT, title: q } },
  ], (body) => {
    if (body && body.code !== 0 && body.code !== 200 && body.code !== 1000) return { value: null, note: text(body.msg) };
    const data = (body && body.data) || {};
    const list = arr(data.list || data.data || data.rows || data);
    const courses = [];
    for (const c of list) { const n = normCourse(c); if (n) courses.push(n); }
    return { value: courses, courses, total: num(data.total) || num(body && body.total) || courses.length };
  });
  return { courses: got.courses || [], total: got.total || 0 };
}

/**
 * 讲次直读：get-course-detail 一次给全量 sub_list。
 * 两个关键点：
 *  1. "接口通了但一条讲次都没有"是**合法结果**（这门课就是没录像），不能当失败
 *  2. 各版本把讲次放在哪不好猜，所以除了 sub_list / origin_sub_list，
 *     还会在 data 里递归找"看起来像讲次"的数组，甚至解开 JSON 字符串里的
 */
function looksLikeSub(o) {
  if (!o || typeof o !== 'object' || Array.isArray(o)) return false;
  const hasId = 'id' in o || 'sub_id' in o || 'subject_id' in o;
  const hasSub = 'type' in o || 'sub_type' in o || 'sub_status' in o || 'start_at' in o || 'start_time' in o || 'sub_title' in o;
  return hasId && hasSub;
}

/** 在任意嵌套结构里找讲次数组（数组里多数元素像讲次就算） */
function deepSubArray(root) {
  let found = null;
  const walk = (o, depth) => {
    if (!o || found || depth > 5) return;
    if (typeof o === 'string') {
      const s = o.trim();
      if ((s.startsWith('[') || s.startsWith('{')) && s.length < 500000) {
        try { walk(JSON.parse(s), depth + 1); } catch (e) {}
      }
      return;
    }
    if (Array.isArray(o)) {
      if (o.length && o.filter(looksLikeSub).length >= Math.max(1, Math.ceil(o.length * 0.6))) { found = o; return; }
      for (const x of o) { walk(x, depth + 1); if (found) return; }
      return;
    }
    if (typeof o === 'object') {
      for (const k of Object.keys(o)) { walk(o[k], depth + 1); if (found) return; }
    }
  };
  walk(root, 0);
  return found || [];
}

function mapSub(courseId, s) {
  const startRaw = pick(s, 'start_at', 'start_time', 'begin_at', 'start_date', 'date');
  const type = text(pick(s, 'type', 'sub_type'));
  return {
    subId: text(pick(s, 'id', 'sub_id', 'subject_id')),
    title: text(pick(s, 'title', 'sub_title', 'name')),
    startAt: tsOf(startRaw),
    duration: num(pick(s, 'duration', 'sub_duration', 'length')),
    status: text(pick(s, 'sub_status', 'status', 'live_status')),
    room: text(pick(s, 'room_name', 'sub_room_name', 'classroom', 'class_room', 'room', 'roomName')),
    teacher: text(pick(s, 'lecturer_name', 'teacher_name', 'realname')),
    type,
    kind: kindOfSub(type),
    termId: text(pick(s, 'term', 'term_id', 'termId')),
    termName: text(pick(s, 'term_name', 'termName')),
    date: dateOf(startRaw),
  };
}

async function courseDetail(jwt, courseId, account) {
  const parse = (body) => {
    if (!body || typeof body !== 'object') return null;
    if (body.code !== undefined && body.code !== 0 && body.code !== 200 && body.code !== 1000) return null;
    const d = body.data && typeof body.data === 'object' ? body.data : body;
    const raw = arr(d.sub_list).length ? arr(d.sub_list)
      : arr(d.origin_sub_list).length ? arr(d.origin_sub_list)
      : deepSubArray(d);
    const sessions = [];
    for (const s of arr(raw)) {
      const m = mapSub(courseId, s);
      if (m.subId) sessions.push(m);
    }
    return {
      courseId,
      title: text(pick(d, 'title', 'course_title')),
      termName: text(pick(d, 'term_name', 'termName')),
      sessions,
    };
  };

  const got = await probeVariants(jwt, [
    { name: 'multi-search+student', path: COURSE_DETAIL, params: { course_id: courseId, student: account || '' } },
    { name: 'multi-search', path: COURSE_DETAIL, params: { course_id: courseId } },
    { name: 'person/course-api', path: '/vlabpassportapi/person/course-api/get-course-detail', params: { course_id: courseId } },
  ], (body) => {
    const r = parse(body);
    // 解析成功就算数，哪怕 0 条讲次（0 条 = 这门课没录像）
    return r ? { value: r } : { value: null };
  });
  if (!got.value) throw new Error('get-course-detail 没返回可用数据');
  return got.value;
}

/**
 * 一门课"到底有没有能看的录像"，以及这些录像的讲次列表。
 * 判定顺序：
 *   1. sub_status=6（可播放）→ 直接算有录像
 *   2. 只有 5 / 未知 状态的 → 抽最新的一两条真的去要一次视频流，要到才算
 *   3. 只有 1/2/3（直播中/未开始/处理中）→ 没有录像
 * 另外补一次教室名：讲次自带就用，没带就找 get-sub-info 要。
 */
async function courseVideos(jwt, courseId, account) {
  const d = await courseDetail(jwt, courseId, account);
  const all = d.sessions.slice().sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
  const confirmed = all.filter(s => isRecordingStatus(s.status));
  const maybe = all.filter(s => maybeRecording(s.status));

  let sessions = [];
  let probed = false;

  if (confirmed.length) {
    sessions = confirmed;
  } else if (maybe.length) {
    // 只在"看起来可能有回放"的课上多花请求：编号最新的一两条试一下
    for (const cand of maybe.slice(0, 2)) {
      probed = true;
      try {
        const info = await subInfo(jwt, courseId, cand.subId);
        if (info.streams && info.streams.length) { sessions = maybe; break; }
      } catch (e) { /* 取不到就当没有 */ }
    }
  }

  // 教室：先用讲次自带的，没有就问 get-sub-info（只问最新一讲）
  let room = (sessions[0] && sessions[0].room) || '';
  if (!room && sessions.length) {
    try {
      const info = await subInfo(jwt, courseId, sessions[0].subId);
      room = (info && info.room) || '';
    } catch (e) {}
  }

  return {
    courseId,
    title: d.title,
    termName: d.termName,
    sessions,
    videoCount: sessions.length,
    totalSessions: all.length,
    room,
    latestDate: (sessions[0] && sessions[0].date) || '',
    probed,
  };
}

/** 过滤出能播的讲次（给扫课表那条兜底路径用） */
function filterRecordings(list) {
  const out = [];
  for (const s of arr(list)) {
    if (isLiveStatus(s.status)) continue;
    if (!isRecordingStatus(s.status) && !maybeRecording(s.status)) continue;
    out.push(s);
  }
  return out.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
}

// ---------------- 课表扫描（按课程名找课 + 列讲次） ----------------
const DAY_LIST = '/courseapi/v3/course-live-role/search-role-course-list';

function rowOf(period, r) {
  const type = text(pick(r, 'type', 'sub_type'));
  return {
    courseId: text(r.course_id),
    subId: text(r.sub_id),
    title: text(r.title),
    subTitle: text(r.sub_title),
    status: text(r.sub_status) || text(r.status),
    duration: num(r.sub_duration) || num(r.duration),
    startAt: num(r.start_at),
    endAt: num(r.end_at),
    teacher: (Array.isArray(r.teachers) && r.teachers[0] && (r.teachers[0].realname || r.teachers[0].name)) || text(r.lecturer_name),
    room: text(r.room_name) || text(r.sub_room_name),
    period: text(period.name),
    type,
    kind: kindOfSub(type),
    drag: text(r.is_allow_video_drag),
  };
}

/** 某一天（或某节课的时段）的课程行；keyword 走 like_title 过滤，响应会小很多 */
async function dayRows(jwt, date, keyword) {
  const params = {
    search_time: date, tenant: TENANT, need_time_quantum: 1, unique_course: 1,
    with_sub_duration: 1, with_sub_data: 1, has_frame: 1, nteacher: 0,
  };
  if (keyword) params.like_title = keyword;
  const body = await getJson(jwt, DAY_LIST, params);
  const out = [];
  for (const p of arr(body && body.list)) {
    for (const r of arr(p && p.list)) out.push(rowOf(p, r));
  }
  return out;
}

function ymdShift(dateStr, deltaDays) {
  const d = new Date(dateStr + 'T00:00:00Z');
  d.setUTCDate(d.getUTCDate() + deltaDays);
  return d.toISOString().slice(0, 10);
}

/** 并发跑一组日期，返回每天的行 */
async function scanDays(jwt, dates, keyword, concurrency = 4) {
  const out = [];
  let idx = 0;
  async function worker() {
    while (idx < dates.length) {
      const d = dates[idx++];
      try {
        const rows = await dayRows(jwt, d, keyword);
        out.push({ date: d, rows });
      } catch (e) {
        if (/会话被拒绝/.test(e.message)) throw e;
        out.push({ date: d, rows: [] });
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(concurrency, dates.length) }, worker));
  return out;
}

// ---------------- 学期时间窗 ----------------
/**
 * 从学期名推这段时间窗。北理的学期名长这样：
 *   "2024-2025 第一学期" / "2024-2025学年第二学期" / "2024-2025-1"
 * 窗口刻意贴紧教学周（开头结尾各留一点），这样"连续没课就提前收工"才不会被
 * 寒暑假的长空白卡住。
 */
function termWindow(name) {
  const s = text(name).replace(/\s+/g, '');
  if (!s) return null;
  const m = s.match(/(20\d{2})\s*[-—~至]\s*(20\d{2})/);
  if (!m) return null;
  const y1 = Number(m[1]);
  const isThird = /第三|3学期|夏季?/.test(s);
  const isSecond = /第二|2学期|Ⅱ|春季?|[-_ ]2$/.test(s);
  const isFirst = /第一|1学期|Ⅰ|秋季?|[-_ ]1$/.test(s);
  if (isThird) return { from: `${y1}-06-15`, to: `${y1}-09-15` };
  if (isSecond) return { from: `${y1 + 1}-02-15`, to: `${y1 + 1}-07-15` };
  if (isFirst) return { from: `${y1}-09-01`, to: `${y1 + 1}-01-31` };
  return { from: `${y1}-09-01`, to: `${y1 + 1}-07-15` };   // 学期不明：整个学年
}

/** [from, to] 之间的日期列表，**旧的在前**（从学期头开始扫） */
function datesBetween(from, to, cap = 240) {
  const out = [];
  const start = new Date(String(from) + 'T00:00:00Z');
  const end = new Date(String(to) + 'T00:00:00Z');
  if (isNaN(start) || isNaN(end) || start > end) return out;
  for (let d = new Date(start); d <= end && out.length < cap; d = new Date(d.getTime() + 86400 * 1000)) {
    out.push(d.toISOString().slice(0, 10));
  }
  return out;
}

/**
 * 扫一段日期范围内的课表，按 courseId 分组。
 * 这是"讲次直读"不可靠时最实在的一条路：一天一次请求，但**一次搜索只扫一遍**，
 * 结果同时覆盖列表里所有课程，每行还自带 sub_status 和 room_name。
 * @returns {Promise<Map<string, Array>>}
 */
async function scanDates(jwt, keyword, dates, { concurrency = 4, maxEmpty = 35 } = {}) {
  const map = new Map();
  let emptyStreak = 0;
  const batch = 7;

  for (let base = 0; base < dates.length; base += batch) {
    const part = dates.slice(base, base + batch);
    const scanned = await scanDays(jwt, part, keyword, concurrency);
    let hit = 0;
    for (const { date, rows } of scanned) {
      for (const r of rows) {
        if (!r.courseId) continue;
        if (keyword && r.title && !r.title.includes(keyword)) continue;
        if (isLiveStatus(r.status)) continue;
        const key = String(r.courseId);
        if (!map.has(key)) map.set(key, []);
        const list = map.get(key);
        if (list.some(x => x.subId === r.subId)) continue;
        list.push({ ...r, date: r.startAt ? dateOf(r.startAt) : date });
        hit++;
      }
    }
    emptyStreak = hit ? 0 : emptyStreak + batch;
    if (emptyStreak >= maxEmpty) break;
  }
  for (const list of map.values()) list.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
  return map;
}

/** 按"最近 N 天"扫（不知道学期时间窗时的兜底） */
async function scanByKeyword(jwt, keyword, { days = 100, concurrency = 4, maxEmpty = 21 } = {}) {
  const today = new Date().toISOString().slice(0, 10);
  const dates = [];
  for (let i = 0; i < days; i++) dates.push(ymdShift(today, -i));
  return scanDates(jwt, keyword, dates, { concurrency, maxEmpty });
}

/** 兜底：按关键词在近期课表里找课（只统计有录像的讲次） */
async function findCourses(jwt, keyword, { days = 21 } = {}) {
  const today = new Date().toISOString().slice(0, 10);
  const dates = [];
  for (let i = 0; i < days; i++) dates.push(ymdShift(today, -i));
  const scanned = await scanDays(jwt, dates, keyword, 4);

  const map = new Map();
  for (const { date, rows } of scanned) {
    for (const r of rows) {
      if (!r.courseId) continue;
      if (keyword && !r.title.includes(keyword)) continue;
      if (!isRecordingStatus(r.status) && !maybeRecording(r.status)) continue;
      const key = r.courseId;
      const cur = map.get(key) || {
        courseId: r.courseId, title: r.title, teacher: r.teacher, kind: r.kind || 'record',
        termId: '', termName: '', sessions: 0, lastDate: date, cover: '', room: r.room || '',
      };
      cur.sessions++;
      if (!cur.room && r.room) cur.room = r.room;
      if (date > cur.lastDate) cur.lastDate = date;
      map.set(key, cur);
    }
  }
  return [...map.values()].sort((a, b) => b.sessions - a.sessions);
}

/** 兜底：按天扫某门课的讲次 */
async function scanSessions(jwt, courseId, keyword, { days = 130, maxEmpty = 30 } = {}) {
  const today = new Date().toISOString().slice(0, 10);
  const found = [];
  let emptyStreak = 0;
  const batch = 7;

  for (let base = 0; base < days; base += batch) {
    const dates = [];
    for (let i = base; i < Math.min(base + batch, days); i++) dates.push(ymdShift(today, -i));
    const scanned = await scanDays(jwt, dates, keyword, 4);
    let hit = 0;
    for (const { date, rows } of scanned) {
      for (const r of rows) {
        if (r.courseId !== String(courseId)) continue;
        if (found.some(x => x.subId === r.subId)) continue;
        found.push({
          ...r,
          date: r.startAt ? dateOf(r.startAt) : date,
        });
        hit++;
      }
    }
    emptyStreak = hit ? 0 : emptyStreak + batch;
    if (emptyStreak >= maxEmpty) break;
  }
  found.sort((a, b) => (b.startAt || 0) - (a.startAt || 0));
  return found;
}

// ---------------- /play/ 签名 ----------------
function signPath(pathname, id, ts) {
  const reversed = String(id.phone || '').split('').reverse().join('');
  const digest = md5(`${pathname}${id.userId}${id.tenantId}${reversed}${ts}`);
  return `${id.userId}-${ts}-${digest}`;
}
function signedUrl(rawUrl, id) {
  const u = new URL(rawUrl);
  u.searchParams.delete('t');
  u.searchParams.set('t', signPath(u.pathname, id, Math.floor(Date.now() / 1000)));
  return u.toString();
}

module.exports = {
  profile, myCourses, weekSchedule, subInfo, signedUrl, signPath, apiUrl, req, dayRows,
  findCourses, scanSessions, scanByKeyword, scanDates, termWindow, datesBetween,
  termList, searchCourses, courseDetail, courseVideos, filterRecordings,
  scheduleMap, isRecordingStatus, maybeRecording, isLiveStatus, tryJson, kindOfSub, dateOf, tsOf, Y2, TENANT, UA,
};
