// 从官网 logo.svg 里抠出「图形标记」（前 4 条渐变色路径），生成可缩放的 mark.svg
// 用法：python tools/make-icon.py
const fs = require('fs');
const path = require('path');

const dir = path.join(__dirname, '..', 'android', 'icon');
const src = fs.readFileSync(path.join(dir, 'logo_full.svg'), 'utf8');

// 1) 四个渐变
const grads = [...src.matchAll(/<linearGradient[\s\S]*?<\/linearGradient>/g)].map(m => m[0]);
if (grads.length < 4) throw new Error('渐变没抠到：' + grads.length);

// 2) 前四条引用渐变的 path（就是这个标记）
const paths = [...src.matchAll(/<path[^>]*fill="url\(#linearGradient-\d\)"[^>]*><\/path>/g)].map(m => m[0]);
if (paths.length < 4) throw new Error('标记路径没抠到：' + paths.length);

// 标记实际占的框（从路径里看是 0..37.66 × 0..39.91）
const W = 37.655, H = 39.907;
// 四周留 12% 的白边：桌面任务栏 / 手机桌面圆角裁切都不会切到图形
const pad = 0.12 * Math.max(W, H);
const VB = `${-pad} ${-pad} ${W + pad * 2} ${H + pad * 2}`;
const svg = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="${VB}"
     preserveAspectRatio="xMidYMid meet">
  <defs>
    ${grads.join('\n    ')}
  </defs>
  <g fill-rule="nonzero">
    ${paths.join('\n    ')}
  </g>
</svg>
`;
fs.writeFileSync(path.join(dir, 'mark.svg'), svg);
console.log('写出 android/icon/mark.svg（' + (svg.length / 1024).toFixed(1) + ' KB）');
