#!/usr/bin/env python3
"""Generate build/tray-template.png — macOS menu-bar template (black + alpha star)."""

from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent
OUT = ROOT / "tray-template.png"

# Cubic segments from src-tauri/icons/icon.svg spark path (viewBox 512).
SEGS = (
    ((256, 74), (273, 157), (315, 199), (398, 216)),
    ((398, 216), (315, 233), (273, 275), (256, 438)),
    ((256, 438), (239, 275), (197, 233), (114, 216)),
    ((114, 216), (197, 199), (239, 157), (256, 74)),
)


def cubic(p0, p1, p2, p3, n=48):
    pts = []
    for i in range(n + 1):
        t = i / n
        u = 1 - t
        x = u**3 * p0[0] + 3 * u**2 * t * p1[0] + 3 * u * t**2 * p2[0] + t**3 * p3[0]
        y = u**3 * p0[1] + 3 * u**2 * t * p1[1] + 3 * u * t**2 * p2[1] + t**3 * p3[1]
        pts.append((x, y))
    return pts


def main() -> None:
    pts = []
    for seg in SEGS:
        chunk = cubic(*seg)
        pts.extend(chunk[1:] if pts else chunk)

    size, pad = 256, 28
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    scale = (size - 2 * pad) / 512
    poly = [(pad + x * scale, pad + y * scale) for x, y in pts]
    draw.polygon(poly, fill=(0, 0, 0, 255))

    scaled = img.resize((44, 44), Image.Resampling.LANCZOS)
    clean = Image.new("RGBA", (44, 44), (0, 0, 0, 0))
    for y in range(44):
        for x in range(44):
            _, _, _, a = scaled.getpixel((x, y))
            if a > 12:
                clean.putpixel((x, y), (0, 0, 0, a))
    clean.save(OUT)
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
