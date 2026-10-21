# -*- coding: utf-8 -*-
"""把「延河课堂-全部课程信息.xlsx」压成播放器内置的课程库。

产物：public/catalog.json.gz
结构（尽量紧凑，79k 行也要在几百 KB 量级）：
  {
    "meta":  { "built": "...", "count": N, "source": "..." },
    "terms": ["2026-2027 第一学期", ...],      # 学期字典
    "names": ["数值分析", ...],                 # 课程名字典
    "teachers": ["孙新", ...],                  # 教师名字典
    "rows":   [[nameIdx, teacherIdx, termIdx, courseId], ...]
  }
"""
import openpyxl, json, gzip, io, os, datetime

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "dist", "延河课堂-全部课程信息.xlsx")
OUT = os.path.join(ROOT, "public", "catalog.json.gz")

wb = openpyxl.load_workbook(SRC, read_only=True, data_only=True)
ws = wb["全部课程"]
it = ws.iter_rows(values_only=True)
header = list(next(it))
idx = {h: i for i, h in enumerate(header)}
i_id = idx["课程ID"]
i_name = idx["课程名称"]
i_teacher = idx["授课教师"]
i_term = idx["学年学期"]

terms, names, teachers = {}, {}, {}
rows = []
skipped = 0

def intern(d, v):
    if v not in d:
        d[v] = len(d)
    return d[v]

for r in it:
    cid = r[i_id]
    name = (r[i_name] or "").strip()
    if cid is None or not name:
        skipped += 1
        continue
    teacher = (r[i_teacher] or "").strip()
    term = (r[i_term] or "").strip()
    rows.append([intern(names, name), intern(teachers, teacher), intern(terms, term), int(cid)])

# 学期字典按时间倒序（新的在前），前端下拉框直接用
def term_key(t):
    digits = "".join(ch if ch.isdigit() else "" for ch in t[:9])
    return (digits, t)

order = sorted(terms.keys(), key=term_key, reverse=True)
remap = {terms[t]: i for i, t in enumerate(order)}
term_list = order
for row in rows:
    row[2] = remap[row[2]]

out = {
    "meta": {
        "built": datetime.datetime.now().strftime("%Y-%m-%d %H:%M"),
        "count": len(rows),
        "source": "延河课堂-全部课程信息.xlsx",
    },
    "terms": term_list,
    "names": list(names.keys()),
    "teachers": list(teachers.keys()),
    "rows": rows,
}

raw = json.dumps(out, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
with gzip.GzipFile(OUT, "wb", compresslevel=9, mtime=0) as f:
    f.write(raw)
print("rows", len(rows), "skipped", skipped, "terms", len(term_list), "names", len(names), "teachers", len(teachers))
print("json", len(raw) // 1024, "KB  ->  gz", os.path.getsize(OUT) // 1024, "KB")
