"""把 1024 的图标做成 exe 的 .ico 和 Android 各密度 mipmap。

用法：python tools/make-icon-png.py
依赖：Pillow
"""
import os
from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ICON_DIR = os.path.join(ROOT, "android", "icon")
SRC = os.path.join(ICON_DIR, "icon_1024.png")

if not os.path.exists(SRC):
    raise SystemExit("先跑 node tools/make-icon.js + Edge 截图生成 icon_1024.png")

im = Image.open(SRC).convert("RGBA")

# ---- Windows exe 图标：一个 .ico 里塞多个尺寸 ----
ico_sizes = [16, 24, 32, 48, 64, 128, 256]
ico_path = os.path.join(ICON_DIR, "icon.ico")
im.save(ico_path, format="ICO", sizes=[(s, s) for s in ico_sizes])
print("写出", os.path.relpath(ico_path, ROOT), os.path.getsize(ico_path), "bytes")

# ---- Android mipmap：mdpi 48 / hdpi 72 / xhdpi 96 / xxhdpi 144 / xxxhdpi 192 ----
density = {
    "mdpi": 48,
    "hdpi": 72,
    "xhdpi": 96,
    "xxhdpi": 144,
    "xxxhdpi": 192,
}
res = os.path.join(ROOT, "android", "res")
for name, size in density.items():
    d = os.path.join(res, "mipmap-" + name)
    os.makedirs(d, exist_ok=True)
    out = os.path.join(d, "ic_launcher.png")
    im.resize((size, size), Image.LANCZOS).save(out, optimize=True)
    print("写出", os.path.relpath(out, ROOT), size)

# 桌面/网页用的通用 png（README、商店图之类）
for size in (256, 512):
    out = os.path.join(ICON_DIR, "icon_%d.png" % size)
    im.resize((size, size), Image.LANCZOS).save(out, optimize=True)
    print("写出", os.path.relpath(out, ROOT), size)
