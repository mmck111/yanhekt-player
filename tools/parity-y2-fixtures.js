/**
 * y2 解析层对拍：把 aita 的响应换成固定 fixture，让 yanhe2.js 的解析逻辑跑一遍，
 * 结果写进 go\testdata\y2-fixtures.json；Go 的 y2_fixture_test.go 再拿同一份 body
 * 喂给对应的解析函数，逐字段比对。
 *
 *   node tools\parity-y2-fixtures.js
 *
 * 这样"登录后"的那些解析（profile / 学期 / 检索 / 讲次 / 取流 / 课表）也能在没有真
 * JWT 的环境里验证，而不是只靠代码审查。
 */
const fs = require('fs');
const path = require('path');
const https = require('https');
const { EventEmitter } = require('events');

const root = path.join(__dirname, '..');
const y2 = require(path.join(root, 'yanhe2.js'));

// ---------------- 把 https.request 换成"照本宣科"的假实现 ----------------
let currentBody = null;
const realRequest = https.request;
https.request = function fakeRequest(url, opts, cb) {
  const res = new EventEmitter();
  res.statusCode = 200;
  res.headers = { 'content-type': 'application/json' };
  const payload = Buffer.from(JSON.stringify(currentBody === undefined ? null : currentBody));
  const req = new EventEmitter();
  req.setTimeout = () => req;
  req.write = () => true;
  req.destroy = () => {};
  req.end = () => {
    process.nextTick(() => {
      cb(res);
      process.nextTick(() => {
        res.emit('data', payload);
        res.emit('end');
      });
    });
    return req;
  };
  return req;
};

/** 用固定 body 跑一次 yanhe2.js 的某个函数；出错就把错误文本当成结果 */
async function run(body, fn) {
  currentBody = body;
  try {
    return { ok: true, value: await fn() };
  } catch (e) {
    return { ok: false, error: e.message };
  }
}

// ---------------- fixtures ----------------
const JWT = 'eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.fake';

const CASES = [
  {
    name: 'profile 常规（tenant_id 缺失要回落 21、phone 要 trim）',
    fn: 'profile',
    body: { code: 0, params: { id: 12345, phone: ' 13800138000 ', account: 's12345', realname: '张三' } },
  },
  {
    name: 'profile 缺 id',
    fn: 'profile',
    body: { code: 0, params: { phone: '13800138000' } },
  },
  {
    name: 'profile 没有 params',
    fn: 'profile',
    body: { code: 0 },
  },
  {
    name: 'termList 平铺列表',
    fn: 'termList',
    body: { code: 0, params: { list: [{ id: 1, term_name: '2024-2025 第一学期' }, { term_id: 2, termName: '2024-2025 第二学期' }] } },
  },
  {
    name: 'termList 学年分组（名字没写学年要补）',
    fn: 'termList',
    body: { code: 0, result: { data: { '2024-2025': [{ id: 3, term_name: '第一学期' }, { id: 4, term_name: '2024-2025 第二学期' }] } } },
  },
  {
    name: 'termList 空',
    fn: 'termList',
    body: { code: 0, params: {} },
  },
  {
    name: 'searchCourses 常规（含 teachers、sessions 为 null、直播类课程）',
    fn: 'searchCourses',
    body: {
      code: 1000,
      data: {
        total: 2,
        list: [
          {
            id: 96123, title: '数值分析', teachers: [{ realname: '李四' }, { name: '王五' }],
            term: 't1', term_name: '2024-2025 第一学期', sub_count: '5', thumb: 'u1', type: 'record',
          },
          {
            course_id: '96124', name: '数值分析（实验）', teacher: '赵六', term_id: 't2',
            TermName: '2024-2025 第二学期', type: 'course_live',
          },
        ],
      },
    },
  },
  {
    name: 'searchCourses 空结果',
    fn: 'searchCourses',
    body: { code: 0, data: { total: 0, list: [] } },
  },
  {
    name: 'searchCourses code 不对',
    fn: 'searchCourses',
    body: { code: 500, msg: '拒绝', data: { list: [{ id: 1, title: 'x' }] } },
  },
  {
    name: 'courseDetail 常规（sub_list + 各种字段名）',
    fn: 'courseDetail',
    body: {
      code: 0,
      data: {
        title: '数值分析', term_name: '2024-2025 第一学期',
        sub_list: [
          {
            id: 1, title: '第1讲', start_at: 1725120000, duration: 5400, sub_status: '6',
            room_name: '教A101', lecturer_name: '李四', type: 'record', term: 't1', term_name: '2024-2025 第一学期',
          },
          {
            sub_id: 2, sub_title: '第2讲', start_time: '2024-09-08 08:00:00', sub_duration: 5400,
            status: '5', classroom: '教A102', realname: '李四',
          },
          { subject_id: 3, sub_title: '没状态的一讲' },
        ],
      },
    },
  },
  {
    name: 'courseDetail origin_sub_list',
    fn: 'courseDetail',
    body: {
      code: 200,
      data: {
        course_title: '线性代数', termName: '2024-2025 第二学期',
        origin_sub_list: [{ id: 11, title: '第1讲', start_at: '2025-03-01 08:00:00', sub_status: '6', room_name: '教B201' }],
      },
    },
  },
  {
    name: 'courseDetail 讲次藏在 JSON 字符串里',
    fn: 'courseDetail',
    body: {
      code: 0,
      data: {
        title: '概率论',
        sub_data: JSON.stringify([
          { id: 21, title: '第1讲', start_at: 1740000000, sub_status: '6' },
          { id: 22, title: '第2讲', start_at: 1740086400, sub_status: '5' },
        ]),
      },
    },
  },
  {
    name: 'courseDetail code 非 0',
    fn: 'courseDetail',
    body: { code: 401, data: { sub_list: [{ id: 1, sub_status: '6' }] } },
  },
  {
    name: 'subInfo 常规（同类型只留第一路、m3u8/mp4/aac 判定、按 camera<screen<room 排）',
    fn: 'subInfo',
    body: {
      code: 0,
      data: {
        sub_status: '6', type: 'record', course_title: '数值分析', sub_title: '第1讲',
        lecturer_name: '李四', room_name: '教A101',
        start_at: 1725120000, end_at: 1725125400, duration: 5400,
        video_list: [
          { type: '4', preview_url: 'https://x/room.mp4?t=1' },
          { type: '3', preview_url: 'https://x/cam.m3u8?t=2' },
          { type: '2', preview_url: 'https://x/screen' },
          { type: '3', preview_url: 'https://x/cam2.mp4' },
          { type: '2', preview_url: 'https://x/screen.mp4' },
        ],
        audio_list: [{ url: 'https://x/a.mp3' }, { url: 'https://x/a.aac?t=9' }],
      },
    },
  },
  {
    name: 'subInfo code 是字符串（Node 的 !== 0 会判失败）',
    fn: 'subInfo',
    body: { code: '0', data: { sub_status: '6' } },
  },
  {
    name: 'weekSchedule 常规',
    fn: 'weekSchedule',
    body: {
      result: {
        list: [
          {
            day: '2024-09-02',
            course: [
              {
                id: 11, course_id: 'c1', sub_title: '第1讲', course_title: '数值分析',
                start_at: 1725120000, end_at: 1725125400, duration: 5400, sub_status: '6',
                room_name: '教A101', lecturer_name: '李四',
              },
              { course_id: 'c2', duration: 3600, sub_duration: 5400 },
            ],
          },
          { day: '2024-09-03', course: [{ id: 12, course_id: 'c1', start_at: 1725206400, sub_duration: 3000, status: '5' }] },
        ],
      },
    },
  },
  {
    name: 'dayRows 常规（teachers 优先、period、drag）',
    fn: 'dayRows',
    body: {
      list: [
        {
          name: '第1-2节',
          list: [
            {
              course_id: 'c1', sub_id: 21, title: '数值分析', sub_title: '第1讲', sub_status: '6',
              sub_duration: 5400, start_at: 1725120000, end_at: 1725125400,
              teachers: [{ realname: '李四' }], room_name: '教A101', type: 'record', is_allow_video_drag: '1',
            },
            {
              course_id: 'c1', sub_id: 22, title: '数值分析', sub_status: '5', duration: 1800,
              start_at: 1725206400, teachers: [{ name: '王五' }], sub_room_name: '教A102', is_allow_video_drag: '0',
            },
          ],
        },
      ],
    },
  },
  {
    name: 'myCourses 常规',
    fn: 'myCourses',
    body: {
      code: 1000,
      params: {
        result: {
          data: [
            { Id: 1, Title: '数值分析', Teacher: '李四', TermName: '2024-2025 第一学期', Thumb: 'u' },
            { course_id: '2', course_title: '线性代数', lecturer_name: '王五' },
            { Title: '没有 id 的课' },
          ],
        },
      },
    },
  },
];

// ---------------- 跑 ----------------
const callers = {
  profile: () => y2.profile(JWT),
  termList: () => y2.termList(JWT),
  searchCourses: () => y2.searchCourses(JWT, { q: '数值分析', perPage: 20 }),
  courseDetail: () => y2.courseDetail(JWT, '96123', 's12345'),
  subInfo: () => y2.subInfo(JWT, '96123', '1'),
  weekSchedule: () => y2.weekSchedule(JWT, 12345, '2024-09-02', '2024-09-08'),
  dayRows: () => y2.dayRows(JWT, '2024-09-02', '数值分析'),
  myCourses: () => y2.myCourses(JWT),
};

// 子进程模式：只跑第 N 个用例，把结果打到 stdout。
// （每个用例单独 fork 一次：termList 有 1 小时进程内缓存，同进程跑会串味）
if (process.argv[2] === '--one') {
  const c = CASES[Number(process.argv[3])];
  run(c.body, callers[c.fn]).then(r => {
    process.stdout.write(JSON.stringify(r));
  });
} else {
  const { execFileSync } = require('child_process');
  const out = { cases: [] };
  CASES.forEach((c, i) => {
    const raw = execFileSync(process.execPath, [__filename, '--one', String(i)], {
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'inherit'],
    });
    out.cases.push({ name: c.name, fn: c.fn, body: c.body, ...JSON.parse(raw) });
  });
  const file = path.join(root, 'go', 'testdata', 'y2-fixtures.json');
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(out, null, 2) + '\n', 'utf8');
  console.log('写入 ' + file + '：' + out.cases.length + ' 个用例');
  for (const c of out.cases) {
    console.log('  - ' + c.name + (c.ok ? '' : '（错误：' + c.error + '）'));
  }
}
