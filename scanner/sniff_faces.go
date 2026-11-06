package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// Speaker pictures for Sniff, naked: the sites keep every headshot as a grey
// cut-out on the brand's colour disc (speakers/<name>.png, on the repo's
// home/assets/images/headshot-circle.png). Sniff takes the disc back out, fades the bottom (where the circle cut the shoulders), and
// serves a small JPEG on the page's own background, so 40 faces stay light
// on venue wifi. One picture is made at a time (the VM has 1 GB), from a
// copy shrunk first, and kept on disk, so a restart never redoes them.

const faceBox = 320 // the longest side, in pixels

var faceWork = make(chan struct{}, 1) // one picture at a time

var (
	faceMu    sync.Mutex
	faceCache = map[string]faceEntry{}   // repo|photo|bg -> JPEG
	faceRepo  = map[string]string{}      // event id -> its site repo
	faceDiscs = map[string]image.Image{} // repo -> its headshot disc
)

type faceEntry struct {
	jpg []byte
	at  time.Time
}

// faceBackgrounds are the pages' backgrounds the faces sit on.
var faceBackgrounds = map[string]color.NRGBA{
	"sreday":      {0xf4, 0xf1, 0xf5, 0xff},
	"llmday":      {0xee, 0xf6, 0xf2, 0xff},
	"platformday": {0xfb, 0xf4, 0xe8, 0xff},
	"white":       {0xff, 0xff, 0xff, 0xff},
}

// getImage fetches and decodes an image (headshots run to a few MB).
func getImage(u string) (image.Image, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	img, _, err := image.Decode(io.LimitReader(resp.Body, 16<<20))
	return img, err
}

// sniffFace is the session's speaker picture on bg, cached for a day.
func (s *Server) sniffFace(ss SniffSession, bg string) ([]byte, error) {
	faceMu.Lock()
	repo := faceRepo[ss.EventID]
	faceMu.Unlock()
	if repo == "" {
		dir, err := findEventDir(siteRepos(), ss.EventID, ss.EventName)
		if err != nil {
			return nil, err
		}
		repo = dir.Repo
		faceMu.Lock()
		faceRepo[ss.EventID] = repo
		faceMu.Unlock()
	}
	photo := strings.TrimSpace(ss.Who) + ".png"
	key := repo + "|" + photo + "|" + bg
	sum := sha256.Sum256([]byte(key))
	file := filepath.Join(s.cfg.DataDir, "faces", hex.EncodeToString(sum[:12])+".jpg")
	faceMu.Lock()
	if e, ok := faceCache[key]; ok && time.Since(e.at) < 24*time.Hour {
		faceMu.Unlock()
		return e.jpg, nil
	}
	faceMu.Unlock()
	faceWork <- struct{}{}
	defer func() { <-faceWork }()
	if st, err := os.Stat(file); err == nil && time.Since(st.ModTime()) < 24*time.Hour {
		if jpg, err := os.ReadFile(file); err == nil {
			faceMu.Lock()
			faceCache[key] = faceEntry{jpg, st.ModTime()}
			faceMu.Unlock()
			return jpg, nil
		}
	}
	faceMu.Lock()
	disc := faceDiscs[repo]
	faceMu.Unlock()
	raw := githubRawURL + "/" + repo + "/HEAD/"
	if disc == nil {
		d, err := getImage(raw + "home/assets/images/headshot-circle.png")
		if err != nil {
			return nil, fmt.Errorf("disc: %w", err)
		}
		disc = shrink(d, 2*faceBox)
		faceMu.Lock()
		faceDiscs[repo] = d
		faceMu.Unlock()
	}
	shot, err := getImage(raw + "speakers/" + url.PathEscape(photo))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", photo, err)
	}
	shot = shrink(shot, 2*faceBox)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, nakedFace(shot, disc, faceBackgrounds[bg]), &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	faceMu.Lock()
	faceCache[key] = faceEntry{buf.Bytes(), time.Now()}
	faceMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err == nil {
		os.WriteFile(file, buf.Bytes(), 0o600)
	}
	return buf.Bytes(), nil
}

// shrink scales img down so its longest side is at most n.
func shrink(img image.Image, n int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= n && h <= n {
		return img
	}
	if w >= h {
		w, h = n, h*n/w
	} else {
		w, h = w*n/h, n
	}
	out := image.NewNRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	return out
}

// nakedFace unmixes a grey person from the colour disc behind them: how much
// of the disc's colour is left in a pixel says how see-through the person is
// there. It crops to the person, fades the bottom fifth out and lays the
// result on bg, at most faceBox on the longest side.
func nakedFace(shot, disc image.Image, bg color.NRGBA) image.Image {
	b := shot.Bounds()
	w, h := b.Dx(), b.Dy()
	px := make([]color.NRGBA, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px[y*w+x] = color.NRGBAModel.Convert(shot.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
		}
	}
	// Older exports sit the disc smaller on the canvas: scale the disc
	// (smoothly: it's grainy) to the one in the picture, found by its width
	// and bottom edge (a head can stick out of the top, never the sides).
	sb, db := opaqueBounds(shot), opaqueBounds(disc)
	side := sb.Dx()
	if side < 2 || db.Dx() < 2 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	ox, oy := sb.Min.X-b.Min.X, sb.Max.Y-b.Min.Y-side
	fit := image.NewNRGBA(image.Rect(0, 0, side, side))
	draw.CatmullRom.Scale(fit, fit.Bounds(), disc, db, draw.Src, nil)
	discAt := func(x, y int) (color.NRGBA, bool) {
		x, y = x-ox, y-oy
		if x < 0 || y < 0 || x >= side || y >= side {
			return color.NRGBA{}, false
		}
		return fit.NRGBAAt(x, y), true
	}
	alpha := make([]float64, w*h)
	grey := make([]float64, w*h)
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := px[y*w+x]
			if c.A == 0 {
				continue
			}
			d, ok := discAt(x, y)
			if !ok { // above the disc: all person
				d = color.NRGBA{128, 128, 128, 255}
			} else if d.A < 250 && c.A < 250 {
				continue // the disc's soft rim: no person there
			}
			dr, dg, dbl := float64(d.R)/255, float64(d.G)/255, float64(d.B)/255
			cr, cg, cb := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
			cm, dm := (cr+cg+cb)/3, (dr+dg+dbl)/3
			// C = a*grey + (1-a)*D, so C's chroma is (1-a) times D's.
			dn := (dr-dm)*(dr-dm) + (dg-dm)*(dg-dm) + (dbl-dm)*(dbl-dm)
			k := 0.0
			if dn > 1e-4 {
				k = ((cr-cm)*(dr-dm) + (cg-cm)*(dg-dm) + (cb-cm)*(dbl-dm)) / dn
			}
			a := clamp01((clamp01(1-k) - 0.08) / 0.84) // snap near-pure disc and person
			g := 0.0
			if a > 0.02 {
				g = clamp01((cm - (1-a)*dm) / a)
			}
			a *= float64(c.A) / 255
			i := y*w + x
			alpha[i], grey[i] = a, g
			if a > 0.03 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	cw, ch := maxX-minX+1, maxY-minY+1
	fade := float64(ch) / 5
	out := image.NewNRGBA(image.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		f := 1.0
		if left := float64(ch - 1 - y); left < fade {
			t := left / fade
			f = t * t * (3 - 2*t)
		}
		for x := 0; x < cw; x++ {
			i := (minY+y)*w + minX + x
			a := alpha[i] * f
			v := grey[i] * 255
			mix := func(bgc uint8) uint8 { return uint8(v*a + float64(bgc)*(1-a) + 0.5) }
			out.SetNRGBA(x, y, color.NRGBA{mix(bg.R), mix(bg.G), mix(bg.B), 255})
		}
	}
	tw, th := cw, ch
	if tw > faceBox || th > faceBox {
		if tw >= th {
			tw, th = faceBox, ch*faceBox/cw
		} else {
			tw, th = cw*faceBox/ch, faceBox
		}
	}
	small := image.NewNRGBA(image.Rect(0, 0, max(tw, 1), max(th, 1)))
	draw.CatmullRom.Scale(small, small.Bounds(), out, out.Bounds(), draw.Src, nil)
	return small
}

// opaqueBounds is the box around an image's solid pixels.
func opaqueBounds(img image.Image) image.Rectangle {
	b := img.Bounds()
	r := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0x8000 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// handleSniffFace serves a session's speaker picture.
func (s *Server) handleSniffFace(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok || strings.TrimSpace(ss.Who) == "" {
		http.NotFound(w, r)
		return
	}
	bg := brandOf(ss.EventName)
	if r.URL.Query().Get("bg") == "white" {
		bg = "white"
	}
	jpg, err := s.sniffFace(ss, bg)
	if err != nil {
		http.Error(w, "no picture", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(jpg)
}
