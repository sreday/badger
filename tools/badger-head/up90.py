"""Key frame: looking all the way up (Marek 2026-11-09: "nose all the way up,
showing only the underbelly of its head"), hand-drawn from the logo's own
parts: the nose (seen from below) at the top, a single-line mouth under it,
the cheek strokes turned into the jaw closing in on the chin, and the ear
tips peeking out at the far end (the bottom). Three variants.

Usage: up90.py HEAD_PNG OUTDIR SIZE
"""
import os
import sys

import numpy as np
from PIL import Image, ImageDraw

SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
os.makedirs(OUT, exist_ok=True)
SS = 2

im = Image.open(SRC).convert("RGBA")
W0, H0 = im.size
a = np.asarray(im).astype(int)
r, g, b, al = [a[..., i] for i in range(4)]
orange = (al > 12) & (r > g + 38) & (r > 115) & (b < 115)
ink = np.where(orange, 0, al).astype(np.uint8)            # the head's black as alpha
corners = np.asarray(im).copy(); corners[~orange] = 0
blk = ink > 128

# cut the logo into its parts
parts, left = [], blk.copy()
while left.any():
    ys, xs = np.nonzero(left)
    m = np.zeros_like(left); m[ys[0], xs[0]] = True
    while True:
        g2 = m.copy()
        g2[1:] |= m[:-1]; g2[:-1] |= m[1:]; g2[:, 1:] |= m[:, :-1]; g2[:, :-1] |= m[:, 1:]
        g2 &= left
        if (g2 == m).all():
            break
        m = g2
    left &= ~m
    if m.sum() > 50:
        parts.append(m)
parts.sort(key=lambda m: -m.sum())
stripes, nose, cheeks = parts[:2], parts[2], parts[3:5]


def layer(mask, grow=2):
    """The part as an RGBA image (black, anti-aliased edge kept), cropped."""
    mm = mask.copy()
    for _ in range(grow):
        mm[1:] |= mm[:-1]; mm[:-1] |= mm[1:]; mm[:, 1:] |= mm[:, :-1]; mm[:, :-1] |= mm[:, 1:]
    alpha = np.where(mm, ink, 0).astype(np.uint8)
    ys, xs = np.nonzero(alpha > 0)
    box = (xs.min(), ys.min(), xs.max() + 1, ys.max() + 1)
    rgba = np.zeros((H0, W0, 4), np.uint8); rgba[..., 3] = alpha
    return Image.fromarray(rgba).crop(box), box


def place(canvas, part, cx, cy, sx=1.0, sy=1.0, flip=False, rot=0.0):
    p = part.transpose(Image.FLIP_TOP_BOTTOM) if flip else part
    p = p.resize((max(1, int(p.width * sx * SS)), max(1, int(p.height * sy * SS))), Image.LANCZOS)
    if rot:
        p = p.rotate(rot, resample=Image.BICUBIC, expand=True)
    canvas.alpha_composite(p, (int(cx * SS - p.width / 2), int(cy * SS - p.height / 2)))


def mouth(canvas, cx, cy, w, curve, thick):
    d = ImageDraw.Draw(canvas)
    d.arc([(cx - w) * SS, (cy - curve) * SS, (cx + w) * SS, (cy + curve) * SS], start=15, end=165,
          fill=(0, 0, 0, 255), width=int(thick * SS))


nose_l, nb = layer(nose)
nose_w = nb[2] - nb[0]
ck = sorted([layer(c) for c in cheeks], key=lambda t: t[1][0])   # left, right
st = sorted([layer(s) for s in stripes], key=lambda t: t[1][0])
mid = W0 / 2


def finish(canvas, name):
    out = canvas.resize((W0, H0), Image.LANCZOS)
    out.alpha_composite(Image.fromarray(corners))
    out.resize((SIZE, round(SIZE * H0 / W0)), Image.LANCZOS).save(f"{OUT}/{name}.png")


# 1) flipped: the whole head upside down and squashed (the literal opposite)
c1 = Image.new("RGBA", (W0 * SS, H0 * SS))
whole = Image.fromarray(np.dstack([np.zeros_like(ink)] * 3 + [ink])).transpose(Image.FLIP_TOP_BOTTOM)
whole = whole.resize((int(W0 * 0.92 * SS), int(H0 * 0.62 * SS)), Image.LANCZOS)
c1.alpha_composite(whole, (int((W0 * SS - whole.width) / 2), int(H0 * 0.16 * SS)))
nose_y1 = H0 * 0.16 + (H0 - 441) * 0.62 + nose_l.height * 0.62
mouth(c1, mid, H0 * 0.16 + nose_l.height * 0.62 + 30, nose_w * 0.38, 24, 8)
finish(c1, "up_flipped")

# 2) underbelly: nose (from below) at the top, mouth, jaw, ear tips at the bottom
def underbelly(ears=True, nose_scale=1.0):
    c = Image.new("RGBA", (W0 * SS, H0 * SS))
    ny = H0 * 0.21
    place(c, nose_l, mid, ny, 0.95 * nose_scale, 0.58 * nose_scale, flip=True)      # the nose seen from below
    mouth(c, mid, ny + nose_l.height * 0.30 * nose_scale + 22, nose_w * 0.38, 24, 8)
    # the jaw: the cheek strokes flipped, closing in towards the chin
    for (part, box), side in zip(ck, (-1, 1)):
        place(c, part, mid + side * W0 * 0.20, H0 * 0.55, 0.95, 1.05, flip=True, rot=side * 8)
    if ears:
        # the ear tips, far away at the bottom: the stripes' tops, flipped and small
        for (part, box), side in zip(st, (-1, 1)):
            ear = part.crop((0, 0, part.width, int(part.height * 0.32)))
            place(c, ear, mid + side * W0 * 0.22, H0 * 0.78, 0.48, 0.40, flip=True, rot=-side * 14)
    return c


finish(underbelly(True), "up_underbelly")
finish(underbelly(False, 1.12), "up_minimal")
print("ok")
