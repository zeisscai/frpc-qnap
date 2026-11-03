#!/usr/bin/env python3
"""生成 QPKG 图标（64x64 PNG，无第三方依赖）。

QTS 的 App Center / 主菜单要求图标为 64x64 的 gif 或 png。
"""

import struct
import zlib
from pathlib import Path

W = H = 64
RADIUS = 13
SCALE = 5
GLYPH = [
    "11111",
    "10000",
    "10000",
    "11110",
    "10000",
    "10000",
    "10000",
]

TOP = (28, 124, 212)
BOTTOM = (11, 84, 156)
FG = (255, 255, 255, 255)


def inside_round(x: int, y: int) -> bool:
    cx = min(max(x, RADIUS), W - 1 - RADIUS)
    cy = min(max(y, RADIUS), H - 1 - RADIUS)
    dx, dy = x - cx, y - cy
    return dx * dx + dy * dy <= RADIUS * RADIUS


def build_pixels():
    glyph_w = len(GLYPH[0]) * SCALE
    glyph_h = len(GLYPH) * SCALE
    gx = (W - glyph_w) // 2
    gy = (H - glyph_h) // 2

    rows = []
    for y in range(H):
        row = bytearray()
        for x in range(W):
            if not inside_round(x, y):
                row += b"\x00\x00\x00\x00"
                continue

            t = y / (H - 1)
            r = int(TOP[0] + (BOTTOM[0] - TOP[0]) * t)
            g = int(TOP[1] + (BOTTOM[1] - TOP[1]) * t)
            b = int(TOP[2] + (BOTTOM[2] - TOP[2]) * t)

            # 叠加 "F" 字形
            if gx <= x < gx + glyph_w and gy <= y < gy + glyph_h:
                col = (x - gx) // SCALE
                line = (y - gy) // SCALE
                if GLYPH[line][col] == "1":
                    row += bytes(FG)
                    continue

            row += bytes((r, g, b, 255))
        rows.append(bytes(row))
    return rows


def write_png(path: Path, rows):
    raw = b"".join(b"\x00" + row for row in rows)

    def chunk(tag: bytes, data: bytes) -> bytes:
        return (
            struct.pack(">I", len(data))
            + tag
            + data
            + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
        )

    ihdr = struct.pack(">IIBBBBB", W, H, 8, 6, 0, 0, 0)
    png = (
        b"\x89PNG\r\n\x1a\n"
        + chunk(b"IHDR", ihdr)
        + chunk(b"IDAT", zlib.compress(raw, 9))
        + chunk(b"IEND", b"")
    )
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(png)
    return len(png)


if __name__ == "__main__":
    out = Path(__file__).resolve().parent.parent / "package" / "icons" / "frpc-qnap.png"
    size = write_png(out, build_pixels())
    print(f"icon written: {out} ({size} bytes)")
