/**
 * 把构建产物传到 GitHub Release（只发 Go 版单文件 exe）。
 *
 *   $env:GITHUB_TOKEN="ghp_..."; node tools/release.js v1.5.0
 *   node tools/release.js v1.5.0 --dry          # 只看要传什么，不需要 token
 *
 * 只用 Node 内置 https，不依赖 gh CLI。已存在的同名附件会先删掉再传。
 */
const fs = require('fs');
const path = require('path');
const https = require('https');
const { execFileSync } = require('child_process');

const root = path.join(__dirname, '..');
const DIST = path.join(root, 'dist');
const DRY = process.argv.includes('--dry');
const TAG = process.argv.slice(2).find(a => /^v?\d/.test(a)) || 'v1.5.0';
const TOKEN = process.env.GITHUB_TOKEN || process.env.GH_TOKEN || '';

// 仓库从 git remote 里读，避免写死
function repoSlug() {
  try {
    const url = execFileSync('git', ['remote', 'get-url', 'origin'], { cwd: root, encoding: 'utf8' }).trim();
    const m = /github\.com[:/]([^/]+)\/([^/.]+)/.exec(url);
    if (m) return { owner: m[1], repo: m[2] };
  } catch (e) {}
  return { owner: 'mmck111', repo: 'yanhekt-player' };
}

const ASSETS = [
  ['yanhekt.exe', '电脑端：16 MB 单文件，双击即用，零依赖'],
  ['yanhekt.apk', '手机端：Android 6.0+ / arm64，和电脑端同一个引擎'],
  ['codes.html', '课程代号查询：单文件 0.8 MB，手机浏览器直接打开，离线可搜'],
];

function api(method, url, body, extraHeaders = {}) {
  return new Promise((resolve) => {
    const u = new URL(url);
    const payload = body ? Buffer.from(typeof body === 'string' ? body : JSON.stringify(body)) : null;
    const headers = Object.assign({
      'user-agent': 'yanhekt-release',
      accept: 'application/vnd.github+json',
      'x-github-api-version': '2022-11-28',
    }, extraHeaders);
    if (TOKEN) headers.authorization = 'Bearer ' + TOKEN;
    if (payload) { headers['content-type'] = 'application/json'; headers['content-length'] = payload.length; }
    const r = https.request({ host: u.hostname, path: u.pathname + u.search, method, headers }, (res) => {
      const chunks = [];
      res.on('data', d => chunks.push(d));
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8');
        let json = null;
        try { json = JSON.parse(text); } catch (e) {}
        resolve({ status: res.statusCode, json, text });
      });
    });
    r.on('error', e => resolve({ status: 0, text: e.message }));
    if (payload) r.write(payload);
    r.end();
  });
}

const NOTES = `## 支持的功能

- **录播播放**：全景（讲台/投影）、课件（屏幕）、双画面三种机位，双画面自动对齐、声音走全景
- **无权限课程也能看**：不用选课，账号能登录，平台上任何有录像的课都能搜到并播放
- **往年课程**：内置 2004 年至今 38 个学期的课程库，往届的课也能按课程名 / 教师名搜到
- **搜索**：课程名、教师名（可空格组合，如 \`数值分析 孙新\`）、学期筛选；按匹配度排序，同分再按录像数
- **下载**：把当前这一讲拼成单个文件存到本地；手机端存进系统「下载」，取消即删、面板里也能直接删
- **直播**：不用登录，列出全校正在播的直播课，多机位、每分钟自动刷新
- **新版平台**（\`/y2\`）：另一套入口，播放快 3~5 倍，MP4 / HLS 都能下载；课程库随包内置，搜索和本地一样快
- **自动登录**：电脑端从浏览器已有的登录取凭证；手机端点一下跳官网登录，登录好后按返回键回播放器

## 三个文件怎么选

| 文件 | 给谁 | 说明 |
|---|---|---|
| \`yanhekt.exe\` | 电脑（Windows） | 双击就能用，不装任何东西 |
| \`yanhekt.apk\` | 手机（Android 6.0+，arm64） | 和电脑端同一个引擎，界面是手机版；装的时候允许「未知来源」 |
| \`codes.html\` | 手机 / 电脑浏览器 | 查旧版延河的课程代号：单文件、离线、不用服务器，输入课名就能看到 \`yanhekt.cn/course/代号\` |

## 本次更新

- 手机端上线：APK 打包同一个 Go 引擎，功能与 exe 对齐（录播 / 直播 / 下载 / 新版平台）
- 手机端登录：点顶栏「登录」→ 跳对应官网登录 → 登好按返回键回播放器即可（登录态后台自动接手）
- 手机端下载：取消即删文件、列表里可直接删、可一键清理，不再堆垃圾
- 直播：修了手机端时区问题（以前会把正在直播的课判成「未开始」甚至整场过滤掉）
- 新版搜索：课程库随包内置，第一次打开就是本地检索，和电脑端一个速度
- 汉堡栏：点课程先出讲次、选完讲次才收起，讲次页顶部可一键回到课程列表
- 首次运行会内置课程库，无需等待爬取
- exe / APK / 网页标签页都换成了延河课堂的标志图标
- 手机端登录 / 直播 / 下载的一串修复（详见提交记录）

## 安装

**电脑**：下载 \`yanhekt.exe\` 双击即可。Windows 第一次会拦一下：
右键 → 属性 → 勾「解除锁定」，或启动时点「更多信息 → 仍要运行」。

**手机**：下载 \`yanhekt.apk\` 安装（首次需允许「安装未知应用」）。打开后：
点顶栏「登录」→ 跳官网登录 → 回来即可搜课/播放；下载的文件在系统「下载」目录。

## 说明

- 电脑端只监听 \`127.0.0.1\`；手机端引擎只监听手机本机 \`127.0.0.1:8799\`，外部访问不到
- 账号密码不经过本程序，登录态只写进本机 cookie
- 课程内容版权归学校与授课教师所有，别外传
- **非官方工具**，和学校 / 延河课堂官方无关；图标用的是延河课堂的官方标志，只为便于识别，版权归北京理工大学所有
`;

async function ensureRelease(owner, repo) {
  const get = await api('GET', `https://api.github.com/repos/${owner}/${repo}/releases/tags/${TAG}`);
  if (get.status === 200 && get.json) return get.json;
  if (get.status !== 404) return { error: `查询 release 失败 HTTP ${get.status} ${get.text.slice(0, 120)}` };
  const created = await api('POST', `https://api.github.com/repos/${owner}/${repo}/releases`, {
    tag_name: TAG,
    target_commitish: 'main',
    name: `${TAG.replace(/^v/, '')} · 延河课堂在线播放器`,
    body: NOTES,
    draft: false,
    prerelease: false,
  });
  if (created.status >= 200 && created.status < 300 && created.json) return created.json;
  return { error: `创建 release 失败 HTTP ${created.status} ${created.text.slice(0, 160)}` };
}

async function uploadAsset(owner, repo, releaseId, file, existing) {
  const name = path.basename(file);
  const data = fs.readFileSync(file);
  const q = `/repos/${owner}/${repo}/releases/${releaseId}/assets`;
  if (existing) {
    const del = await api('DELETE', `https://api.github.com${q}/${existing.id}`);
    if (del.status !== 204 && del.status !== 200) console.log(`    ! 删除旧附件 ${name} 失败 HTTP ${del.status}`);
  }
  return new Promise((resolve) => {
    const u = new URL(`https://uploads.github.com${q}?name=${encodeURIComponent(name)}`);
    const r = https.request({
      host: u.hostname, path: u.pathname + u.search, method: 'POST',
      headers: {
        authorization: 'Bearer ' + TOKEN,
        'user-agent': 'yanhekt-release',
        accept: 'application/vnd.github+json',
        'content-type': 'application/octet-stream',
        'content-length': data.length,
      },
    }, (res) => {
      const chunks = [];
      res.on('data', d => chunks.push(d));
      res.on('end', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString('utf8').slice(0, 200) }));
    });
    r.on('error', e => resolve({ status: 0, text: e.message }));
    r.end(data);
  });
}

(async () => {
  const { owner, repo } = repoSlug();
  console.log(`仓库   : ${owner}/${repo}`);
  console.log(`Release: ${TAG}`);

  const files = ASSETS.map(([name, desc]) => ({ name, desc, file: path.join(DIST, name) }))
    .filter(f => {
      if (fs.existsSync(f.file)) return true;
      console.log(`  跳过（不存在）: ${f.name}`);
      return false;
    });
  if (!files.length) { console.log('dist/ 里没有可传的产物，先构建'); process.exit(1); }
  console.log('\n要传的产物:');
  for (const f of files) console.log(`  ${f.name.padEnd(30)} ${(fs.statSync(f.file).size / 1048576).toFixed(2)} MB  ${f.desc}`);

  if (DRY) { console.log('\n--dry：到此为止（没有改动 GitHub）'); return; }
  if (!TOKEN) { console.log('\n需要 token：$env:GITHUB_TOKEN="ghp_..."; node tools/release.js ' + TAG); process.exit(1); }

  const rel = await ensureRelease(owner, repo);
  if (rel.error) { console.log('✗ ' + rel.error); process.exit(1); }
  console.log(`\nrelease id = ${rel.id}（${rel.html_url || ''}）`);
  const existing = rel.assets || [];

  let bad = 0;
  for (const f of files) {
    const old = existing.find(a => a.name === f.name);
    const up = await uploadAsset(owner, repo, rel.id, f.file, old);
    if (up.status >= 200 && up.status < 300) console.log(`  ✓ ${f.name}`);
    else { bad++; console.log(`  ✗ ${f.name} HTTP ${up.status} ${up.text}`); }
  }
  console.log(bad ? `\n${bad} 个附件上传失败` : `\n全部完成：${rel.html_url || TAG}`);
  process.exit(bad ? 1 : 0);
})();
