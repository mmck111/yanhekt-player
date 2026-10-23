/**
 * 打一个「单文件版」课程代号查询页：课程库（catalog.json.gz）以 base64 内联进去，
 * 生成 dist/codes.html —— 拷到手机上直接用浏览器打开就能搜，不需要任何服务器。
 *
 *   node tools/make-codes.js
 *
 * 需要托管给同学用时，直接把 public/codes.html + public/catalog.json.gz 放到任意静态站点，
 * 或者只上传 dist/codes.html 这一个文件（它自带数据）。
 */
const fs = require('fs');
const path = require('path');

const root = path.join(__dirname, '..');
const src = path.join(root, 'public', 'codes.html');
const gz = path.join(root, 'public', 'catalog.json.gz');
const out = path.join(root, 'dist', 'codes.html');

let html = fs.readFileSync(src, 'utf8');
const b64 = fs.readFileSync(gz).toString('base64');

const marker = '<script>';
const at = html.lastIndexOf(marker);
if (at < 0) throw new Error('codes.html 里找不到 <script> 插入点');

// 数据脚本必须排在主脚本前面
html = html.slice(0, at)
  + '<script>window.__CAT_GZ__="' + b64 + '";</script>\n'
  + html.slice(at);

fs.mkdirSync(path.dirname(out), { recursive: true });
fs.writeFileSync(out, html);
const mb = (fs.statSync(out).size / 1024 / 1024).toFixed(2);
console.log('写出 ' + path.relative(root, out) + '  ' + mb + ' MB（自带课程库，无需服务器）');
