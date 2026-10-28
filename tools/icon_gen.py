#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""内阁图标素材生成脚本（一次性工具，留档备用）。

输入：项目根目录 内阁.png（1024x1024，黑底立绘）
输出：
  assets/icon/cabinet.png          归档标准图（512，透明底，全身）
  assets/icon/cabinet-bust.png     上半身裁剪（托盘小尺寸辨识度更好）
  assets/icon/cabinet.ico          托盘用多尺寸 ico（16~256，上半身版）
  phone/src/main/res/drawable-*/ic_launcher.png  手机各密度启动图标（全身版，
      内容居中占 66% 画布，符合 Android launcher 安全区惯例）

处理要点：背景为纯黑，但人物内部（西装/裤子）也是黑色，
不能全局按颜色抠底——从四角 floodfill 只抠与边缘连通的黑背景，内部黑色保留。
"""
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "内阁.png"
ICON_DIR = ROOT / "assets" / "icon"
RES = ROOT / "phone" / "src" / "main" / "res"

# Android 各密度启动图标边长（px）
LAUNCHER_SIZES = {
    "drawable-mdpi": 48,
    "drawable-hdpi": 72,
    "drawable-xhdpi": 96,
    "drawable-xxhdpi": 144,
    "drawable-xxxhdpi": 192,
}


def cutout_black_bg(img: Image.Image) -> Image.Image:
    """黑底转透明：RGBA 化后从四角 floodfill，thresh 容差覆盖抗锯齿过渡带。"""
    rgba = img.convert("RGBA")
    w, h = rgba.size
    for seed in [(0, 0), (w - 1, 0), (0, h - 1), (w - 1, h - 1)]:
        ImageDraw.floodfill(rgba, seed, (0, 0, 0, 0), thresh=32)
    return rgba


def tight_crop(img: Image.Image) -> Image.Image:
    """按 alpha 收紧到内容 bbox。"""
    bbox = img.getbbox()
    return img.crop(bbox) if bbox else img


def scale_to_content(canvas: int, content: Image.Image, ratio: float) -> Image.Image:
    """生成 canvas x canvas 透明画布，内容按 ratio 缩放后居中。"""
    img = Image.new("RGBA", (canvas, canvas), (0, 0, 0, 0))
    inner = int(canvas * ratio)
    w, h = content.size
    scale = min(inner / w, inner / h)
    resized = content.resize((max(1, int(w * scale)), max(1, int(h * scale))), Image.LANCZOS)
    img.alpha_composite(resized, ((canvas - resized.width) // 2, (canvas - resized.height) // 2))
    return img


def main() -> None:
    ICON_DIR.mkdir(parents=True, exist_ok=True)
    src = Image.open(SRC)
    full = tight_crop(cutout_black_bg(src))

    # 上半身：取头部到约 48% 高度（帽+肩+风衣上部），再收紧
    bust = tight_crop(full.crop((0, 0, full.width, int(full.height * 0.48))))

    # 归档标准图
    full512 = scale_to_content(512, full, 0.96)
    full512.save(ICON_DIR / "cabinet.png")
    bust512 = scale_to_content(512, bust, 0.96)
    bust512.save(ICON_DIR / "cabinet-bust.png")

    # 托盘 ico（上半身版，小尺寸辨识度更好）
    bust512.save(ICON_DIR / "cabinet.ico", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])

    # 手机启动图标（全身版，内容占 66% 安全区）
    for dpi, size in LAUNCHER_SIZES.items():
        out = RES / dpi / "ic_launcher.png"
        out.parent.mkdir(parents=True, exist_ok=True)
        scale_to_content(size, full, 0.66).save(out)
        print(f"launcher {dpi}: {out} ({size}x{size})")

    print("done:", ICON_DIR / "cabinet.png", ICON_DIR / "cabinet.ico")


if __name__ == "__main__":
    main()
