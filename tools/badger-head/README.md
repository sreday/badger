# The turning badger head

`render.py` makes the views in `snout/assets/head/` that `head.js` shows
when you hover a badger head (every Badger header and the hub). It needs
Python 3 with numpy and Pillow.

The large views (hub) come from the high-res head on the hub logo: the
frame's box cut from `badger-logo-body.webp` with `badger-frame.webp` on
top (left 1, top 1, 453 x 442 px). The small views (headers) come from
`badger-head.png`.

```sh
# from the repo root; head-hires.png = the hub head described above
DENS=3 python3 tools/badger-head/render.py head-hires.png snout/assets/head 0.8654 large
DENS=3 python3 tools/badger-head/render.py snout/assets/badger-head.png snout/assets/head 0.6 small
rm snout/assets/head/depth.png snout/assets/head/frame.png   # previews only
```

Each run writes `<name>.webp` (11 x 11 views) and `<name>.json` (the
angles, and per view where each eye is, how big, and whether it's seen).
Facing forward must stay the logo pixel for pixel: check the middle view
after any change to the shape.
