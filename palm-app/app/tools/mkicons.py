#!/usr/bin/env python3
"""Write palm-gw's launcher icons from Zoe's logo (icons/logo.tiff, 256x256: a router on a
teal-to-blue circle), as BMPs PilRC reads (after FuelLog's tools/mkicons.py; docs/PALM-DEV.md
section 6).

No image library: macOS's sips turns the TIFF into an ordinary BMP, read here. Each icon
pixel looks at 8x8 samples of the logo: under half of them inside the circle and it is
transparent (magenta, which PilRC is told about); mostly the router's white and it is
white, so the lines stay crisp at 22 pixels; otherwise the average of the circle's colour
there. PilRC maps the colours to the Palm's 8-bit palette. The 1-bit icons are the circle
in black with the router in white.

    tools/mkicons.py [icons/logo.tiff] [icons]
"""
import os, struct, subprocess, sys, tempfile

SRC = sys.argv[1] if len(sys.argv) > 1 else "icons/logo.tiff"
OUT = sys.argv[2] if len(sys.argv) > 2 else "icons"
T = (255, 0, 255)


def read_logo(path):
    """The logo's pixels as rows of (r, g, b), top row first, through sips."""
    with tempfile.TemporaryDirectory() as d:
        bmp = os.path.join(d, "logo.bmp")
        subprocess.run(["sips", "-s", "format", "bmp", path, "--out", bmp], check=True, capture_output=True)
        b = open(bmp, "rb").read()
    off = struct.unpack_from("<I", b, 10)[0]
    w, h = struct.unpack_from("<ii", b, 18)
    bits = struct.unpack_from("<H", b, 28)[0]
    assert bits in (24, 32), bits
    step = bits // 8
    stride = (w * step + 3) & ~3
    rows = []
    for y in range(abs(h)):
        line = b[off + y * stride: off + y * stride + w * step]
        rows.append([(line[i + 2], line[i + 1], line[i]) for i in range(0, w * step, step)])
    if h > 0:
        rows.reverse()  # bottom-up
    return rows


def circle(rows):
    """Centre and radius of the logo's disc: the extent of the pixels that are not white."""
    w, h = len(rows[0]), len(rows)
    xs = [x for y in range(h) for x in range(w) if sum(rows[y][x]) < 3 * 235]
    ys = [y for y in range(h) for x in range(w) if sum(rows[y][x]) < 3 * 235]
    x0, x1, y0, y1 = min(xs), max(xs), min(ys), max(ys)
    return (x0 + x1 + 1) / 2, (y0 + y1 + 1) / 2, max(x1 - x0 + 1, y1 - y0 + 1) / 2


def render(rows, cx, cy, r, w, h, d):
    """An icon w x h with the disc d pixels across, centred; rows of colours."""
    out = []
    ox, oy = (w - d) / 2, (h - d) / 2
    scale = 2 * r / d
    for v in range(h):
        line = []
        for u in range(w):
            inside, white, acc = 0, 0, [0, 0, 0]
            for j in range(8):
                for i in range(8):
                    sx = cx - r + ((u - ox) + (i + 0.5) / 8) * scale
                    sy = cy - r + ((v - oy) + (j + 0.5) / 8) * scale
                    if (sx - cx) ** 2 + (sy - cy) ** 2 > r * r:
                        continue
                    inside += 1
                    p = rows[min(int(sy), len(rows) - 1)][min(int(sx), len(rows[0]) - 1)]
                    if min(p) > 200:
                        white += 1
                    else:
                        acc = [acc[k] + p[k] for k in range(3)]
            if inside < 32:
                line.append(T)
            elif white * 100 >= inside * 35:
                line.append((255, 255, 255))
            else:
                n = inside - white
                line.append(tuple(round(a / n) for a in acc))
        out.append(line)
    return out


def info_header(w, h, bits, image_bytes, colours):
    """A standard 40-byte BITMAPINFOHEADER. The PilRC Tessera's setup builds (jichu4n/pilrc)
    reads it on a 64-bit host; the original PilRC 3.2 needed FuelLog's padded 64-byte layout
    (PALM-DEV.md section 6), which this one refuses."""
    return struct.pack("<IiiHHIIiiII", 40, w, h, 1, bits, 0, image_bytes, 2835, 2835, colours, 0)


def write_bmp(path, w, h, bits, palette, pix):
    off = 14 + 40 + len(palette) * 4
    palb = b"".join(struct.pack("<BBBB", b, g, r, 0) for (r, g, b) in palette)
    hdr = b"BM" + struct.pack("<IHHI", off + len(pix), 0, 0, off)
    with open(path, "wb") as f:
        f.write(hdr + info_header(w, h, bits, len(pix), len(palette)) + palb + pix)


def bmp24(path, img):
    """24-bit colour; PilRC maps it to the 8-bit palette for BPP 8."""
    h, w = len(img), len(img[0])
    stride = (w * 3 + 3) & ~3
    pix = b""
    for line in reversed(img):
        pix += b"".join(bytes((c[2], c[1], c[0])) for c in line) + b"\0" * (stride - w * 3)
    write_bmp(path, w, h, 24, [], pix)


def bmp1(path, img):
    """1 bit: index 0 black ink (the disc), 1 white (the router and the background)."""
    h, w = len(img), len(img[0])
    stride = ((w + 31) // 32) * 4
    pix = b""
    for line in reversed(img):
        row = bytearray(stride)
        for i, c in enumerate(line):
            if c == T or c == (255, 255, 255):
                row[i // 8] |= 0x80 >> (i % 8)
        pix += bytes(row)
    write_bmp(path, w, h, 1, [(0, 0, 0), (255, 255, 255)], pix)


rows = read_logo(SRC)
cx, cy, r = circle(rows)
os.makedirs(OUT, exist_ok=True)
for name, w, h, d in (("icon22", 22, 22, 22), ("icon44", 44, 44, 44), ("small15", 15, 9, 9), ("small30", 30, 18, 18)):
    img = render(rows, cx, cy, r, w, h, d)
    bmp24(os.path.join(OUT, name + "-8.bmp"), img)
    if name in ("icon22", "small15"):
        bmp1(os.path.join(OUT, name + "-1.bmp"), img)
    print(name, w, "x", h, ":", sum(c != T for line in img for c in line), "pixels drawn")
