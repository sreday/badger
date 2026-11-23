package snore

import (
	"bytes"
	"html"
	"image"
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
	hexColour  = regexp.MustCompile(`#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`)
)

const (
	logoMax   = 600 << 10
	logoRetry = 24 * time.Hour
	logoTries = 4
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

// pickLogos lists where a company's logo may be, best first: an image the
// page itself calls a logo (one that carries the company's name before any
// other, so a wall of customers' logos does not win), then the icons.
func pickLogos(page, pageURL, domain string) []string {
	label := domain
	if i := strings.IndexByte(label, '.'); i > 0 {
		label = label[:i]
	}
	var own, named, icons []string
	seen := map[string]bool{}
	add := func(list *[]string, ref string) {
		if ref == "" || strings.HasPrefix(ref, "data:") {
			return
		}
		if u := absURL(pageURL, ref); u != "" && !seen[u] {
			seen[u] = true
			*list = append(*list, u)
		}
	}
	for _, t := range imgTag.FindAllString(page, -1) {
		a := allAttrs(t)
		src := a["src"]
		if src == "" || strings.HasPrefix(src, "data:") {
			src = a["data-src"]
		}
		words := strings.ToLower(a["alt"] + " " + a["class"] + " " + a["id"] + " " + src)
		if !strings.Contains(words, "logo") {
			continue
		}
		if strings.Contains(strings.ToLower(a["alt"]+" "+src), label) {
			add(&own, src)
		} else if len(named) < 2 {
			add(&named, src)
		}
	}
	var svg, touch, png []string
	for _, t := range linkTag.FindAllString(page, -1) {
		a := allAttrs(t)
		rel, href := strings.ToLower(a["rel"]), a["href"]
		switch {
		case !strings.Contains(rel, "icon"):
		case strings.Contains(a["type"], "svg") || strings.HasSuffix(strings.ToLower(href), ".svg"):
			add(&svg, href)
		case strings.Contains(rel, "apple-touch"):
			add(&touch, href)
		default:
			add(&png, href)
		}
	}
	icons = append(append(svg, touch...), png...)
	out := append(append(own, named...), icons...)
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
		return "webp", false
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico", false
	}
	return "", false
}

func lightSVG(s string) bool {
	cols := hexColour.FindAllStringSubmatch(s, -1)
	if len(cols) == 0 {
		return strings.Contains(s, `fill="white"`) && !strings.Contains(s, `fill="black"`)
	}
	for _, c := range cols {
		if h := strings.ToLower(c[1]); h != "fff" && h != "ffffff" && h != "fefefe" && h != "fafafa" {
			return false
		}
	}
	return true
}

// lightPNG: the picture's visible pixels are white or close to it.
func lightPNG(data []byte) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	stepX, stepY := b.Dx()/48+1, b.Dy()/48+1
	var sum, n float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			sum += (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / float64(a)
			n++
		}
	}
	return n > 0 && sum/n > 0.86
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

// logoOf: the kept logo of a domain ("" when there is none) and whether it
// needs the dark plate.
func (s *Server) logoOf(domain string) (file string, dark bool) {
	if !logoDomain.MatchString(domain) {
		return "", false
	}
	for _, ext := range []string{"svg", "png", "webp", "ico"} {
		for _, mark := range []string{".", ".light."} {
			name := domain + mark + ext
			if _, err := os.Stat(filepath.Join(s.dir, "logos", name)); err == nil {
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

	home := "https://" + domain + "/"
	page, err := s.get(home, nil, true)
	if err != nil {
		return
	}
	cands := pickLogos(string(page), home, domain)
	cands = append(cands, home+"favicon.ico")
	for _, u := range cands {
		data, err := s.get(u, nil, true)
		if err != nil || len(data) < 80 || len(data) > logoMax {
			continue
		}
		ext, light := logoKind(data)
		if ext == "" {
			continue
		}
		if ext == "png" || ext == "webp" {
			if cut, ok := cutWhite(data); ok {
				data, ext, light = cut, "png", false
			}
		}
		name := domain + "." + ext
		if light {
			name = domain + ".light." + ext
		}
		dir := filepath.Join(s.dir, "logos")
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
	http.ServeFile(w, r, filepath.Join(s.dir, "logos", f))
}
