/**
 * 新版平台登录：从"用户可能粘进来的各种东西"里抠 JWT 的回归测试。
 *
 *   node test/checkjwt.js
 *
 * _token 的形态在官网改过几版（裸 JWT / 整段 cookie / URL 编码 / PHP 序列化），
 * 这几种都必须能抠出同一段 JWT，否则登录会静默失败。
 */
const y2 = require('../yanhe2');

const JWT = 'eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6MTQ4MjI2fQ.abc+/def-ghi_jkl=';

const cases = [
  ['裸 JWT', JWT, JWT],
  ['整段 cookie', '_token=' + JWT + '; locale=zh_cn', JWT],
  ['URL 编码的 cookie', '_token=' + encodeURIComponent(JWT) + '; locale=zh_cn', JWT],
  ['PHP 序列化的值', 'a:2:{i:0;s:6:"_token";i:1;s:' + JWT.length + ':"' + JWT + '";}', JWT],
  ['编码过的序列化值', encodeURIComponent('a:2:{i:0;s:6:"_token";i:1;s:' + JWT.length + ':"' + JWT + '";}'), JWT],
  ['带引号的 JSON', '{"_token":"' + JWT + '"}', JWT],
  ['前后有空白', '   ' + JWT + '\n', JWT],
  ['空串', '', ''],
];

let pass = 0, fail = 0;
for (const [name, input, want] of cases) {
  const got = y2.extractJwt(input);
  if (got === want) { pass++; console.log('  ✓ ' + name); }
  else { fail++; console.log('  ✗ ' + name + '  → 得到 ' + JSON.stringify(String(got).slice(0, 30))); }
}

// 鉴权写法探测的开关
y2.setAuthStyle('plain');
if (y2.getAuthStyle() !== 'plain') { fail++; console.log('  ✗ setAuthStyle 没生效'); }
else { pass++; console.log('  ✓ setAuthStyle 生效'); }
y2.setAuthStyle('off');

console.log('\n结果：' + pass + ' 通过，' + fail + ' 失败\n');
process.exit(fail ? 1 : 0);
