"""Hand-drawn style key poses for the badger head (Marek 2026-11-09: "the
aesthetics of hand-drawn animation with some squeezing, rather than proper
3d"). The flat logo is bent, never rendered as a 3D object:

- the face is warped smoothly: the parts nearer the front (snout) travel
  further than the ears, and the head squashes across the direction it
  turns and stretches along it;
- the nose is its own layer and leads the move, drawn over the face;
- looking up, a single-line mouth shows under the nose.

Three styles of maximum pose per direction (squash / reach / lean) to pick
from. Usage: cartoon.py SRC OUTDIR SIZE   (writes OUTDIR/<dir>_<style>.png)
"""
import os
import sys

import numpy as np
from PIL import Image, ImageDraw

SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
os.makedirs(OUT, exist_ok=True)
SS = 3  # supersampling

src = Image.open(SRC).convert("RGBA")
W0, H0 = src.size
W, H = SIZE * SS, round(SIZE * H0 / W0) * SS
img = np.asarray(src.resize((W, H), Image.LANCZOS)).astype(np.float32) / 255.0
r, g, b, al = (img[..., i] for i in range(4))
orange = (al > 0.05) & (r > g + 0.15) & (r > 0.45) & (b < 0.45)
ink = np.where(orange, 0, al)                     # the head's black (see-through elsewhere)
corners = img.copy(); corners[~orange] = 0
corners_im = Image.fromarray((corners * 255).astype(np.uint8)).resize((SIZE, H // SS), Image.LANCZOS)
yy, xx = np.mgrid[0:H, 0:W].astype(np.float32)


def blur(a, rad, n=3):
    a = a.astype(np.float32); rad = max(1, int(rad))
    for _ in range(n):
        for ax in (0, 1):
            pad = [(rad + 1, rad) if i == ax else (0, 0) for i in range(2)]
            c = np.cumsum(np.pad(a, pad, mode="edge"), axis=ax)
            a = (c[2 * rad + 1:] - c[:-2 * rad - 1]) / (2 * rad + 1) if ax == 0 else (c[:, 2 * rad + 1:] - c[:, :-2 * rad - 1]) / (2 * rad + 1)
    return a


head = ink > 0.3
sil = np.zeros_like(head)
for y in range(H):
    xs = np.nonzero(head[y])[0]
    if len(xs) > 1:
        sil[y, xs.min():xs.max() + 1] = True
ys, xs = np.nonzero(sil)
top, bot, left, right = ys.min(), ys.max(), xs.min(), xs.max()
cx, cy = (left + right) / 2, (top + bot) / 2
size = right - left

# The nose: the black blob at the bottom middle (connected to its centre).
seed_y, seed_x = int(bot - 0.06 * (bot - top)), int(cx)
nose = np.zeros_like(head); nose[seed_y, seed_x] = True
blk = ink > 0.5
while True:
    g2 = nose.copy()
    g2[1:] |= nose[:-1]; g2[:-1] |= nose[1:]; g2[:, 1:] |= nose[:, :-1]; g2[:, :-1] |= nose[:, 1:]
    g2 &= blk
    if (g2 == nose).all():
        break
    nose = g2
nose_soft = blur(nose, SS, 1) > 0.02                # include its anti-aliased rim
ny, nx = np.nonzero(nose)
ncx, ncy = nx.mean(), ny.mean(); nw = nx.max() - nx.min(); nh = ny.max() - ny.min()
face_ink = np.where(nose_soft, 0, ink)
nose_ink = np.where(nose_soft, ink, 0)

# "How far forward" each part is: highest at the snout, low at the ears.
dome = blur(sil, size * 0.08) ** 0.7
tt = np.clip((yy - top) / (ncy - top), 0, 1)
muzzle = np.exp(-((xx - cx) / (size * (0.32 - 0.14 * tt))) ** 2) * tt ** 2
front = blur(0.45 * dome + 0.75 * muzzle, size * 0.04, 2)
front = front / front.max()


def sample(arr, X, Y):
    x0 = np.floor(X).astype(np.int32); y0 = np.floor(Y).astype(np.int32)
    fx, fy = X - x0, Y - y0
    out = 0
    for dy, wy in ((0, 1 - fy), (1, fy)):
        for dx, wx in ((0, 1 - fx), (1, fx)):
            xi = np.clip(x0 + dx, 0, W - 1); yi = np.clip(y0 + dy, 0, H - 1)
            inside = (x0 + dx >= 0) & (x0 + dx < W) & (y0 + dy >= 0) & (y0 + dy < H)
            out = out + arr[yi, xi] * wx * wy * inside
    return out


PAD = int(0.4 * W)  # draw on a bigger canvas, then fit inside the corners
gy_, gx_ = np.mgrid[-PAD:H + PAD, -PAD:W + PAD].astype(np.float32)


def pose(dx, dy, travel, squash, tilt_deg=0.0, nose_lead=1.6, mouth=False):
    """dx, dy: direction (-1..1); travel: how far the snout moves, x head
    size; squash: how much the head narrows along the move (0..0.4), as a
    turning head foreshortens; it stretches a little across it."""
    d = np.array([dx, dy], np.float32); moving = np.linalg.norm(d) > 0
    dn = d / max(np.linalg.norm(d), 1e-6); perp = np.array([-dn[1], dn[0]])
    A = (np.outer(dn, dn) * (1 - squash) + np.outer(perp, perp) * (1 + 0.3 * squash)) if moving else np.eye(2)
    th = np.radians(tilt_deg); R = np.array([[np.cos(th), -np.sin(th)], [np.sin(th), np.cos(th)]])
    M = R @ A; Minv = np.linalg.inv(M)
    shift = d * travel * size
    qx, qy = gx_ - cx, gy_ - cy
    # the face: invert q = M p + shift * front(p) (front parts travel furthest)
    px, py = qx.copy(), qy.copy()
    for _ in range(12):
        f = sample(front, px + cx, py + cy)
        bx, by = qx - shift[0] * f, qy - shift[1] * f
        px = Minv[0, 0] * bx + Minv[0, 1] * by
        py = Minv[1, 0] * bx + Minv[1, 1] * by
    face = sample(face_ink, px + cx, py + cy)
    # the nose leads, drawn over the face; looking up it flattens a little
    ncq = np.array([ncx - cx, ncy - cy]) @ M.T + shift * nose_lead
    nM = M @ np.diag([1.0, 1 - 0.18 * max(0.0, -dy)])
    nMi = np.linalg.inv(nM)
    nqx, nqy = qx - ncq[0], qy - ncq[1]
    npx = nMi[0, 0] * nqx + nMi[0, 1] * nqy + (ncx - cx)
    npy = nMi[1, 0] * nqx + nMi[1, 1] * nqy + (ncy - cy)
    nos = sample(nose_ink, npx + cx, npy + cy)
    a = np.clip(face + nos - face * nos, 0, 1)
    im = Image.fromarray((np.dstack([np.zeros_like(a)] * 3 + [a]) * 255).astype(np.uint8), "RGBA")
    if mouth:
        # one hand-drawn line under the nose
        dr = ImageDraw.Draw(im)
        mx = cx + ncq[0] + PAD; my = cy + ncq[1] + PAD + nh * 0.62
        w_ = nw * 0.42
        dr.arc([mx - w_, my - w_ * 0.5, mx + w_, my + w_ * 0.5], start=25, end=155, fill=(0, 0, 0, 255), width=max(2, int(size * 0.022)))
    # fit inside the corners: never bigger than facing forward
    al_ = np.asarray(im)[..., 3]
    ysel, xsel = np.nonzero(al_ > 70)
    bx0, bx1, by0, by1 = xsel.min(), xsel.max(), ysel.min(), ysel.max()
    fx0, fx1, fy0, fy1 = left + PAD, right + PAD, top + PAD, bot + PAD
    sc = min(1.0, (fx1 - fx0) / max(bx1 - bx0, 1), (fy1 - fy0) / max(by1 - by0, 1))
    crop = im.crop((bx0, by0, bx1 + 1, by1 + 1))
    if sc < 1:
        crop = crop.resize((max(1, int(crop.width * sc)), max(1, int(crop.height * sc))), Image.LANCZOS)
    ox = int(np.clip((bx0 + bx1) / 2 - crop.width / 2, fx0, fx1 - crop.width)) - PAD
    oy = int(np.clip((by0 + by1) / 2 - crop.height / 2, fy0, fy1 - crop.height)) - PAD
    canvas = Image.new("RGBA", (W, H)); canvas.alpha_composite(crop, (max(0, ox), max(0, oy)))
    out = canvas.resize((SIZE, H // SS), Image.LANCZOS)
    out.alpha_composite(corners_im)
    return out


STYLES = {
    # travel, squash, tilt, nose lead
    "squash": (0.16, 0.30, 0, 1.5),
    "reach":  (0.26, 0.12, 0, 1.9),
    "lean":   (0.18, 0.24, 12, 1.6),
}
DIRS = {"up": (0, -1), "down": (0, 1), "left": (-1, 0), "right": (1, 0)}
pose(0, 0, 0, 0).save(f"{OUT}/neutral.png")
for dname, (dx, dy) in DIRS.items():
    for sname, (tr, sq, tilt, lead) in STYLES.items():
        tilt_s = tilt * (1 if dx >= 0 else -1) if dy == 0 else tilt * 0.6
        pose(dx, dy, tr, sq, tilt_s, lead, mouth=(dy < 0)).save(f"{OUT}/{dname}_{sname}.png")
    print(dname, flush=True)
