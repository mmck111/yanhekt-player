import io, re
p = 'go/y2_orch.go'
s = io.open(p, encoding='utf-8').read()
# 只替换 y2Videos 内部的缓存写入（459/506/516/529/545 那几处），保持 y2RememberIf 自身不变
lines = s.split('\n')
for idx in (458, 505, 515, 528, 544):  # 0-based
    if 'y2Remember(key,' in lines[idx]:
        lines[idx] = lines[idx].replace('y2Remember(key,', 'y2RememberIf(deep, key,')
s = '\n'.join(lines)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('patched')
