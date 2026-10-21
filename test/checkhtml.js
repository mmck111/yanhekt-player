// 把 y2.html / index.html 里的内联 script 抽出来做语法检查
const fs = require('fs');
const path = require('path');
const vm = require('vm');
const targets = process.argv.slice(2);
let bad = 0;
for (const t of targets) {
  const html = fs.readFileSync(t, 'utf8');
  const blocks = [...html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi)].map(m => m[1]);
  console.log(t, 'inline scripts:', blocks.length, blocks.map(b => b.length).join(','));
  blocks.forEach((code, i) => {
    try { new vm.Script(code, { filename: `${t}#${i}` }); }
    catch (e) { bad++; console.log('  SYNTAX ERROR in block', i, '→', e.message); }
  });
}
process.exit(bad ? 1 : 0);
