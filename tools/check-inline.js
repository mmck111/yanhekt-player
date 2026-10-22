// 把两个页面的内联脚本抽出来做语法检查（页面里没法直接 node --check）
const fs = require('fs');
const { execFileSync } = require('child_process');
const tmp = process.env.TEMP + '\\' + 'inline-check.js';
for (const f of ['public/index.html', 'public/y2.html']) {
  const s = fs.readFileSync(f, 'utf8');
  const m = s.match(/<script>\n([\s\S]*?)<\/script>/);
  if (!m) { console.log(f + ': 没找到内联脚本'); process.exit(1); }
  fs.writeFileSync(tmp, m[1]);
  try {
    execFileSync(process.execPath, ['--check', tmp], { stdio: 'pipe' });
    console.log(f + ': 脚本语法 OK（' + m[1].length + ' 字节）');
  } catch (e) {
    console.log(f + ': 脚本语法错误\n' + String(e.stderr || e.message));
    process.exit(1);
  }
}
