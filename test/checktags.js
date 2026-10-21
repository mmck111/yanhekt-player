/**
 * 粗粒度标签配对检查：能抓出编辑 HTML 时漏掉的 </div>、写错的 <details> 之类。
 * 用法：node test/checktags.js public/index.html [更多文件...]
 * 只做结构配对，不当 HTML 解析器（script/style/template 内容会被跳过）。
 */
const fs = require('fs');

const VOID = new Set(['area', 'base', 'br', 'col', 'embed', 'hr', 'img', 'input',
  'link', 'meta', 'param', 'source', 'track', 'wbr']);

function stripInner(html) {
  // 把 <script>/<style> 的内容挖掉，避免把 JS 字符串里的尖括号当成标签
  return html.replace(/<(script|style)\b[^>]*>([\s\S]*?)<\/\1>/gi, (m, tag) => `<${tag}></${tag}>`);
}

function check(file) {
  const raw = fs.readFileSync(file, 'utf8');
  const html = stripInner(raw);
  const stack = [];
  const problems = [];
  const re = /<(\/?)([a-zA-Z][a-zA-Z0-9]*)((?:[^>"']|"[^"]*"|'[^']*')*?)(\/?)>/g;
  let m;
  while ((m = re.exec(html))) {
    const [full, slash, rawName, attrs, selfClose] = m;
    const name = rawName.toLowerCase();
    if (VOID.has(name) || selfClose === '/') continue;
    if (name === '!doctype' || rawName.startsWith('!')) continue;
    if (!slash) {
      // 记下位置，报错时能定位
      const line = html.slice(0, m.index).split('\n').length;
      stack.push({ name, line, text: full.slice(0, 60) });
    } else {
      if (!stack.length) { problems.push(`第 ${html.slice(0, m.index).split('\n').length} 行：多出来的 </${name}>`); continue; }
      const top = stack[stack.length - 1];
      if (top.name === name) { stack.pop(); continue; }
      // 允许少量"隐式闭合"的容器，其余算错
      const idx = stack.map(s => s.name).lastIndexOf(name);
      if (idx === -1) {
        problems.push(`第 ${html.slice(0, m.index).split('\n').length} 行：</${name}> 没有对应的开标签（当前未闭合的是 <${top.name}>，第 ${top.line} 行）`);
      } else {
        for (let i = stack.length - 1; i > idx; i--) {
          problems.push(`第 ${stack[i].line} 行的 <${stack[i].name}> 没闭合（被 </${name}> 硬关上）`);
        }
        stack.length = idx;
      }
    }
  }
  for (const s of stack) problems.push(`第 ${s.line} 行的 <${s.name}> 没有闭合`);
  return problems;
}

let bad = 0;
for (const f of process.argv.slice(2)) {
  const problems = check(f);
  if (!problems.length) { console.log(`${f}: 标签配对 OK`); continue; }
  bad++;
  console.log(`${f}: ${problems.length} 个问题`);
  problems.slice(0, 20).forEach(p => console.log('  ' + p));
}
process.exit(bad ? 1 : 0);
