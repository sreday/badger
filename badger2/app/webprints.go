package app

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// webPrints holds hosted badges until the browser fetches them to print:
// a few minutes at most, and never more than a few thousand.
type webPrints struct {
	mu   sync.Mutex
	pngs map[string]webPrint
}

type webPrint struct {
	png []byte
	at  time.Time
}

const (
	webPrintTTL = 10 * time.Minute
	webPrintMax = 3000 // a whole event's "Print all"
)

func (w *webPrints) add(png []byte) string {
	b := make([]byte, 12)
	rand.Read(b)
	id := hex.EncodeToString(b)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pngs == nil {
		w.pngs = map[string]webPrint{}
	}
	now := time.Now()
	for k, p := range w.pngs {
		if now.Sub(p.at) > webPrintTTL || len(w.pngs) >= webPrintMax {
			delete(w.pngs, k)
		}
	}
	w.pngs[id] = webPrint{png, now}
	return id
}

func (w *webPrints) get(id string) ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.pngs[id]
	if !ok || time.Since(p.at) > webPrintTTL {
		return nil, false
	}
	return p.png, true
}
