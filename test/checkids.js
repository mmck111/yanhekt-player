/**
 * 静态检查：页面里 $("#xxx") 引用到的 id 必须真的存在。
 * 这类错会让整段脚本在运行到 null.onclick 时中断，页面就永远停在"正在检查登录状态…"。
 *
 *   node test/checkids.js
 */
const fs = require('fs');
const path = require('path');

const file = path.join(__dirname, '..', 'public', 'index.html');
const s = fs.readFileSync(file, 'utf8');

const used = new Set([...s.matchAll(/\$\('#([A-Za-z0-9_-]+)'\)/g)].map(m => m[1]));
const ids = new Set([...s.matchAll(/id="([A-Za-z0-9_-]+)"/g)].map(m => m[1]));
const missing = [...used].filter(x => !ids.has(x));

if (missing.length) {
  console.log('✗ 代码里引用了页面中不存在的 id：' + missing.join(', '));
  process.exit(1);
}
console.log('✓ 页面 id 引用全部存在（' + used.size + ' 个）');
