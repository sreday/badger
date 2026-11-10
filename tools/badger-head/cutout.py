"""Key poses as cut-out animation: the logo's six shapes (two stripes with the
ears and eyes, two cheek strokes, the nose) each moved, squeezed and tilted
as a whole, so every shape stays exactly the logo's - nothing tears, flattens
or merges. (Bending the whole picture did all three.)

Every pose is checked: no two shapes touch (at least MIN_GAP px apart at the
output size) and nothing touches the orange corners.

Usage: cutout.py HEAD_PNG OUTDIR SIZE
"""
import os
import sys

import numpy as np
from PIL import Image

SRC, OUT, SIZE = sys.argv[1], sys.argv[2], int(sys.argv[3])
os.makedirs(OUT, exist_ok=True)
SS = 2
MIN_GAP = 3  # px at the output size

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

# the six parts
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
    return {"img": Image.fromarray(rgba).crop((x0, y0, x1, y1)), "cx": (x0 + x1) / 2, "cy": (y0 + y1) / 2}


P = {}
st = sorted(parts[:2], key=lambda m: np.nonzero(m)[1].mean())
ck = sorted(parts[3:5], key=lambda m: np.nonzero(m)[1].mean())
P["stripeL"], P["stripeR"] = cut(st[0]), cut(st[1])
P["nose"] = cut(parts[2])
P["cheekL"], P["cheekR"] = cut(ck[0]), cut(ck[1])


HCX, HCY = W0 / 2, H0 / 2


def render(moves, G=1.0, nudge=None):
    """moves: part -> (dx, dy, sx, sy, rot) as fractions of the head width.
    G shrinks the whole pose towards the middle; nudge moves single parts."""
    nudge = nudge or {}
    layers = {}
    canvas = Image.new("RGBA", (W0 * SS, H0 * SS))
    for name, p in P.items():
        dx, dy, sx, sy, rot = moves.get(name, (0, 0, 1, 1, 0))
        img = p["img"].resize((max(1, int(p["img"].width * sx * G * SS)), max(1, int(p["img"].height * sy * G * SS))), Image.LANCZOS)
        if rot:
            img = img.rotate(rot, resample=Image.BICUBIC, expand=True)
        nx_, ny_ = nudge.get(name, (0.0, 0.0))
        pcx = HCX + (p["cx"] + dx * W0 + nx_ - HCX) * G
        pcy = HCY + (p["cy"] + dy * W0 + ny_ - HCY) * G
        x = pcx * SS - img.width / 2
        y = pcy * SS - img.height / 2
        lay = Image.new("RGBA", canvas.size); lay.alpha_composite(img, (int(x), int(y)))
        layers[name] = np.asarray(lay.resize((W0, H0), Image.LANCZOS))[..., 3] > 110
        canvas.alpha_composite(img, (int(x), int(y)))
    out = canvas.resize((W0, H0), Image.LANCZOS)
    # checks
    names = list(layers)
    grown = {}
    for n in names:
        m = layers[n].copy()
        for _ in range(int(MIN_GAP * W0 / SIZE)):
            m[1:] |= m[:-1]; m[:-1] |= m[1:]; m[:, 1:] |= m[:, :-1]; m[:, :-1] |= m[:, 1:]
        grown[n] = m
    touching = [f"{a}-{b}" for i, a in enumerate(names) for b in names[i + 1:] if (grown[a] & layers[b]).any()]
    edge = np.zeros_like(zone); m_ = int(0.012 * W0)
    edge[:m_], edge[-m_:], edge[:, :m_], edge[:, -m_:] = True, True, True, True
    in_corner = [n for n in names if (layers[n] & (zone | edge)).any()]   # the corners, or cut off at the edge
    out.alpha_composite(corners)
    cent = {n: (np.nonzero(layers[n])[1].mean(), np.nonzero(layers[n])[0].mean()) for n in names if layers[n].any()}
    return out.resize((SIZE, round(SIZE * H0 / W0)), Image.LANCZOS), touching, in_corner, cent


AREA = {n: (np.asarray(p["img"])[..., 3] > 128).sum() for n, p in P.items()}


def solve(moves):
    """Nudge touching parts apart and shrink the pose off the corners until
    both checks pass."""
    G, nudge = 1.0, {}
    for _ in range(80):
        img, touching, in_corner, cent = render(moves, G, nudge)
        if not touching and not in_corner:
            return img, G, nudge, True
        for pair in touching:
            a_, b_ = pair.split("-")
            small, big = (a_, b_) if AREA[a_] < AREA[b_] else (b_, a_)
            vx, vy = cent[small][0] - cent[big][0], cent[small][1] - cent[big][1]
            n = max(np.hypot(vx, vy), 1e-6)
            ox, oy = nudge.get(small, (0.0, 0.0))
            nudge[small] = (ox + vx / n * 0.012 * W0, oy + vy / n * 0.012 * W0)
        if in_corner:
            G *= 0.98
    return img, G, nudge, False


def mirror(moves):
    """The same pose turned the other way."""
    swap = {"stripeL": "stripeR", "stripeR": "stripeL", "cheekL": "cheekR", "cheekR": "cheekL", "nose": "nose"}
    return {swap[k]: (-dx, dy, sx, sy, -rot) for k, (dx, dy, sx, sy, rot) in moves.items()}


# Right = the nose all the way to the right: the near side (left half)
# widens and swings round, the far side squeezes thin and slides behind.
RIGHT = {
    "squeeze": {"nose": (0.20, -0.02, 0.86, 1.0, 0), "stripeL": (0.10, 0.0, 1.06, 1.0, -4), "stripeR": (0.14, 0.0, 0.52, 1.0, -3),
                "cheekL": (0.13, -0.01, 1.05, 1.0, -6), "cheekR": (0.08, -0.02, 0.45, 0.95, -8)},
    "swing":   {"nose": (0.22, -0.05, 0.84, 0.98, 10), "stripeL": (0.10, -0.01, 1.04, 1.0, 4), "stripeR": (0.14, -0.03, 0.55, 0.98, 8),
                "cheekL": (0.15, -0.04, 1.02, 1.0, 6), "cheekR": (0.07, -0.05, 0.45, 0.95, 4)},
    "slide":   {"nose": (0.17, 0.0, 0.92, 1.0, 0), "stripeL": (0.08, 0.0, 1.0, 1.0, 0), "stripeR": (0.12, 0.0, 0.72, 1.0, 0),
                "cheekL": (0.11, 0.0, 1.0, 1.0, 0), "cheekR": (0.07, 0.0, 0.62, 1.0, 0)},
}
# Down = the nose all the way down: the head squashes top to bottom, the
# ears tip forward (shorter), the nose comes forward (bigger) to the bottom.
DOWN = {
    "squeeze": {"nose": (0, 0.0, 1.12, 0.96, 0), "stripeL": (0.01, 0.10, 1.0, 0.74, 0), "stripeR": (-0.01, 0.10, 1.0, 0.74, 0),
                "cheekL": (0.02, 0.0, 1.02, 0.85, 0), "cheekR": (-0.02, 0.0, 1.02, 0.85, 0)},
    "nod":     {"nose": (0, 0.0, 1.16, 1.0, 0), "stripeL": (0.02, 0.08, 0.98, 0.78, -5), "stripeR": (-0.02, 0.08, 0.98, 0.78, 5),
                "cheekL": (0.03, -0.01, 1.0, 0.85, -6), "cheekR": (-0.03, -0.01, 1.0, 0.85, 6)},
    "duck":    {"nose": (0, 0.0, 1.08, 0.94, 0), "stripeL": (0.0, 0.14, 1.03, 0.66, 0), "stripeR": (0.0, 0.14, 1.03, 0.66, 0),
                "cheekL": (0.0, 0.02, 1.04, 0.8, 0), "cheekR": (0.0, 0.02, 1.04, 0.8, 0)},
}

render({})[0].save(f"{OUT}/neutral.png")
jobs = [(f"{d}_{style}", m) for style, mv in RIGHT.items() for d, m in (("right", mv), ("left", mirror(mv)))]
jobs += [(f"down_{style}", mv) for style, mv in DOWN.items()]
for name, mv in jobs:
    img, G, nudge, ok = solve(mv)
    img.save(f"{OUT}/{name}.png")
    print(f"{name:16s} {'OK  ' if ok else 'FAIL'} shrink {G:.2f}  nudged: {', '.join(sorted(nudge)) or 'nothing'}")
