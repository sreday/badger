"""Nine key poses: the nose sits under the pointer (Marek 2026-11-09: "map 9
border positions of the nose that follows 1:1 the cursor ... the nose should
be under the cursor at all times").

Cut-out style: the logo's six shapes (two stripes with ears + eyes, two
cheek strokes, the nose), each moved / squeezed / tilted / flipped whole.
- Rows: bottom = the logo (bottom-middle IS the resting logo); middle = the
  head tipped halfway up, nose between the stripes, a single-line mouth
  under it; top = tipped right back: the underside, nose at the top, mouth,
  the cheek strokes as the jaw, ear tips far away at the bottom.
- Columns: a turn on top of the row: the far stripe and cheek squeeze thin,
  the near side widens, everything swings a little towards the nose.
Checks on every pose: no two shapes touch, nothing in the orange corners or
cut off at the edge. The nose never moves off its spot; the rest makes room.

Usage: nine.py HEAD_PNG OUTDIR SIZE  -> OUTDIR/pose_<row>_<col>.png + nine.json
"""
import json
import os
import sys

import numpy as np
from PIL import Image, ImageDraw

SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
os.makedirs(OUT, exist_ok=True)
SS = 2
MIN_GAP = 3

im = Image.open(SRC).convert("RGBA")
W0, H0 = im.size
a = np.asarray(im).astype(int)
r, g, b, al = [a[..., i] for i in range(4)]
orange = (al > 12) & (r > g + 38) & (r > 115) & (b < 115)
ink = np.where(orange, 0, al).astype(np.uint8)
corners_rgba = np.asarray(im).copy(); corners_rgba[~orange] = 0
corners = Image.fromarray(corners_rgba)
zone = orange.copy()
for _ in range(6):
    zone[1:] |= zone[:-1]; zone[:-1] |= zone[1:]; zone[:, 1:] |= zone[:, :-1]; zone[:, :-1] |= zone[:, 1:]
edge = np.zeros_like(zone); m_ = int(0.012 * W0)
edge[:m_], edge[-m_:], edge[:, :m_], edge[:, -m_:] = True, True, True, True
BAD = zone | edge

parts, left_ = [], ink > 128
while left_.any():
    ys, xs = np.nonzero(left_)
    m = np.zeros_like(left_); m[ys[0], xs[0]] = True
    while True:
        g2 = m.copy()
        g2[1:] |= m[:-1]; g2[:-1] |= m[1:]; g2[:, 1:] |= m[:, :-1]; g2[:, :-1] |= m[:, 1:]
        g2 &= left_
        if (g2 == m).all():
            break
        m = g2
    left_ &= ~m
    if m.sum() > 50:
        parts.append(m)
parts.sort(key=lambda m: -m.sum())


def cut(mask):
    mm = mask.copy()
    for _ in range(2):
        mm[1:] |= mm[:-1]; mm[:-1] |= mm[1:]; mm[:, 1:] |= mm[:, :-1]; mm[:, :-1] |= mm[:, 1:]
    alpha = np.where(mm, ink, 0).astype(np.uint8)
    ys, xs = np.nonzero(alpha)
    x0, y0, x1, y1 = xs.min(), ys.min(), xs.max() + 1, ys.max() + 1
    rgba = np.zeros((H0, W0, 4), np.uint8); rgba[..., 3] = alpha
    return Image.fromarray(rgba).crop((x0, y0, x1, y1)), ((x0 + x1) / 2, (y0 + y1) / 2)


st = sorted(parts[:2], key=lambda m: np.nonzero(m)[1].mean())
ck = sorted(parts[3:5], key=lambda m: np.nonzero(m)[1].mean())
IMG, HOME = {}, {}
for name, mask in (("stripeL", st[0]), ("stripeR", st[1]), ("nose", parts[2]), ("cheekL", ck[0]), ("cheekR", ck[1])):
    IMG[name], HOME[name] = cut(mask)
# ear tips: the top third of each stripe (for the underside view)
for side in ("L", "R"):
    s_ = IMG["stripe" + side]
    IMG["ear" + side] = s_.crop((0, 0, s_.width, int(s_.height * 0.30)))
NOSE_HOME = HOME["nose"]


# The nine poses, designed one by one, around the nose: for each shape its
# offset from the nose (x, y as fractions of the head box), squeeze x,
# squeeze y, tilt, flipped; squeeze 0 = hidden (turned away behind the
# snout). Right column only; the left is its mirror. Bottom-middle is the
# resting logo itself.
POSES = {
    ("bottom", "centre"): "logo",
    # nose in the middle: tipped halfway up; stripes foreshorten above it,
    # the mouth shows under it, the cheek strokes frame the jaw
    # the stripes run down into the nose (their tips tucked under it), so
    # they're longer and the eyes bigger
    ("mid", "centre"):    {"stripeL": (-0.21, -0.23, 0.95, 0.66, 9, False), "stripeR": (0.21, -0.23, 0.95, 0.66, -9, False),
                           # lower and wider: the neck below the chin, not a beard
                           "belly": [(-0.36, 0.17), (-0.20, 0.26), (0, 0.31), (0.20, 0.26), (0.36, 0.17),
                                     (0.31, 0.34), (0, 0.41), (-0.31, 0.34)],
                           "nose": (0, 0, 0.86, 0.80, 0, False), "mouth": (0, 0.14, 0.10, 0.035)},
    # nose at the top: tipped right back, the underside: mouth, the cheek
    # strokes as the jaw, ear tips far away at the bottom
    ("top", "centre"):    {"belly": [(-0.27, 0.21), (-0.10, 0.36), (0, 0.50), (0.10, 0.36), (0.27, 0.21),
                                     (0.35, 0.45), (0.27, 0.66), (0, 0.73), (-0.27, 0.66), (-0.35, 0.45)],
                           "nose": (0, 0, 0.95, 0.58, 0, True), "mouth": (0, 0.12, 0.085, 0.05)},
    # turned: the near side (left) full width, the far side squeezed
    ("top", "right"):     {"belly": [(-0.29, 0.20), (-0.13, 0.35), (-0.04, 0.48), (0.03, 0.35), (0.10, 0.22),
                                     (0.14, 0.44), (0.09, 0.60), (-0.06, 0.67), (-0.31, 0.61), (-0.38, 0.42)],
                           "nose": (0, 0, 0.84, 0.58, 0, True), "mouth": (-0.02, 0.12, 0.07, 0.05)},
}


# Looking right: a side view. The nose points right; the near stripe lies
# along the head from the nose back to the ear (eye in it); the cheek stroke
# is the jaw underneath; the mouth is a line under the nose; the far side is
# hidden. Offsets from the nose; rot is counter-clockwise, in degrees.
PROFILE_RIGHT = {"nose": (0, 0, 1.0, 0.86, 90, False),
                 "stripeL": (-0.53, -0.10, 1.0, 1.14, 73, False),   # long: the ear out towards the far edge
                 # the black neck: under the jaw back to behind the ear, white
                 # cheek between it and the eye stripe
                 "belly": [(-0.17, 0.17), (-0.35, 0.11), (-0.57, 0.08), (-0.77, 0.12), (-0.78, 0.22),
                           (-0.62, 0.35), (-0.36, 0.36), (-0.19, 0.27)],
                 "mouth": None}   # the smile only shows tipped up (mid-centre, top row)


def tilt(pose, deg):
    """The whole pose turned about the nose by deg (clockwise = the nose
    points lower)."""
    t = np.radians(deg); out = {}
    for k, v in pose.items():
        if v is None:
            out[k] = None
            continue
        if k == "belly":
            out[k] = []
            for bx, by in v:
                y_ = by * H0 / W0
                out[k].append((bx * np.cos(t) - y_ * np.sin(t), (bx * np.sin(t) + y_ * np.cos(t)) * W0 / H0))
            continue
        x, y = v[0], v[1] * H0 / W0
        rx, ry = x * np.cos(t) - y * np.sin(t), x * np.sin(t) + y * np.cos(t)
        ry = ry * W0 / H0
        out[k] = (rx, ry) + tuple(v[2:]) if k == "mouth" else (rx, ry, v[2], v[3], v[4] - deg, v[5])
    return out


POSES[("mid", "right")] = PROFILE_RIGHT
POSES[("bottom", "right")] = tilt(PROFILE_RIGHT, 32)


def mirror(pose):
    if pose == "logo":
        return pose
    swap = {"stripeL": "stripeR", "stripeR": "stripeL", "cheekL": "cheekR", "cheekR": "cheekL", "earL": "earR", "earR": "earL", "nose": "nose"}
    out = {}
    for k, v in pose.items():
        if k == "belly":
            out[k] = [(-bx, by) for bx, by in v]
        elif k == "mouth":
            out[k] = None if v is None else (-v[0], v[1], v[2], v[3])
        elif k == "nose":
            x, y, sx, sy, rot, flip = v
            out[k] = (-x, y, sx, sy, -rot, flip)
        else:
            x, y, sx, sy, rot, flip = v
            out[swap[k]] = (-x, y, sx, sy, -rot, flip)
    return out


for row in ("bottom", "mid", "top"):
    POSES[(row, "left")] = mirror(POSES[(row, "right")])


LOGO = {"stripeL": (0.268, 0.456), "stripeR": (0.730, 0.456), "cheekL": (0.193, 0.740), "cheekR": (0.807, 0.740), "nose": (0.500, 0.898)}


def layout(row, col, nose_xy=None):
    """Absolute layout: the pose built around the nose at nose_xy (pixels)."""
    P_ = POSES[(row, col)]
    if P_ == "logo":
        return {k: (x * W0, y * H0, 1, 1, 0, False) for k, (x, y) in LOGO.items()}, None
    nx, ny = nose_xy
    L = {k: (nx + v[0] * W0, ny + v[1] * H0) + tuple(v[2:]) for k, v in P_.items() if k not in ("mouth", "belly") and v[2] > 0}
    BELLY["pts"] = [(nx + bx * W0, ny + by * H0) for bx, by in P_.get("belly", [])]
    m = P_["mouth"]
    mouth = None if m is None else (nx + m[0] * W0, ny + m[1] * H0, m[2] * W0, m[3] * H0)
    return L, mouth


# The cheek strokes are thin slivers in the logo only because of the
# perspective; away from the resting pose they open up into proper stripes
# (Marek: "much more pronounced"). Widened across their own length.
CHEEK_THICK = {"top": 1.8, "mid": 2.4, "bottom": 2.0}
THICK = {"now": 1.0}
_thick_cache = {}


def thick(name, t):
    """The part widened t times across its long axis (curve kept)."""
    if t == 1.0:
        return IMG[name]
    key = (name, t)
    if key not in _thick_cache:
        im_ = IMG[name]
        a_ = np.asarray(im_)[..., 3] > 128
        ys_, xs_ = np.nonzero(a_)
        cov = np.cov(np.stack([xs_ - xs_.mean(), ys_ - ys_.mean()]))
        ev, evec = np.linalg.eigh(cov)
        ang = np.degrees(np.arctan2(evec[1, 1], evec[0, 1]))  # long axis, image coords (y down)
        big = Image.new("RGBA", (im_.width * 3, im_.height * 3)); big.alpha_composite(im_, (im_.width, im_.height))
        r1 = big.rotate(ang, resample=Image.BICUBIC)            # long axis now horizontal
        r1 = r1.resize((r1.width, int(r1.height * t)), Image.LANCZOS)
        r2 = r1.rotate(-ang, resample=Image.BICUBIC)
        bb = r2.getbbox()
        _thick_cache[key] = r2.crop(bb)
    return _thick_cache[key]


OUTLINE = {"on": False}
TUCK = {"pairs": set()}   # shapes allowed to tuck under the nose in this pose
BELLY = {"pts": []}
PLACED = {}


def smooth(pts, n=24):
    """Catmull-Rom through the points."""
    P_ = [pts[0]] + list(pts) + [pts[-1]]
    out = []
    for i in range(1, len(P_) - 2):
        p0, p1, p2, p3 = [np.array(q, float) for q in P_[i - 1:i + 3]]
        for t in np.linspace(0, 1, n, endpoint=False):
            out.append(tuple(0.5 * ((2 * p1) + (-p0 + p2) * t + (2 * p0 - 5 * p1 + 4 * p2 - p3) * t ** 2 + (-p0 + 3 * p1 - 3 * p2 + p3) * t ** 3)))
    out.append(tuple(pts[-1]))
    return out


def draw(L, mouth, G=1.0, nudge=None):
    nudge = nudge or {}
    nx, ny = L["nose"][0], L["nose"][1]
    canvas = Image.new("RGBA", (W0 * SS, H0 * SS)); masks = {}
    for name, (x, y, sx, sy, rot, flip) in L.items():
        src_ = thick(name, THICK["now"]) if name.startswith("cheek") else IMG[name]
        p = src_.transpose(Image.FLIP_TOP_BOTTOM) if flip else src_
        k = 1.0 if name == "nose" else G
        p = p.resize((max(1, int(p.width * sx * k * SS)), max(1, int(p.height * sy * k * SS))), Image.LANCZOS)
        if rot:
            p = p.rotate(rot, resample=Image.BICUBIC, expand=True)
        dx, dy = nudge.get(name, (0, 0))
        if name != "nose":  # the rest shrinks towards the nose, never the nose
            x, y = nx + (x + dx - nx) * G, ny + (y + dy - ny) * G
        lay = Image.new("RGBA", canvas.size); lay.alpha_composite(p, (int(x * SS - p.width / 2), int(y * SS - p.height / 2)))
        masks[name] = np.asarray(lay.resize((W0, H0), Image.LANCZOS))[..., 3] > 110
        canvas.alpha_composite(lay)
        if name.startswith("cheek"):
            PLACED[name] = (x, y, p.width / SS, p.height / SS)
    if BELLY["pts"]:
        # the black underside of the head: one smooth closed shape
        pts = BELLY["pts"]
        loop = smooth(pts + pts[:3], 16)[16:16 * (len(pts) + 1)]
        lay = Image.new("RGBA", canvas.size)
        ImageDraw.Draw(lay).polygon([(qx * SS, qy * SS) for qx, qy in loop], fill=(0, 0, 0, 255))
        masks["belly"] = np.asarray(lay.resize((W0, H0), Image.LANCZOS))[..., 3] > 110
        canvas.alpha_composite(lay)
    if mouth:
        mx, my, mw, mc = mouth
        lay = Image.new("RGBA", canvas.size)
        ImageDraw.Draw(lay).arc([(mx - mw) * SS, (my - mc) * SS, (mx + mw) * SS, (my + mc) * SS], start=18, end=162,
                                fill=(0, 0, 0, 255), width=int(0.018 * W0 * SS))
        masks["mouth"] = np.asarray(lay.resize((W0, H0), Image.LANCZOS))[..., 3] > 110
        canvas.alpha_composite(lay)
    out = canvas.resize((W0, H0), Image.LANCZOS)
    return out, masks


def check(masks):
    names = list(masks)
    grown = {}
    for n in names:
        m = masks[n].copy()
        for _ in range(int(MIN_GAP * W0 / SIZE)):
            m[1:] |= m[:-1]; m[:-1] |= m[1:]; m[:, 1:] |= m[:, :-1]; m[:, :-1] |= m[:, 1:]
        grown[n] = m
    touching = [(x, y) for i, x in enumerate(names) for y in names[i + 1:] if (grown[x] & masks[y]).any()
                and (x, y) not in TUCK["pairs"] and (y, x) not in TUCK["pairs"]
                and not ({x, y} & {"outline"} and ({x, y} - {"outline"}).pop().startswith("cheek"))]  # the outline is drawn round the cheeks
    bad = [n for n in names if (masks[n] & (zone if n == "nose" else BAD)).any()]
    return touching, bad


# The pointer is a moving dot; the nose goes as close to it as it can while
# staying clear of the orange corners and inside the frame.
DOT = {"left": 0.10, "centre": 0.50, "right": 0.90, "top": 0.10, "mid": 0.50, "bottom": 0.90}


def nose_mask(sx, sy, rot, flip):
    p = IMG["nose"].transpose(Image.FLIP_TOP_BOTTOM) if flip else IMG["nose"]
    p = p.resize((max(1, int(p.width * sx)), max(1, int(p.height * sy))), Image.LANCZOS)
    if rot:
        p = p.rotate(rot, resample=Image.BICUBIC, expand=True)
    m = np.asarray(p)[..., 3] > 60
    g_ = int(MIN_GAP * W0 / SIZE)
    for _ in range(g_):
        m[1:] |= m[:-1]; m[:-1] |= m[1:]; m[:, 1:] |= m[:, :-1]; m[:, :-1] |= m[:, 1:]
    return m


def closest_nose(dot_x, dot_y, sx, sy, rot, flip):
    """The nearest spot to the dot where the nose fits."""
    m = nose_mask(sx, sy, rot, flip); mh, mw = m.shape
    best = None
    for rad in range(0, int(0.6 * W0), 2):
        ring = [(dot_x + rad * np.cos(t), dot_y + rad * np.sin(t)) for t in np.linspace(0, 2 * np.pi, max(8, rad * 2), endpoint=False)] if rad else [(dot_x, dot_y)]
        ring.sort(key=lambda q: (q[0] - dot_x) ** 2 + (q[1] - dot_y) ** 2)
        for qx, qy in ring:
            x0, y0 = int(round(qx - mw / 2)), int(round(qy - mh / 2))
            if x0 < 0 or y0 < 0 or x0 + mw > W0 or y0 + mh > H0:
                continue
            region = zone[y0:y0 + mh, x0:x0 + mw]
            if region.shape == m.shape and not (region & m).any():
                return qx, qy
    return dot_x, dot_y


def solve(row, col):
    THICK["now"] = 1.0 if POSES[(row, col)] == "logo" else CHEEK_THICK[row]
    OUTLINE["on"] = False
    TUCK["pairs"] = {("stripeL", "nose"), ("stripeR", "nose")} if (row, col) == ("mid", "centre") else set()
    BELLY["pts"] = []
    if POSES[(row, col)] == "logo":
        L, mouth = layout(row, col)
    else:
        _, _, sx, sy, rot, flip = POSES[(row, col)]["nose"]
        nx, ny = closest_nose(DOT[col] * W0, DOT[row] * H0, sx, sy, rot, flip)
        L, mouth = layout(row, col, (nx, ny))
    NOSE_AT[(row, col)] = (L["nose"][0] / W0, L["nose"][1] / H0)
    nudge = {}
    for it in range(120):
        out, masks = draw(L, mouth, 1.0, nudge)
        touching, bad = check(masks)
        if not touching and not bad:
            return out, True, 1.0
        LAST[(row, col)] = (touching, bad)
        moves = {}
        for x, y in touching:   # the smaller shape steps away (never the nose or mouth)
            cand = [n for n in (x, y) if n not in ("nose", "mouth", "outline", "belly")]
            if not cand:
                continue
            mover = min(cand, key=lambda n: masks[n].sum())
            other = y if mover == x else x
            cy_, cx_ = np.nonzero(masks[mover]); oy_, ox_ = np.nonzero(masks[other])
            vx, vy = cx_.mean() - ox_.mean(), cy_.mean() - oy_.mean()
            n = max(np.hypot(vx, vy), 1e-6)
            mx, my = moves.get(mover, (0, 0)); moves[mover] = (mx + vx / n, my + vy / n)
        for n_ in bad:          # off the corners / edge: step towards the middle
            if n_ in ("nose", "mouth"):
                continue
            cy_, cx_ = np.nonzero(masks[n_])
            vx, vy = W0 / 2 - cx_.mean(), H0 / 2 - cy_.mean()
            n = max(np.hypot(vx, vy), 1e-6)
            mx, my = moves.get(n_, (0, 0)); moves[n_] = (mx + vx / n, my + vy / n)
        for n_, (vx, vy) in moves.items():
            n = max(np.hypot(vx, vy), 1e-6)
            ox, oy = nudge.get(n_, (0.0, 0.0))
            nudge[n_] = (ox + vx / n * 0.008 * W0, oy + vy / n * 0.008 * W0)
    return out, False, 1.0


NOSE_AT = {}
LAST = {}
report = {}
for row in ("top", "mid", "bottom"):
    for col in ("left", "centre", "right"):
        out, ok, G = solve(row, col)
        if col == "centre" and row != "bottom":
            # facing forward: exactly symmetric - the left half is the right
            # half mirrored (bottom-centre is the logo itself, left as is)
            arr = np.asarray(out).copy(); h_ = W0 // 2
            arr[:, :h_] = arr[:, W0 - 1:W0 - 1 - h_:-1]
            out = Image.fromarray(arr)
        out.alpha_composite(corners)
        out.resize((SIZE, round(SIZE * H0 / W0)), Image.LANCZOS).save(f"{OUT}/pose_{row}_{col}.png")
        nx, ny = NOSE_AT[(row, col)]
        dist = np.hypot((nx - DOT[col]) * W0, (ny - DOT[row]) * H0) * SIZE / W0
        report[f"{row}_{col}"] = {"ok": ok, "shrink": round(G, 2), "nose": [round(nx, 3), round(ny, 3)], "dot": [DOT[col], DOT[row]]}
        if not ok:
            print("   still:", LAST.get((row, col)))
        print(f"{row:6s} {col:6s} {'OK  ' if ok else 'FAIL'} rest at {G:.2f}  nose {nx:.2f},{ny:.2f}  dot {DOT[col]:.2f},{DOT[row]:.2f}  ({dist:.0f}px away)", flush=True)
json.dump(report, open(f"{OUT}/nine.json", "w"), indent=1)
