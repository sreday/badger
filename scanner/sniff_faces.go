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
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// Speaker pictures for Sniff, never round: the sites keep every headshot as
// a grey cut-out on the brand's colour disc (speakers/<name>.png, on the
// repo's home/assets/images/headshot-circle.png). Sniff squares it up, its
// background kept, and serves a small JPEG, so 40 faces stay light on venue
// wifi. One picture is made at a time (the VM has 1 GB), from a
// copy shrunk first, and kept on disk, so a restart never redoes them.

const faceBox = 320 // the longest side, in pixels

var faceWork = make(chan struct{}, 1) // one picture at a time

var (
	faceMu    sync.Mutex
	faceCache = map[string]faceEntry{}   // repo|photo|look -> JPEG
	faceRepo  = map[string]string{}      // event id -> its site repo
	faceDiscs = map[string]image.Image{} // repo -> its headshot disc
)

type faceEntry struct {
	jpg []byte
	at  time.Time
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

// sniffFace is the session's speaker picture, cached for a day.
func (s *Server) sniffFace(ss SniffSession) ([]byte, error) {
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
	key := repo + "|" + photo + "|square"
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
		faceDiscs[repo] = disc
		faceMu.Unlock()
	}
	shot, err := getImage(raw + "speakers/" + url.PathEscape(photo))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", photo, err)
	}
	shot = shrink(shot, 2*faceBox)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, squareFace(shot, disc), &jpeg.Options{Quality: 82}); err != nil {
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

// squareFace keeps the headshot as it is, background and all, but square:
// the disc's colour runs on past its edge (from a blurred copy, so the grain
// doesn't streak) and the person sits on it whole. The square is a little
// narrower than the disc and starts at the top of the head, so the curve
// the circle cut into the shoulders falls outside it. At most faceBox.
func squareFace(shot, disc image.Image) image.Image {
	sb, db := opaqueBounds(shot), opaqueBounds(disc)
	side := sb.Dx()
	if side < 8 || db.Dx() < 2 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	// Older exports sit the disc smaller on the canvas: scale the disc
	// (smoothly: it's grainy) to the one in the picture, found by its width
	// and bottom edge (a head can stick out of the top, never the sides).
	ox, oy := sb.Min.X, sb.Max.Y-side
	fit := image.NewNRGBA(image.Rect(0, 0, side, side))
	draw.CatmullRom.Scale(fit, fit.Bounds(), disc, db, draw.Src, nil)
	soft := image.NewNRGBA(image.Rect(0, 0, max(side/24, 2), max(side/24, 2)))
	draw.ApproxBiLinear.Scale(soft, soft.Bounds(), fit, fit.Bounds(), draw.Src, nil)
	r := float64(side) / 2
	edge := r * 0.92 // inside the soft rim
	n := side * 22 / 25
	x0, y0 := ox+(side-n)/2, min(sb.Min.Y, oy)
	out := image.NewNRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x0+x-ox)+0.5-r, float64(y0+y-oy)+0.5-r
			var d color.NRGBA
			if dist := math.Hypot(fx, fy); dist <= edge {
				d = fit.NRGBAAt(int(fx+r), int(fy+r))
			} else {
				k := edge / dist
				sx := (fx*k + r) * float64(soft.Bounds().Dx()) / float64(side)
				sy := (fy*k + r) * float64(soft.Bounds().Dy()) / float64(side)
				d = soft.NRGBAAt(int(sx), int(sy))
			}
			c := color.NRGBAModel.Convert(shot.At(x0+x, y0+y)).(color.NRGBA)
			a := float64(c.A) / 255
			mix := func(p, q uint8) uint8 { return uint8(float64(p)*a + float64(q)*(1-a) + 0.5) }
			out.SetNRGBA(x, y, color.NRGBA{mix(c.R, d.R), mix(c.G, d.G), mix(c.B, d.B), 255})
		}
	}
	if n <= faceBox {
		return out
	}
	small := image.NewNRGBA(image.Rect(0, 0, faceBox, faceBox))
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

// handleSniffFace serves a session's speaker picture.
func (s *Server) handleSniffFace(w http.ResponseWriter, r *http.Request) {
	ss, ok := s.sniff.Session(r.PathValue("code"))
	if !ok || strings.TrimSpace(ss.Who) == "" {
		http.NotFound(w, r)
		return
	}
	jpg, err := s.sniffFace(ss)
	if err != nil {
		http.Error(w, "no picture", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(jpg)
}
