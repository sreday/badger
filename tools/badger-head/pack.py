"""Pack the in-between frames for the live head (head.js).

small: one 21 x 21 sprite sheet for the header heads (2x their 56 px).
large: one file per frame for the hub (a single sheet that size would
       cost hundreds of MB to decode); loaded in the background.
Both with <name>.json: grid size, the resting frame, and per frame where
the eyes are (fractions of the head box) and whether each is seen.

Usage: pack.py HIRES_DIR OUT_DIR   (HIRES_DIR from: HIRES=1 tween.py ...)
"""
import json
import os
import sys

from PIL import Image

SRC, OUT = sys.argv[1], sys.argv[2]
meta_src = json.load(open(os.path.join(SRC, "tween.json")))
N = meta_src["n"]
eyes = [meta_src["eyes"][f"{i}_{j}"] for i in range(N) for j in range(N)]
first = Image.open(os.path.join(SRC, "h_00_00.png"))
W0, H0 = first.size


def meta(fw, fh, **extra):
    m = {"fw": fw, "fh": fh, "cols": N, "rows": N, "rest": [N - 1, N // 2], "from": 0.1, "to": 0.9, "eyes": eyes}
    m.update(extra)
    return m


# small: a sprite sheet
fw, fh = 118, round(118 * H0 / W0)
sheet = Image.new("RGBA", (fw * N, fh * N))
for i in range(N):
    for j in range(N):
        sheet.paste(Image.open(os.path.join(SRC, f"h_{i:02d}_{j:02d}.png")).resize((fw, fh), Image.LANCZOS), (j * fw, i * fh))
sheet.save(os.path.join(OUT, "small.webp"), "WEBP", quality=90, method=6)
json.dump(meta(fw, fh, sheet="small.webp"), open(os.path.join(OUT, "small.json"), "w"))

# large: one file per frame
os.makedirs(os.path.join(OUT, "large"), exist_ok=True)
fw, fh = 400, round(400 * H0 / W0)
for i in range(N):
    for j in range(N):
        Image.open(os.path.join(SRC, f"h_{i:02d}_{j:02d}.png")).resize((fw, fh), Image.LANCZOS).save(
            os.path.join(OUT, "large", f"f_{i:02d}_{j:02d}.webp"), "WEBP", quality=88, method=6)
json.dump(meta(fw, fh, frames="large/f_{i}_{j}.webp"), open(os.path.join(OUT, "large.json"), "w"))
print("packed", N, "x", N)
