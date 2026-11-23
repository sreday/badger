package snore

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

// Company logos for the sponsor cards. The server takes each one from the
// company's own website (the logo in its header, else its icon), keeps a
// copy and shows it without a box behind it. A logo drawn in white would
// vanish on a light card: those are marked and get a dark plate.

var (
	linkTag    = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	anyAttr    = regexp.MustCompile(`(?is)\b([a-z-]+)\s*=\s*("([^"]*)"|'([^']*)')`)
	logoDomain = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9-]+)+$`)
	logoFile   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*\.(svg|png|webp|ico)$`)
)

const (
	logoMax   = 600 << 10
	logoRetry = 24 * time.Hour
	logoTries = 6
	// logoDir: the pictures picked by the current rules. An older way of
	// picking kept its files elsewhere, and those are no longer shown.
	logoDir = "logos3"
)

func allAttrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range anyAttr.FindAllStringSubmatch(tag, -1) {
		v := m[3]
		if v == "" {
			v = m[4]
		}
		out[strings.ToLower(m[1])] = html.UnescapeString(strings.TrimSpace(v))
	}
	return out
}

// logoCand is one place a logo may be: an address to fetch, or a drawing
// that was written into the page itself.
type logoCand struct {
	url    string
	inline []byte
}

var (
	linkedBit = regexp.MustCompile(`(?is)<a\b[^>]*>.*?</a>`)
	openTag   = regexp.MustCompile(`(?is)^<a\b[^>]*>`)
	svgBit    = regexp.MustCompile(`(?is)<svg\b.*?</svg>`)
	svgOpen   = regexp.MustCompile(`(?is)^<svg\b`)
)

// homeHref: the link leads to the site's front page, as a logo does.
func homeHref(href, domain string) bool {
	h := strings.ToLower(strings.TrimSpace(href))
	if i := strings.IndexAny(h, "?#"); i >= 0 {
		h = h[:i]
	}
	switch h {
	case "/", "./", "index.html", "/index.html":
		return true
	}
	h = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(h, "https:"), "http:"), "//")
	h = strings.TrimSuffix(strings.TrimPrefix(h, "www."), "/")
	return h == domain
}

// pickLogos lists where a company's logo may be, best first. The picture
// inside the link to the front page is the logo on nearly every site; then
// a picture that names the company itself; then the site's icons. A picture
// that is merely called a logo is not taken: sites show their customers'
// logos too.
func pickLogos(page, pageURL, domain string) []logoCand {
	label := domain
	if i := strings.IndexByte(label, '.'); i > 0 {
		label = label[:i]
	}
	var out []logoCand
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || strings.HasPrefix(ref, "data:") {
			return
		}
		if u := absURL(pageURL, ref); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, logoCand{url: u})
		}
	}
	imgSrc := func(a map[string]string) string {
		src := a["src"]
		if src == "" || strings.HasPrefix(src, "data:") {
			src = a["data-src"]
		}
		if src == "" {
			if f := strings.Fields(a["srcset"]); len(f) > 0 {
				src = f[0]
			}
		}
		return src
	}
	homes := 0
	for _, bit := range linkedBit.FindAllString(page, 400) {
		if homes >= 2 {
			break
		}
		if !homeHref(allAttrs(openTag.FindString(bit))["href"], domain) {
			continue
		}
		if t := imgTag.FindString(bit); t != "" {
			homes++
			add(imgSrc(allAttrs(t)))
			continue
		}
		if d := svgBit.FindString(bit); len(d) > 200 && !strings.Contains(d, "<use") {
			homes++
			if !strings.Contains(d[:strings.IndexByte(d, '>')], "xmlns") {
				d = svgOpen.ReplaceAllString(d, `<svg xmlns="http://www.w3.org/2000/svg"`)
			}
			out = append(out, logoCand{inline: []byte(d)})
		}
	}
	for _, t := range imgTag.FindAllString(page, -1) {
		a := allAttrs(t)
		src := imgSrc(a)
		base := src
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		words := strings.ToLower(a["alt"] + " " + a["class"] + " " + a["id"] + " " + base)
		if strings.Contains(words, "logo") && strings.Contains(strings.ToLower(a["alt"]+" "+base), label) {
			add(src)
			break
		}
	}
	var svg, touch, png []string
	for _, t := range linkTag.FindAllString(page, -1) {
		a := allAttrs(t)
		rel, href := strings.ToLower(a["rel"]), a["href"]
		switch {
		case !strings.Contains(rel, "icon") || strings.Contains(rel, "mask"):
		case strings.Contains(rel, "apple-touch"):
			touch = append(touch, href)
		case strings.Contains(a["type"], "svg") || strings.HasSuffix(strings.ToLower(href), ".svg"):
			svg = append(svg, href)
		default:
			png = append(png, href)
		}
	}
	for _, ref := range append(append(touch, svg...), png...) {
		add(ref)
	}
	if len(out) > logoTries {
		out = out[:logoTries]
	}
	return out
}

// logoKind: the file ending for a logo we can show ("" for anything else)
// and whether it is drawn in white or near white.
func logoKind(data []byte) (ext string, light bool) {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	if bytes.Contains(bytes.ToLower(head), []byte("<svg")) {
		return "svg", lightSVG(string(data))
	}
	switch http.DetectContentType(data) {
	case "image/png":
		return "png", lightPNG(data)
	case "image/webp":
		return "webp", lightPNG(data)
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico", false
	}
	return "", false
}

var svgPaint = regexp.MustCompile(`(?i)\b(?:fill|stroke|stop-color)\s*[:=]\s*["']?\s*(#[0-9a-f]{3,8}|[a-z]+|rgba?\([^)]*\))`)

// paleColour: a colour that would not show on a white card. ok is false
// for a word that is no colour at all ("none", "url").
func paleColour(c string) (pale, ok bool) {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "none", "url", "transparent", "inherit":
		return false, false
	case "white", "ivory", "snow", "whitesmoke", "ghostwhite":
		return true, true
	}
	var r, g, b int
	switch {
	case strings.HasPrefix(c, "#") && (len(c) == 4 || len(c) == 5):
		fmt.Sscanf(c[1:4], "%1x%1x%1x", &r, &g, &b)
		r, g, b = r*17, g*17, b*17
	case strings.HasPrefix(c, "#") && len(c) >= 7:
		fmt.Sscanf(c[1:7], "%2x%2x%2x", &r, &g, &b)
	case strings.HasPrefix(c, "rgb"):
		if n, _ := fmt.Sscanf(strings.NewReplacer("rgba(", "", "rgb(", "", ",", " ").Replace(c), "%d %d %d", &r, &g, &b); n < 3 {
			return false, false
		}
	default:
		return false, true // a named colour, or the text colour: dark
	}
	return 0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(b) > 0.84*255, true
}

// lightSVG: most of what the drawing is painted with is white or close.
func lightSVG(s string) bool {
	pale, dark := 0, 0
	for _, m := range svgPaint.FindAllStringSubmatch(s, -1) {
		if p, ok := paleColour(m[1]); !ok {
			continue
		} else if p {
			pale++
		} else {
			dark++
		}
	}
	return pale > 0 && pale >= 2*dark
}

// lightPNG: most of the picture's visible pixels are white or close to it.
func lightPNG(data []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	stepX, stepY := b.Dx()/64+1, b.Dy()/64+1
	var pale, n float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			n++
			if (0.2126*float64(r)+0.7152*float64(g)+0.0722*float64(bl))/float64(a) > 0.84 {
				pale++
			}
		}
	}
	return n > 0 && pale/n > 0.6
}

// cutWhite takes the white box away from a logo that was saved on one: the
// white that touches the edge becomes see-through, white inside the
// drawing stays. False when the picture has no white box around it.
func cutWhite(data []byte) ([]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := src.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 || b.Dx()*b.Dy() > 60_000_000 {
		return nil, false
	}
	// Some sites keep a logo many thousand pixels wide: a card needs a few
	// hundred, so every k-th pixel is enough.
	k := 1
	for b.Dx()/k > 900 || b.Dy()/k > 900 {
		k++
	}
	w, h := b.Dx()/k, b.Dy()/k
	if w < 8 || h < 8 {
		return nil, false
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// The mean of the block, so edges stay smooth.
			var r, g, bl, a uint64
			for dy := 0; dy < k; dy++ {
				for dx := 0; dx < k; dx++ {
					pr, pg, pb, pa := src.At(b.Min.X+x*k+dx, b.Min.Y+y*k+dy).RGBA()
					r, g, bl, a = r+uint64(pr), g+uint64(pg), bl+uint64(pb), a+uint64(pa)
				}
			}
			if a == 0 {
				continue
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = uint8(r*255/a), uint8(g*255/a), uint8(bl*255/a)
			img.Pix[i+3] = uint8(a / uint64(k*k) >> 8)
		}
	}
	// The box is the colour of the top left corner, when that is a light
	// grey or white (some pictures store white a little darker) and the
	// other corners agree.
	bg := img.Pix[0:4:4]
	near := func(a, b uint8, by int) bool { return int(a)-int(b) <= by && int(b)-int(a) <= by }
	if bg[3] < 0xf0 || bg[0] < 0xd8 || !near(bg[0], bg[1], 6) || !near(bg[0], bg[2], 6) {
		return nil, false
	}
	white := func(x, y int) bool {
		i := img.PixOffset(x, y)
		return img.Pix[i+3] > 0xf0 && near(img.Pix[i], bg[0], 12) && near(img.Pix[i+1], bg[1], 12) && near(img.Pix[i+2], bg[2], 12)
	}
	if !white(w-1, 0) || !white(0, h-1) || !white(w-1, h-1) {
		return nil, false
	}
	bg = append([]uint8(nil), bg...) // the corner itself is about to change
	seen := make([]bool, w*h)
	var stack []int
	push := func(x, y int) {
		if x < 0 || y < 0 || x >= w || y >= h || seen[y*w+x] || !white(x, y) {
			return
		}
		seen[y*w+x] = true
		stack = append(stack, y*w+x)
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := p%w, p/w
		img.Pix[img.PixOffset(x, y)+3] = 0
		push(x+1, y)
		push(x-1, y)
		push(x, y+1)
		push(x, y-1)
	}
	var out bytes.Buffer
	if png.Encode(&out, img) != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// trimEdges cuts the empty margin off a logo, so a small drawing on a big
// see-through sheet fills its place on the card. False when there is no
// margin worth cutting.
func trimEdges(data []byte) ([]byte, bool) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := src.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 || b.Dx()*b.Dy() > 16_000_000 {
		return nil, false
	}
	x0, y0, x1, y1 := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a > 0x1000 {
				if x < x0 {
					x0 = x
				}
				if x > x1 {
					x1 = x
				}
				if y < y0 {
					y0 = y
				}
				if y > y1 {
					y1 = y
				}
			}
		}
	}
	w, h := x1-x0+1, y1-y0+1
	if w < 4 || h < 4 || (w*100 > b.Dx()*92 && h*100 > b.Dy()*92) {
		return nil, false
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), src, image.Pt(x0, y0), draw.Src)
	var out bytes.Buffer
	if png.Encode(&out, img) != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// logoOf: the kept logo of a domain ("" when there is none) and whether it
// needs the dark plate.
func (s *Server) logoOf(domain string) (file string, dark bool) {
	if !logoDomain.MatchString(domain) {
		return "", false
	}
	for _, ext := range []string{"svg", "png", "webp", "ico"} {
		for _, mark := range []string{".", ".light."} {
			name := domain + mark + ext
			if _, err := os.Stat(filepath.Join(s.dir, logoDir, name)); err == nil {
				return name, mark == ".light."
			}
		}
	}
	return "", false
}

// wantLogo fetches a domain's logo once a day at most. Called without the
// lock, in its own goroutine: it reads the company's pages, politely.
func (s *Server) wantLogo(domain string) {
	if !logoDomain.MatchString(domain) {
		return
	}
	s.logoMu.Lock()
	if s.logoTried == nil {
		s.logoTried = map[string]time.Time{}
	}
	if at, ok := s.logoTried[domain]; ok && s.now().Sub(at) < logoRetry {
		s.logoMu.Unlock()
		return
	}
	s.logoTried[domain] = s.now()
	s.logoMu.Unlock()

	// A past sponsor's logo is on our own event pages: that one is right.
	var cands []logoCand
	if u := s.fp.sponsorLogos()[domain]; u != "" {
		cands = append(cands, logoCand{url: u})
	}
	home := "https://" + domain + "/"
	if page, err := s.get(home, nil, true); err == nil {
		cands = append(cands, pickLogos(string(page), home, domain)...)
	}
	cands = append(cands, logoCand{url: home + "favicon.ico"})
	for _, c := range cands {
		data := c.inline
		if data == nil {
			var err error
			if data, err = s.get(c.url, nil, true); err != nil {
				continue
			}
		}
		if len(data) < 80 || len(data) > logoMax {
			continue
		}
		ext, light := logoKind(data)
		if ext == "" {
			continue
		}
		if ext == "png" || ext == "webp" {
			if cut, ok := cutWhite(data); ok {
				data, ext, light = cut, "png", lightPNG(cut)
			}
			if cut, ok := trimEdges(data); ok {
				data, ext, light = cut, "png", lightPNG(cut)
			}
		}
		name := domain + "." + ext
		if light {
			name = domain + ".light." + ext
		}
		dir := filepath.Join(s.dir, logoDir)
		if os.MkdirAll(dir, 0o700) == nil && os.WriteFile(filepath.Join(dir, name), data, 0o600) == nil {
			return
		}
	}
}

// handleLogo: GET /logo/{file}. The files come from other people's sites,
// so they are served as pictures only: nothing in one may run.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	f := r.PathValue("file")
	if !logoFile.MatchString(f) || strings.Contains(f, "..") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(s.dir, logoDir, f))
}
