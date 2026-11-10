"""Draft the badger head's extreme poses (Marek 2026-11-09: nose all the way
up / down / left / right), as a solid head rather than just the face.

The head is the logo's face (depth map: rounded skull + muzzle) plus walls
running back from its outline to a rounded back. The walls take the colour
of the outline where they start, so the stripes run back along the head,
nose to ears, as on a real badger. Under the snout: the black nose's
underside, then the face's white with one thin mouth line.

White = the face's white: drawn as see-through (as in the logo), or solid
white with SOLID=1. Every pose is fitted inside the orange corners.

Usage: extremes.py SRC OUTDIR SIZE   (writes OUTDIR/<dir>_<deg>[_solid].png)
"""
import os
import sys

import numpy as np
from PIL import Image

SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
os.makedirs(OUT, exist_ok=True)
SS, DENS = 2, 2
BACK = 0.42        # how far back the head goes, x the head's width
TAPER = 0.55       # how much the head narrows towards the back (0 = a straight cylinder)
MUZZLE = 0.34      # how far the muzzle sticks out, x the head's width

src = Image.open(SRC).convert("RGBA")
W0, H0 = src.size
Wc = SIZE * SS; Hc = round(SIZE * H0 / W0) * SS
Wt, Ht = Wc * DENS, Hc * DENS
img = np.asarray(src.resize((Wt, Ht), Image.LANCZOS)).astype(np.float32) / 255.0
r, g, b, al = (img[..., i] for i in range(4))
orange = (al > 0.05) & (r > g + 0.15) & (r > 0.45) & (b < 0.45)
ink = np.where(orange, 0, al)
corners = img.copy(); corners[~orange] = 0
corners_img = Image.fromarray((corners * 255).astype(np.uint8)).resize((Wc // SS, Hc // SS), Image.LANCZOS)


def blur(a, rad, n=3):
    a = a.astype(np.float32)
    rad = max(1, int(rad))
    for _ in range(n):
        for ax in (0, 1):
            pad = [(rad + 1, rad) if i == ax else (0, 0) for i in range(2)]
            c = np.cumsum(np.pad(a, pad, mode="edge"), axis=ax)
            a = (c[2 * rad + 1:] - c[:-2 * rad - 1]) / (2 * rad + 1) if ax == 0 else (c[:, 2 * rad + 1:] - c[:, :-2 * rad - 1]) / (2 * rad + 1)
    return a


head = ink > 0.3
sil = np.zeros_like(head)
for y in range(Ht):
    xs = np.nonzero(head[y])[0]
    if len(xs) > 1:
        sil[y, xs.min():xs.max() + 1] = True
ys, xs = np.nonzero(sil)
top, bot, left, right = ys.min(), ys.max(), xs.min(), xs.max()
cx, cy = (left + right) / 2, (top + bot) / 2
size = right - left
yy, xx = np.mgrid[0:Ht, 0:Wt].astype(np.float32)

dome = blur(sil, size * 0.07) ** 0.7
nose_y = bot - 0.10 * (bot - top)
t = np.clip((yy - top) / (nose_y - top), 0, 1)
muzzle = np.exp(-((xx - cx) / (size * (0.30 - 0.14 * t))) ** 2) * t ** 2
z = np.where(sil, 0.30 * dome * size + MUZZLE * muzzle * size, 0).astype(np.float32)
z = np.where(sil, blur(z, size * 0.004, 1) / np.maximum(blur(sil, size * 0.004, 1), 1e-3), 0).astype(np.float32)
gy, gx = np.gradient(z)
nrm = np.stack([-gx, -gy, np.ones_like(z)], -1); nrm /= np.linalg.norm(nrm, axis=-1, keepdims=True)

# --- the face ----------------------------------------------------------------
fy, fx = np.nonzero(sil)
pts = [np.stack([fx - Wt / 2, fy - Ht / 2, z[fy, fx]], 1)]
cols = [ink[fy, fx]]          # 1 = black, 0 = the face's white
nrms = [nrm[fy, fx]]

# --- walls from the outline back to a rounded back ---------------------------
inner = sil.copy()
inner[1:-1, 1:-1] = sil[1:-1, 1:-1] & sil[:-2, 1:-1] & sil[2:, 1:-1] & sil[1:-1, :-2] & sil[1:-1, 2:]
edge = sil & ~inner
ey, ex = np.nonzero(edge)
sb = blur(sil, size * 0.01, 2)
gsy, gsx = np.gradient(sb)
on = -np.stack([gsx[ey, ex], gsy[ey, ex]], 1)
on /= np.maximum(np.linalg.norm(on, axis=1, keepdims=True), 1e-6)   # outward, in the image plane
# the back is rounded: shallower towards the top and bottom of the head
ry = np.clip(np.abs(ey - cy) / ((bot - top) / 2), 0, 1)
zback = -BACK * size * np.sqrt(np.clip(1 - ry ** 2, 0.15, 1))
zfront = z[ey, ex]
# the wall's colour: crisp, from a few pixels inside the outline (the very
# edge is anti-aliased grey)
iy = np.clip(np.round(ey - on[:, 1] * 4 * DENS).astype(int), 0, Ht - 1)
ix = np.clip(np.round(ex - on[:, 0] * 4 * DENS).astype(int), 0, Wt - 1)
ecol = (ink[iy, ix] > 0.5).astype(np.float32)
# under the snout: the nose's underside (black) near the front, then the
# face's white, crossed by one thin mouth line
dxm = (ex - cx) / (0.13 * size)
under = (on[:, 1] > 0.55) & (np.abs(dxm) < 1.6) & (ey > nose_y - 0.05 * size)
# where the eyes are (see-through holes inside the stripes), for the band
clear = ink < 0.5
reach = np.zeros_like(clear)
reach[0], reach[-1], reach[:, 0], reach[:, -1] = clear[0], clear[-1], clear[:, 0], clear[:, -1]
while True:
    grown = reach.copy()
    grown[1:] |= reach[:-1]; grown[:-1] |= reach[1:]; grown[:, 1:] |= reach[:, :-1]; grown[:, :-1] |= reach[:, 1:]
    grown &= clear
    if (grown == reach).all():
        break
    reach = grown
eye_y = np.nonzero(clear & ~reach & (yy < nose_y - 0.15 * size))[0].mean()
HEADH = bot - top
for i in range(len(ey)):
    length = (zfront[i] - zback[i]) * 1.2 + TAPER * np.hypot(ex[i] - cx, ey[i] - cy)
    n = int(length / 0.6) + 2
    tt = np.linspace(0, 1, n)                         # 0 at the face's edge, 1 at the back
    zs = zfront[i] + (zback[i] - zfront[i]) * tt
    pull = TAPER * tt ** 1.6                          # rounds in towards the back
    X = ex[i] + (cx - ex[i]) * pull
    Y = ey[i] + (cy - ey[i]) * pull
    c = np.full(n, ecol[i], np.float32)
    # painted like a badger: the first sliver keeps the outline's colour
    # (joins the face); the top keeps the stripes running back over a white
    # crown; the sides are white with a black band from the eye back up to
    # the ear; under the jaw is white (the snout gets the mouth line below)
    if on[i, 1] > 0.5:
        c = np.where(tt < 0.06, ecol[i], 0.0)
    elif on[i, 1] > -0.5:
        band = (eye_y - top) / HEADH - 0.30 * tt                 # rises towards the ear
        f = (Y - top) / HEADH
        c = np.where(tt < 0.06, ecol[i], (np.abs(f - band) < 0.13).astype(np.float32))
    if under[i]:
        line_t = 0.22 + 0.06 * dxm[i] ** 2            # a gentle curve across the jaw
        mouth = (np.abs(tt - line_t) < 0.03) & (np.abs(dxm[i]) < 1.0)
        c = np.where((tt < 0.08) | mouth, 1.0, 0.0)   # nose underside, white, the mouth line
    pts.append(np.stack([X - Wt / 2, Y - Ht / 2, zs], 1))
    cols.append(c)
    nz = -0.9 * tt                                    # turns from sideways to backwards
    nrms.append(np.stack([np.full(n, on[i, 0]), np.full(n, on[i, 1]), nz], 1) / np.sqrt(1 + nz[:, None] ** 2))
# the back of the head (only seen past 90 degrees; keeps the solid closed)
bz = -BACK * size * np.sqrt(np.clip(1 - (np.abs(fy - cy) / ((bot - top) / 2)) ** 2, 0.15, 1))
bxp = fx + (cx - fx) * TAPER; byp = fy + (cy - fy) * TAPER
pts.append(np.stack([bxp - Wt / 2, byp - Ht / 2, bz], 1)); cols.append(np.ones(len(fy), np.float32))
nrms.append(np.repeat(np.array([[0, 0, -1.0]]), len(fy), 0))

P0 = np.concatenate(pts).astype(np.float32); C0 = np.concatenate(cols).astype(np.float32); N0 = np.concatenate(nrms).astype(np.float32)
light = np.array([-0.3, -0.55, 1.0]); light /= np.linalg.norm(light)


def rot(p, ya, pa):
    x, y, zz = p[..., 0], p[..., 1], p[..., 2]
    x1 = x * np.cos(ya) + zz * np.sin(ya)
    z1 = -x * np.sin(ya) + zz * np.cos(ya)
    y2 = y * np.cos(pa) - z1 * np.sin(pa)
    z2 = y * np.sin(pa) + z1 * np.cos(pa)
    return np.stack([x1, y2, z2], -1)


def neighbours(a):
    return sum(np.roll(np.roll(a, dy, 0), dx, 1) for dy in (-1, 0, 1) for dx in (-1, 0, 1) if dy or dx)


k0 = 1.0 / DENS
NB = (P0[: len(fy), 0].min() * k0 + Wc / 2, P0[: len(fy), 0].max() * k0 + Wc / 2, P0[: len(fy), 1].min() * k0 + Hc / 2, P0[: len(fy), 1].max() * k0 + Hc / 2)


def render(yaw, pitch, solid=False):
    ya, pa = np.radians(yaw), np.radians(pitch)
    P = rot(P0, ya, pa); N = rot(N0, ya, pa)
    vis = N[:, 2] > 0.02
    P, C, N = P[vis], C0[vis], N[vis]
    shade = 0.84 + 0.16 * np.clip(N @ light, 0, 1)
    rgb = (1 - C) * 1.0 + C * 0.07 * shade
    white = 1 - C
    # fit inside the corners: never bigger than facing forward, and inside
    # the space the untouched head takes
    x0, x1 = P[:, 0].min() * k0, P[:, 0].max() * k0
    y0, y1 = P[:, 1].min() * k0, P[:, 1].max() * k0
    bw, bh = NB[1] - NB[0], NB[3] - NB[2]
    sc = min(1.0, bw / max(x1 - x0, 1), bh / max(y1 - y0, 1))
    ox = (NB[0] + NB[1]) / 2 - (x0 + x1) / 2 * sc
    oy = (NB[2] + NB[3]) / 2 - (y0 + y1) / 2 * sc
    if yaw == 0 and pitch == 0:
        sc, ox, oy = 1.0, Wc / 2, Hc / 2
    px = np.round(P[:, 0] * k0 * sc + ox).astype(np.int64)
    py = np.round(P[:, 1] * k0 * sc + oy).astype(np.int64)
    ok = (px >= 0) & (px < Wc) & (py >= 0) & (py < Hc)
    idx = (py * Wc + px)[ok]; zz = P[ok, 2]
    zbuf = np.full(Hc * Wc, -1e9, np.float32); np.maximum.at(zbuf, idx, zz)
    front = zz >= zbuf[idx] - 0.012 * size
    n_ = np.bincount(idx[front], minlength=Hc * Wc).astype(np.float32)
    col = (np.bincount(idx[front], rgb[ok][front], minlength=Hc * Wc) / np.maximum(n_, 1)).reshape(Hc, Wc).astype(np.float32)
    wht = (np.bincount(idx[front], white[ok][front], minlength=Hc * Wc) / np.maximum(n_, 1)).reshape(Hc, Wc).astype(np.float32)
    cov = (n_ > 0).astype(np.float32).reshape(Hc, Wc)
    nb = neighbours(cov); fill = (cov == 0) & (nb >= 5)
    for arr in (col, wht):
        avg = neighbours(arr * cov) / np.maximum(nb, 1); arr[fill] = avg[fill]
    cov[fill] = 1
    if solid:  # the face's white painted white
        alpha = cov; rgbv = col
    else:      # the face's white see-through, as in the logo
        alpha = cov * (1 - wht); rgbv = col
    pre = np.stack([rgbv * alpha] * 3 + [alpha], -1)
    im = pre.reshape(Hc // SS, SS, Wc // SS, SS, 4).mean((1, 3))
    a_ = im[..., 3:4]
    im = np.concatenate([np.where(a_ > 0, im[..., :3] / np.maximum(a_, 1e-6), 0), a_], -1)
    out = Image.fromarray((np.clip(im, 0, 1) * 255).astype(np.uint8))
    out.alpha_composite(corners_img)
    return out


render(0, 0).save(f"{OUT}/neutral.png")
DIRS = {"up": (0, 1), "down": (0, -1), "left": (-1, 0), "right": (1, 0)}
for d, (sy_, sp_) in DIRS.items():
    for deg in (45, 60, 75, 90):
        render(sy_ * deg, sp_ * deg).save(f"{OUT}/{d}_{deg}.png")
    render(sy_ * 90, sp_ * 90, solid=True).save(f"{OUT}/{d}_90_solid.png")
    print(d, "done", flush=True)
