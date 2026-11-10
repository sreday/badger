"""Fake-3D badger head: the flat logo on a 3D surface, rendered as 121 views.

How (Marek's idea): the see-through parts of the head (the white blaze,
cheeks, eye holes) are the face's white. They're filled with a marked white
so the head is one solid surface that hides what's behind it when it
turns; every frame is rendered with a depth buffer (nearest point wins);
then the marked white is made see-through again.

- Under the snout there's real geometry (nose underside with nostrils,
  then the chin), edge-on and hidden looking straight ahead, coming into
  view as the head looks up.
- Every frame records each eye's spot and size, and whether it's seen
  (depth-tested), so eyelids blink in the right place.
- The orange scan-frame corners never move.

Usage: badger3d.py SRC OUT SCALE NAME   (writes OUT/NAME.webp + NAME.json)
"""
import json
import os
import sys

import numpy as np
from PIL import Image

SRC, OUT, SCALE, NAME = sys.argv[1], sys.argv[2], float(sys.argv[3]), sys.argv[4]
os.makedirs(OUT, exist_ok=True)

YAWS = np.linspace(-34, 34, 11)    # degrees, + = nose to the right
PITCHES = np.linspace(36, -24, 11)  # degrees, + = nose up; row 0 looks highest, row 6 level
if os.environ.get("ONE"):
    YAWS, PITCHES = np.array([float(os.environ["ONE"].split(",")[0])]), np.array([float(os.environ["ONE"].split(",")[1])])
HEAD_SCALE = 0.86                  # room so the turned head stays inside the corners
SS = 2                             # output supersampling
DENS = int(os.environ.get("DENS", 3))  # surface samples per supersampled pixel, per axis
if os.environ.get("ONE"):  # one test frame: ONE="yaw,pitch"
    _y, _p = map(float, os.environ["ONE"].split(","))

src = Image.open(SRC).convert("RGBA")
W0, H0 = src.size
Wc, Hc = int(round(W0 * SCALE)) * SS, int(round(H0 * SCALE)) * SS  # canvas
Wt, Ht = Wc * DENS, Hc * DENS                                        # surface grid
img = np.asarray(src.resize((Wt, Ht), Image.LANCZOS)).astype(np.float32) / 255.0
r, g, b, al = (img[..., i] for i in range(4))
orange = (al > 0.05) & (r > g + 0.15) & (r > 0.45) & (b < 0.45)
ink = np.where(orange, 0, al)  # the head's black, 0..1
if NAME.endswith("large"):
    fr = img.copy(); fr[~orange] = 0
    Image.fromarray((fr * 255).astype(np.uint8)).resize((Wc // SS, Hc // SS), Image.LANCZOS).save(f"{OUT}/frame.png")


def blur(a, rad, n=3):
    a = a.astype(np.float32)
    rad = max(1, int(rad))
    for _ in range(n):
        for ax in (0, 1):
            pad = [(rad + 1, rad) if i == ax else (0, 0) for i in range(2)]
            c = np.cumsum(np.pad(a, pad, mode="edge"), axis=ax)
            a = (c[2 * rad + 1:] - c[:-2 * rad - 1]) / (2 * rad + 1) if ax == 0 else (c[:, 2 * rad + 1:] - c[:, :-2 * rad - 1]) / (2 * rad + 1)
    return a


# The head's silhouette: each row from its first to its last black pixel.
head = ink > 0.3
sil = np.zeros_like(head)
for y in range(Ht):
    xs = np.nonzero(head[y])[0]
    if len(xs) > 1:
        sil[y, xs.min():xs.max() + 1] = True
ys, xs = np.nonzero(sil)
top, bot, left, right = ys.min(), ys.max(), xs.min(), xs.max()
cx = (left + right) / 2
size = right - left
yy, xx = np.mgrid[0:Ht, 0:Wt].astype(np.float32)

# Depth: a rounded skull plus a long muzzle, highest at the nose.
dome = blur(sil, size * 0.07) ** 0.7
nose_y = bot - 0.10 * (bot - top)
t = np.clip((yy - top) / (nose_y - top), 0, 1)
width = size * (0.30 - 0.14 * t)
muzzle = np.exp(-((xx - cx) / width) ** 2) * t ** 2
zm = 0.34 * muzzle * size
z = np.where(sil, 0.30 * dome * size + zm, 0).astype(np.float32)
# smooth only inside the head (normalised), so the edge doesn't slope down
# into the empty space outside and smear when it turns towards us
z = np.where(sil, blur(z, size * 0.004, 1) / np.maximum(blur(sil, size * 0.004, 1), 1e-3), 0).astype(np.float32)
if NAME.endswith("large"):
    Image.fromarray((np.clip(z / z.max(), 0, 1) * 255).astype(np.uint8)).resize((Wc // SS, Hc // SS)).save(f"{OUT}/depth.png")
gy, gx = np.gradient(z)
nrm = np.stack([-gx, -gy, np.ones_like(z)], -1)
nrm /= np.linalg.norm(nrm, axis=-1, keepdims=True)
light = np.array([-0.3, -0.55, 1.0]); light /= np.linalg.norm(light)

# Eyes: see-through holes inside the stripes (not reachable from the border).
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
holes = clear & ~reach & (yy < nose_y - 0.15 * size)
eyes = []
for side in (xx < cx, xx >= cx):
    ey, ex = np.nonzero(holes & side)
    eyes.append((ex.mean(), ey.mean(), max(np.ptp(ex), np.ptp(ey)) / 2 + 1))

# --- Surface points: the face (white-filled), then the underside -----------
fy, fx = np.nonzero(sil)
F_xyz = np.stack([fx - Wt / 2, fy - Ht / 2, z[fy, fx]], 1).astype(np.float32)
F_ink = ink[fy, fx].astype(np.float32)   # 1 = black, 0 = the face's white
F_n = nrm[fy, fx]

cols = np.arange(left, right + 1)
yb = np.array([np.nonzero(sil[:, x])[0].max() for x in cols], np.float32)
zf = np.array([z[max(0, int(y) - int(0.03 * size)):int(y) + 1, x].max() for x, y in zip(cols, yb)], np.float32)
zf *= np.exp(-((cols - cx) / (0.16 * size)) ** 2)
keep = zf > 0.03 * size
ucols, uyb, uzf = cols[keep], yb[keep], zf[keep]
S = np.linspace(0, 1, int(0.6 * size))
UX = np.repeat(ucols[None, :], len(S), 0).astype(np.float32)
UY = np.repeat(uyb[None, :], len(S), 0)
UZ = np.repeat(uzf[None, :], len(S), 0) * (1 - 0.5 * S[:, None])  # half-depth: the snout's underside, not a whole jaw
s = np.repeat(S[:, None], len(ucols), 1)
dxn = (UX - cx) / (0.13 * size)
nose = (np.abs(dxn) < 1) & (s < 0.4)
nostril = nose & (((np.abs(dxn) - 0.45) / 0.26) ** 2 + ((s - 0.16) / 0.09) ** 2 < 1)
tone = np.where(nostril, 0.04, np.where(nose, 0.30, 0.25 - 0.08 * s))
U_xyz = np.stack([UX - Wt / 2, UY - Ht / 2 + 0.5, UZ], -1).reshape(-1, 3).astype(np.float32)
U_tone = tone.reshape(-1).astype(np.float32)


PIVOT = 0.0  # set below: the head turns round a point inside the snout


def rot(p, ya, pa, pivot=True):
    x, y, zz = p[..., 0], p[..., 1], p[..., 2] - (PIVOT if pivot else 0)
    x1 = x * np.cos(ya) + zz * np.sin(ya)
    z1 = -x * np.sin(ya) + zz * np.cos(ya)
    y2 = y * np.cos(pa) - z1 * np.sin(pa)
    z2 = y * np.sin(pa) + z1 * np.cos(pa) + (PIVOT if pivot else 0)
    return np.stack([x1, y2, z2], -1)


PIVOT = 0.14 * size


def neighbours(a):
    return sum(np.roll(np.roll(a, dy, 0), dx, 1) for dy in (-1, 0, 1) for dx in (-1, 0, 1) if dy or dx)


k0 = 1.0 / DENS  # surface grid -> canvas pixels at full size
# the untouched head's extent on the canvas
NB = (F_xyz[:, 0].min() * k0 + Wc / 2, F_xyz[:, 0].max() * k0 + Wc / 2, F_xyz[:, 1].min() * k0 + Hc / 2, F_xyz[:, 1].max() * k0 + Hc / 2)
frames, eyemeta = [], []
for pitch in PITCHES:
    for yaw in YAWS:
        ya, pa = np.radians(yaw), np.radians(pitch)
        # full size facing forward (matches the static logo), a little
        # smaller the further it turns, so it keeps inside the corners
        k = k0 * (1 - (1 - HEAD_SCALE) * min(1, max(abs(yaw) / YAWS.max(), abs(pitch) / PITCHES.max())))
        Pf = rot(F_xyz, ya, pa)
        Nf = rot(F_n, ya, pa, pivot=False)
        shade = 0.84 + 0.16 * np.clip(Nf @ light, 0, 1)
        # back-face culling: surface turned away from us (the skull's far
        # edge as the head turns) isn't drawn, so edges stay clean
        face_on = Nf[:, 2] > 0.08
        P, C, Wf = [Pf[face_on]], [((1 - F_ink) + F_ink * 0.07 * shade)[face_on]], [(1 - F_ink)[face_on]]
        if pitch > 0.5 and len(U_xyz):
            P.append(rot(U_xyz, ya, pa)); C.append(U_tone * (0.75 + 0.25 * np.cos(pa))); Wf.append(np.zeros(len(U_xyz), np.float32))
        P = np.concatenate(P); C = np.concatenate(C); Wf = np.concatenate(Wf)
        # keep the whole head inside the corners: nudge it back if it spills
        gx0, gx1 = P[:, 0].min() * k + Wc / 2, P[:, 0].max() * k + Wc / 2
        gy0, gy1 = P[:, 1].min() * k + Hc / 2, P[:, 1].max() * k + Hc / 2
        # allowed: the canvas less a margin, but never less than the space
        # the untouched logo already takes (so facing forward never moves)
        m = 0.015 * Wc
        lx0, lx1 = min(m, NB[0]), max(Wc - m, NB[1])
        ly0, ly1 = min(m, NB[2]), max(Hc - m, NB[3])
        shx = (lx0 - gx0 if gx0 < lx0 else 0) - (gx1 - lx1 if gx1 > lx1 else 0)
        shy = (ly0 - gy0 if gy0 < ly0 else 0) - (gy1 - ly1 if gy1 > ly1 else 0)
        px = np.round(P[:, 0] * k + Wc / 2 + shx).astype(np.int64)
        py = np.round(P[:, 1] * k + Hc / 2 + shy).astype(np.int64)
        ok = (px >= 0) & (px < Wc) & (py >= 0) & (py < Hc)
        idx = (py * Wc + px)[ok]; zz = P[ok, 2]; Co = C[ok]; Wo = Wf[ok]
        # depth buffer: the nearest depth per pixel ...
        zbuf = np.full(Hc * Wc, -1e9, np.float32)
        np.maximum.at(zbuf, idx, zz)
        # ... then everything on that front surface (within a sliver of it)
        # is averaged, so black/white edges come out smooth, not speckled.
        front = zz >= zbuf[idx] - 0.012 * size
        n_ = np.bincount(idx[front], minlength=Hc * Wc).astype(np.float32)
        col = np.bincount(idx[front], Co[front], minlength=Hc * Wc) / np.maximum(n_, 1)
        wht = np.bincount(idx[front], Wo[front], minlength=Hc * Wc) / np.maximum(n_, 1)
        cov = (n_ > 0).astype(np.float32)
        col = col.astype(np.float32); wht = wht.astype(np.float32)
        cov2, col2, wht2 = cov.reshape(Hc, Wc), col.reshape(Hc, Wc), wht.reshape(Hc, Wc)
        # pinholes (stretched spots with no sample) take their neighbours' average
        nb = neighbours(cov2)
        fill = (cov2 == 0) & (nb >= 5)
        for arr in (col2, wht2):
            avg = neighbours(arr * cov2) / np.maximum(nb, 1)
            arr[fill] = avg[fill]
        cov2[fill] = 1
        # the marked white goes back to see-through
        alpha = cov2 * (1 - wht2)
        pre = np.stack([col2 * alpha] * 3 + [alpha], -1)
        im = pre.reshape(Hc // SS, SS, Wc // SS, SS, 4).mean((1, 3))
        a_ = im[..., 3:4]
        im = np.concatenate([np.where(a_ > 0, im[..., :3] / np.maximum(a_, 1e-6), 0), a_], -1)
        frames.append(Image.fromarray((np.clip(im, 0, 1) * 255).astype(np.uint8)))

        em = []
        zb = zbuf.reshape(Hc, Wc)
        for ex, ey, er in eyes:
            c = np.array([ex - Wt / 2, ey - Ht / 2, z[int(ey), int(ex)]], np.float32)
            pts = rot(np.array([c, c + [er, 0, 0], c - [er, 0, 0], c + [0, er, 0], c - [0, er, 0]], np.float32), ya, pa)
            X = pts[:, 0] * k + Wc / 2 + shx; Y = pts[:, 1] * k + Hc / 2 + shy
            iy, ix = int(round(Y[0])), int(round(X[0]))
            seen = bool(0 <= iy < Hc and 0 <= ix < Wc and pts[0, 2] >= zb[iy, ix] - 0.02 * size)
            em.append([round(float(X[0]) / SS, 2), round(float(Y[0]) / SS, 2), round(float(np.hypot(X[1] - X[2], Y[1] - Y[2])) / SS, 2),
                       round(float(np.hypot(X[3] - X[4], Y[3] - Y[4])) / SS, 2), seen])
        eyemeta.append(em)

nc, nr = len(YAWS), len(PITCHES)
fw, fh = Wc // SS, Hc // SS
sheet = Image.new("RGBA", (fw * nc, fh * nr))
for i, im in enumerate(frames):
    sheet.paste(im, ((i % nc) * fw, (i // nc) * fh))
sheet.save(f"{OUT}/{NAME}.webp", "WEBP", quality=86, method=6)
json.dump({"fw": fw, "fh": fh, "cols": nc, "rows": nr, "yaws": YAWS.tolist(), "pitches": PITCHES.tolist(), "eyes": eyemeta},
          open(f"{OUT}/{NAME}.json", "w"))
print(NAME, "frames", len(frames), "frame", (fw, fh), os.path.getsize(f"{OUT}/{NAME}.webp") // 1024, "KB",
      "hidden eyes", sum(1 for e in eyemeta for x in e if not x[4]))
