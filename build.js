/**
 * 打包准备：把 signer.js 内联进 server.js，生成 dist/app.js 与 sea-config.json。
 * 之后用 node --experimental-sea-config + postject 注入成单文件 exe：
 *
 *   node build.js
 *   node --experimental-sea-config dist/sea-config.json
 *   copy <node.exe> dist/延河课堂播放器.exe
 *   npx postject dist/延河课堂播放器.exe NODE_SEA_BLOB dist/sea-prep.blob \
 *     --sentinel-fuse NODE_SEA_FUSE_fce680ab2cc467b6e072b8b5df1996b2
 */
const fs = require('fs');
const path = require('path');

const root = __dirname;
const dist = path.join(root, 'dist');
fs.mkdirSync(dist, { recursive: true });

const signerSrc = fs.readFileSync(path.join(root, 'signer.js'), 'utf8');
const yanhe2Src = fs.readFileSync(path.join(root, 'yanhe2.js'), 'utf8');
let serverSrc = fs.readFileSync(path.join(root, 'server.js'), 'utf8');

// server.js 里的 require 换成内联版（打包后没有 ./signer.js / ./yanhe2.js 这两个文件）
const marker = "const signer = require('./signer');";
if (!serverSrc.includes(marker)) throw new Error('server.js 里找不到 signer 的 require');
serverSrc = serverSrc.replace(marker, 'const signer = __signer;');

const y2marker = "const y2 = require('./yanhe2');";
if (!serverSrc.includes(y2marker)) throw new Error('server.js 里找不到 yanhe2 的 require');
serverSrc = serverSrc.replace(y2marker, 'const y2 = __yanhe2;');

const bundle = `/* 延河课堂播放器 · 单文件构建产物（由 build.js 生成，别手改） */
const __signer = (() => {
  const module = { exports: {} };
  const exports = module.exports;
  (function (module, exports, require) {
${signerSrc}
  })(module, exports, require);
  return module.exports;
})();

const __yanhe2 = (() => {
  const module = { exports: {} };
  const exports = module.exports;
  (function (module, exports, require) {
${yanhe2Src}
  })(module, exports, require);
  return module.exports;
})();

${serverSrc}
`;

fs.writeFileSync(path.join(dist, 'app.js'), bundle);

const seaConfig = {
  main: path.join(dist, 'app.js').replace(/\\/g, '/'),
  output: path.join(dist, 'sea-prep.blob').replace(/\\/g, '/'),
  disableExperimentalSEAWarning: true,
  useSnapshot: false,
  useCodeCache: false,
  assets: {
    'index.html': path.join(root, 'public', 'index.html').replace(/\\/g, '/'),
    'y2.html': path.join(root, 'public', 'y2.html').replace(/\\/g, '/'),
    'hls.min.js': path.join(root, 'public', 'hls.min.js').replace(/\\/g, '/'),
    'catalog.json.gz': path.join(root, 'public', 'catalog.json.gz').replace(/\\/g, '/'),
  },
};
fs.writeFileSync(path.join(dist, 'sea-config.json'), JSON.stringify(seaConfig, null, 2));

console.log('生成 dist/app.js:', (bundle.length / 1024).toFixed(1), 'KB');
console.log('生成 dist/sea-config.json');
