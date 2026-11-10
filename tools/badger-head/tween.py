"""The in-between frames: 21 x 21 (twice as smooth as the first 11 x 11
draft), built from the nine approved key poses (nine.py).

- Every frame has the same parts: the nose, the two eye stripes, the two
  halves of the black underside, the mouth. A part a key pose doesn't show
  is there at zero size, so between poses it grows or shrinks away
  smoothly (the far stripe as the head turns) instead of popping.
- The underside halves are outlines with matching points in every pose
  (traced along each shape's length), so the logo's thin strokes widen
  into the throat point by point.
- Values follow a smooth curve through all three keys per direction (no
  kinks at the key poses).
- The nose sits under the pointer (the nearest spot clear of the orange
  corners) in every frame; the face follows it.
- Every frame is checked like the keys: nothing merging, corners clear.

Usage: tween.py HEAD_PNG OUTDIR SIZE   -> OUTDIR/f_<row>_<col>.png + tween.json
"""
import json
import os
import sys

import numpy as np
from PIL import Image, ImageDraw

HERE = os.path.dirname(os.path.abspath(__file__))
SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
sys.argv = [sys.argv[0], SRC, os.path.join(OUT, "_keys"), str(SIZE)]
sys.path.insert(0, HERE)
import nine  # noqa: E402  (the key poses)

os.makedirs(OUT, exist_ok=True)
W0, H0, SS = nine.W0, nine.H0, nine.SS
N = 21           # frames per direction
K = 12           # samples along each underside half (outline = 2K points)
ROWS, COLS = ("top", "mid", "bottom"), ("left", "centre", "right")

for r in ROWS:
    for c in COLS:
        nine.solve(r, c)


def poly_mask(pts):
    loop = nine.smooth(list(pts) + list(pts[:3]), 16)[16:16 * (len(pts) + 1)]
    im = Image.new("L", (W0, H0)); ImageDraw.Draw(im).polygon(loop, fill=255)
    return np.asarray(im) > 127


def axis_outline(mask, inner):
    """The shape traced along its length: 2K points (one edge out, the other
    back), starting at the end nearest `inner` (towards the nose)."""
    ys, xs = np.nonzero(mask)
    c = np.array([xs.mean(), ys.mean()])
    P = np.stack([xs, ys], 1) - c
    ev, evec = np.linalg.eigh(np.cov(P.T))
    e1 = evec[:, 1]
    t = P @ e1
    if np.linalg.norm(c + t.min() * e1 - inner) > np.linalg.norm(c + t.max() * e1 - inner):
        e1 = -e1; t = -t
    e2 = np.array([-e1[1], e1[0]])
    s = P @ e2
    lo, hi = t.min(), t.max()
    up, down = [], []
    for tk in np.linspace(lo, hi, K):
        sel = np.abs(t - tk) <= max((hi - lo) / K, 1.5)
        smax, smin = s[sel].max(), s[sel].min()
        up.append(c + tk * e1 + smax * e2)
        down.append(c + tk * e1 + smin * e2)
    return np.array(up + down[::-1])


def leaf_from_outline(o, nose_x):
    """[Ax, Ay, Bx, By, width, bend]: the shape as one leaf - its tips A
    (towards the nose) and B, its widest width, and how far its middle bows
    off the straight line between the tips."""
    up, down = o[:K], o[K:][::-1]
    mid = (up + down) / 2
    if abs(mid[0][0] - nose_x) > abs(mid[-1][0] - nose_x):
        mid = mid[::-1]; up, down = down[::-1], up[::-1]
    A, B = mid[0], mid[-1]            # A: the tip nearest the face's centre line, in every pose
    width = float(np.max(np.linalg.norm(up - down, axis=1)))
    chord = B - A; L_ = max(np.linalg.norm(chord), 1e-6); nrm = np.array([-chord[1], chord[0]]) / L_
    bend = float(np.dot(mid[K // 2] - (A + B) / 2, nrm))
    return np.array([A[0], A[1], B[0], B[1], width, bend], float)


def leaf_points(q, n=40):
    """The leaf as a closed outline."""
    A, B, width, bend = q[0:2], q[2:4], max(q[4], 0), q[5]
    chord = B - A; L_ = max(np.linalg.norm(chord), 1e-6); nrm = np.array([-chord[1], chord[0]]) / L_
    C = (A + B) / 2 + nrm * 2 * bend           # quadratic curve through the bowed middle
    t = np.linspace(0, 1, n)[:, None]
    P = (1 - t) ** 2 * A + 2 * (1 - t) * t * C + t ** 2 * B
    D = 2 * (1 - t) * (C - A) + 2 * t * (B - C)
    Dn = np.stack([-D[:, 1], D[:, 0]], 1) / np.maximum(np.linalg.norm(D, axis=1, keepdims=True), 1e-6)
    w = width / 2 * np.sin(np.pi * t[:, 0]) ** 0.75
    return np.concatenate([P + Dn * w[:, None], (P - Dn * w[:, None])[::-1]])


def collapsed(pt):
    return np.repeat(np.array([pt], float), 2 * K, 0)


def key_params(r, c):
    L, mouth, belly = nine.FINAL[(r, c)]
    nx, ny, nsx, nsy, nrot, nflip = L["nose"]
    p = {"nose": np.array([nx, ny, nsx, -nsy if nflip else nsy, nrot], float)}
    # stripes (zero size where the pose hides them)
    for side in ("L", "R"):
        k = "stripe" + side
        if k in L:
            x, y, sx, sy, rot, _ = L[k]
            p[k] = np.array([x, y, sx, sy, rot], float)
    # the underside halves
    if (r, c) == ("bottom", "centre"):           # the logo: its two thin strokes
        for side, m in (("L", nine.ck[0]), ("R", nine.ck[1])):
            p["belly" + side] = axis_outline(m, np.array([nx, ny]))
    else:
        m = poly_mask(belly)
        profile = r in ("mid", "bottom") and c != "centre"
        if profile:
            near = "L" if c == "right" else "R"   # looking right, the face's left side is towards us
            p["belly" + near] = axis_outline(m, np.array([nx, ny]))
            far = "R" if near == "L" else "L"
            p["belly" + far] = collapsed(p["belly" + near][0])
        else:
            xx = np.arange(W0)[None, :]
            for side, part in (("L", m & (xx <= nx)), ("R", m & (xx >= nx))):
                p["belly" + side] = axis_outline(part, np.array([nx, ny])) if part.sum() > 30 else None
            for side in ("L", "R"):
                if p["belly" + side] is None:
                    other = p["belly" + ("R" if side == "L" else "L")]
                    p["belly" + side] = collapsed(other[0])
    for side in ("L", "R"):
        o = p["belly" + side]
        if np.ptp(o[:, 0]) < 1 and np.ptp(o[:, 1]) < 1:      # hidden: a leaf of no size at that spot
            p["belly" + side] = np.array([o[0, 0], o[0, 1], o[0, 0], o[0, 1], 0, 0], float)
        else:
            p["belly" + side] = leaf_from_outline(o, nx)
    # hidden stripes: tipping back they go behind the nose; turning, the far
    # one slides behind the near one - both shrinking to nothing
    if r == "top":
        for side, sg in (("L", -1), ("R", 1)):
            p["stripe" + side] = np.array([nx + sg * 0.06 * W0, ny, 0, 0, 0], float)
    for side in ("L", "R"):
        if "stripe" + side not in p:
            other = p["stripe" + ("R" if side == "L" else "L")]
            p["stripe" + side] = np.array([other[0], other[1], 0, 0, other[4]], float)
    # the mouth (zero width where it doesn't show)
    if mouth:
        mx, my, mw, mc = mouth
        p["mouth"] = np.array([mx, my, mw, mc], float)
    else:
        p["mouth"] = np.array([nx, ny + 0.14 * H0, 0, 0.03 * H0], float)
    return p


KEYS = [[key_params(r, c) for c in COLS] for r in ROWS]


def eye_of(img):
    """The see-through eye hole in a stripe image: centre (x, y) and radius."""
    a = np.asarray(img)[..., 3] < 128
    reach = np.zeros_like(a); reach[0], reach[-1], reach[:, 0], reach[:, -1] = a[0], a[-1], a[:, 0], a[:, -1]
    while True:
        g = reach.copy(); g[1:] |= reach[:-1]; g[:-1] |= reach[1:]; g[:, 1:] |= reach[:, :-1]; g[:, :-1] |= reach[:, 1:]
        g &= a
        if (g == reach).all():
            break
        reach = g
    ys, xs = np.nonzero(a & ~reach)
    return xs.mean(), ys.mean(), max(np.ptp(xs), np.ptp(ys)) / 2 + 1


EYE = {k: eye_of(nine.IMG[k]) for k in ("stripeL", "stripeR")}
LAST_EYES = []


def pchip3(a, b, c, t):
    """A smooth curve through three keys at 0, 0.5, 1 that never swings past
    them (monotone cubic), element-wise."""
    a, b, c = (np.asarray(x, float) for x in (a, b, c))
    h = 0.5
    d0, d1 = (b - a) / h, (c - b) / h
    m1 = np.where(d0 * d1 > 0, 2 * d0 * d1 / np.where(d0 + d1 == 0, 1, d0 + d1), 0.0)
    m0 = (3 * d0 - d1) / 2
    m0 = np.where(np.sign(m0) != np.sign(d0), 0, np.where((np.sign(d0) != np.sign(d1)) & (np.abs(m0) > np.abs(3 * d0)), 3 * d0, m0))
    m2 = (3 * d1 - d0) / 2
    m2 = np.where(np.sign(m2) != np.sign(d1), 0, np.where((np.sign(d0) != np.sign(d1)) & (np.abs(m2) > np.abs(3 * d1)), 3 * d1, m2))
    if t <= 0.5:
        y0, y1, s0, s1, x = a, b, m0, m1, t / h
    else:
        y0, y1, s0, s1, x = b, c, m1, m2, (t - h) / h
    h00, h10, h01, h11 = 2 * x**3 - 3 * x**2 + 1, x**3 - 2 * x**2 + x, -2 * x**3 + 3 * x**2, x**3 - x**2
    return h00 * y0 + h10 * h * s0 + h01 * y1 + h11 * h * s1


def blend(u, v):
    out = {}
    for k in KEYS[0][0]:
        rows = [pchip3(KEYS[i][0][k], KEYS[i][1][k], KEYS[i][2][k], u) for i in range(3)]
        out[k] = pchip3(rows[0], rows[1], rows[2], v)
    return out


EYES_NOW = []
LASTP = {}


def draw_frame(p, nudge):
    EYES_NOW.clear()
    canvas = Image.new("RGBA", (W0 * SS, H0 * SS)); masks = {}

    def add(name, lay):
        masks[name] = np.asarray(lay.resize((W0, H0), Image.LANCZOS))[..., 3] > 110
        canvas.alpha_composite(lay)

    for side in ("L", "R"):
        x, y, sx, sy, rot = p["stripe" + side]
        if sx < 0.03 or sy < 0.03:
            continue   # nothing left (small ones sit behind the nose or the near stripe)
        dx, dy = nudge.get("stripe" + side, (0, 0))
        img = nine.IMG["stripe" + side]
        img = img.resize((max(1, int(img.width * sx * SS)), max(1, int(img.height * sy * SS))), Image.LANCZOS)
        if rot:
            img = img.rotate(rot, resample=Image.BICUBIC, expand=True)
        lay = Image.new("RGBA", canvas.size); lay.alpha_composite(img, (int((x + dx) * SS - img.width / 2), int((y + dy) * SS - img.height / 2)))
        add("stripe" + side, lay)
        src_ = nine.IMG["stripe" + side]; ex, ey, er = EYE["stripe" + side]
        ox_, oy_ = (ex - src_.width / 2) * sx, (ey - src_.height / 2) * sy
        t_ = np.radians(rot)
        rx, ry = ox_ * np.cos(t_) + oy_ * np.sin(t_), -ox_ * np.sin(t_) + oy_ * np.cos(t_)
        EYES_NOW.append([side, x + dx + rx, y + dy + ry, 2 * er * sx, 2 * er * sy])
    for side in ("L", "R"):
        q = p["belly" + side]
        if q[4] < 0.004 * W0 or np.hypot(q[2] - q[0], q[3] - q[1]) < 0.01 * W0:
            continue   # nothing left
        loop = [tuple(pt) for pt in leaf_points(q)]
        lay = Image.new("RGBA", canvas.size)
        ImageDraw.Draw(lay).polygon([(qx * SS, qy * SS) for qx, qy in loop], fill=(0, 0, 0, 255))
        add("belly" + side, lay)
    x, y, sx, sy, rot = p["nose"]
    flip = sy < 0; sy = max(abs(sy), 0.12)   # flips at its thinnest, edge-on
    img = nine.IMG["nose"].transpose(Image.FLIP_TOP_BOTTOM) if flip else nine.IMG["nose"]
    img = img.resize((max(1, int(img.width * sx * SS)), max(1, int(img.height * sy * SS))), Image.LANCZOS)
    if rot:
        img = img.rotate(rot, resample=Image.BICUBIC, expand=True)
    lay = Image.new("RGBA", canvas.size); lay.alpha_composite(img, (int(x * SS - img.width / 2), int(y * SS - img.height / 2)))
    add("nose", lay)
    mx, my, mw, mc = p["mouth"]
    if mw > 0.02 * W0:
        # grows from a dot: thin while short, full weight once it's wide
        thick = 0.018 * W0 * min(1.0, mw / (0.06 * W0))
        lay = Image.new("RGBA", canvas.size)
        ImageDraw.Draw(lay).arc([(mx - mw) * SS, (my - max(mc, 1)) * SS, (mx + mw) * SS, (my + max(mc, 1)) * SS], start=18, end=162,
                                fill=(0, 0, 0, 255), width=max(1, int(thick * SS)))
        add("mouth", lay)
    return canvas.resize((W0, H0), Image.LANCZOS), masks


ALLOWED = {frozenset(("stripeL", "nose")), frozenset(("stripeR", "nose")), frozenset(("bellyL", "bellyR")),
           frozenset(("stripeL", "stripeR"))}


def problems(masks):
    names = list(masks); grown = {}
    for n in names:
        m = masks[n].copy()
        for _ in range(int(nine.MIN_GAP * W0 / SIZE)):
            m[1:] |= m[:-1]; m[:-1] |= m[1:]; m[:, 1:] |= m[:, :-1]; m[:, :-1] |= m[:, 1:]
        grown[n] = m
    touching = [(a, b) for i, a in enumerate(names) for b in names[i + 1:]
                if frozenset((a, b)) not in ALLOWED and (grown[a] & masks[b]).any()]
    bad = [n for n in names if (masks[n] & (nine.zone if n == "nose" else nine.BAD)).any()]
    return touching, bad


def frame(i, j):
    u, v = j / (N - 1), i / (N - 1)
    p = blend(u, v)
    for k in ("stripeL", "stripeR"):
        p[k][2:4] = np.clip(p[k][2:4], 0, None)
    p["mouth"][2] = max(p["mouth"][2], 0)
    # the smile belongs to the tipped-up poses: gone well before the logo
    p["mouth"][2] *= float(np.clip((0.88 - v) / 0.28, 0, 1))
    # the nose under the pointer; the face follows it
    dot = ((0.10 + 0.80 * u) * W0, (0.10 + 0.80 * v) * H0)
    x, y, sx, sy, rot = p["nose"]
    tx, ty = nine.closest_nose(dot[0], dot[1], sx, max(abs(sy), 0.12), rot, sy < 0)
    dx, dy = tx - x, ty - y
    for k in ("stripeL", "stripeR", "nose", "mouth"):
        p[k][0] += dx; p[k][1] += dy
    for k in ("bellyL", "bellyR"):
        p[k][[0, 2]] += dx; p[k][[1, 3]] += dy
    nudge = {}
    for it in range(60):
        out, masks = draw_frame(p, nudge)
        touching, bad = problems(masks)
        if not touching and not bad:
            return out, True, (tx, ty), dot
        moved = False
        for a, b in touching:
            mover = next((n for n in (a, b) if n.startswith("stripe")), None)
            if mover:
                other = b if mover == a else a
                cy_, cx_ = np.nonzero(masks[mover]); oy_, ox_ = np.nonzero(masks[other])
                vx, vy = cx_.mean() - ox_.mean(), cy_.mean() - oy_.mean(); n_ = max(np.hypot(vx, vy), 1e-6)
                ox, oy = nudge.get(mover, (0, 0)); nudge[mover] = (ox + vx / n_ * 0.008 * W0, oy + vy / n_ * 0.008 * W0); moved = True
            elif any(n.startswith("belly") for n in (a, b)):
                bn = next(n for n in (a, b) if n.startswith("belly"))
                other = b if bn == a else a
                cy_, cx_ = np.nonzero(masks[bn]); oy_, ox_ = np.nonzero(masks[other])
                vx, vy = cx_.mean() - ox_.mean(), cy_.mean() - oy_.mean(); n_ = max(np.hypot(vx, vy), 1e-6)
                p[bn][[0, 2]] += vx / n_ * 0.008 * W0; p[bn][[1, 3]] += vy / n_ * 0.008 * W0
                p[bn][4] *= 0.99; moved = True
        for n in bad:
            if n.startswith("stripe"):
                cy_, cx_ = np.nonzero(masks[n]); vx, vy = W0 / 2 - cx_.mean(), H0 / 2 - cy_.mean(); n_ = max(np.hypot(vx, vy), 1e-6)
                ox, oy = nudge.get(n, (0, 0)); nudge[n] = (ox + vx / n_ * 0.008 * W0, oy + vy / n_ * 0.008 * W0); moved = True
            elif n.startswith("belly"):
                q = p[n]; cx_, cy_ = (q[0] + q[2]) / 2, (q[1] + q[3]) / 2
                for a_, b_ in ((0, 1), (2, 3)):
                    q[a_] = cx_ + (q[a_] - cx_) * 0.97; q[b_] = cy_ + (q[b_] - cy_) * 0.97
                q[4] *= 0.97; moved = True
        if not moved:
            break
    LASTP[(i, j)] = (touching, bad)
    return out, False, (tx, ty), dot


if __name__ == "__main__":
    report = {}
    fails = []
    EYEMAP = {}
    only = os.environ.get("ONLY")   # "i,j;i,j" to render a sample
    cells = [tuple(map(int, c.split(","))) for c in only.split(";")] if only else [(i, j) for i in range(N) for j in range(N)]
    for i in range(N):
        for j in range(N):
            if (i, j) not in cells:
                continue
            out, ok, nose_xy, dot = frame(i, j)
            if os.environ.get("HIRES"):
                # full size, no corners (the page draws them), plus the eyes:
                # [x, y, w, h] as fractions of the head box, and whether seen
                out.save(f"{OUT}/h_{i:02d}_{j:02d}.png")
                a_ = np.asarray(out)[..., 3]
                eyes = {}
                for side, ex, ey, ew, eh in EYES_NOW:
                    iy, ix = int(round(ey)), int(round(ex))
                    seen = bool(0 <= iy < H0 and 0 <= ix < W0 and a_[iy, ix] < 128)
                    eyes[side] = [round(ex / W0, 4), round(ey / H0, 4), round(ew / W0, 4), round(eh / H0, 4), seen]
                EYEMAP[f"{i}_{j}"] = [eyes.get("L", [0, 0, 0, 0, False]), eyes.get("R", [0, 0, 0, 0, False])]
            out.alpha_composite(nine.corners)
            out.resize((SIZE, round(SIZE * H0 / W0)), Image.LANCZOS).save(f"{OUT}/f_{i:02d}_{j:02d}.png")
            d = float(np.hypot(nose_xy[0] - dot[0], nose_xy[1] - dot[1]) * SIZE / W0)
            report[f"{i}_{j}"] = {"ok": ok, "nose_from_dot_px": round(d, 1)}
            if not ok:
                fails.append((i, j))
        if not only:
            print(f"row {i + 1}/{N} done", flush=True)
    if not only:
        json.dump({"n": N, "frames": report, "eyes": EYEMAP}, open(f"{OUT}/tween.json", "w"), indent=0)
    print("failing frames:", fails or "none")
    print("nose from dot: max", max(r["nose_from_dot_px"] for r in report.values()), "px")
