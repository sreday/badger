package main

import (
	"log"
	"sync"
	"time"

	"github.com/sreday/badger/luma"
)

// GuestCache keeps one GuestIndex per event and refreshes it in the
// background, so a scan never waits on Luma once the first fetch is done.
type GuestCache struct {
	apiKey string
	maxAge time.Duration

	mu      sync.Mutex
	entries map[string]*guestEntry
}

type guestEntry struct {
	ix         *GuestIndex
	fetchedAt  time.Time
	triedAt    time.Time
	refreshing bool
}

func NewGuestCache(apiKey string, maxAge time.Duration) *GuestCache {
	return &GuestCache{apiKey: apiKey, maxAge: maxAge, entries: make(map[string]*guestEntry)}
}

// Index returns the cached index for an event. The first call blocks on Luma;
// later calls return immediately and refresh in the background when stale.
func (c *GuestCache) Index(eventID string) *GuestIndex {
	c.mu.Lock()
	e := c.entries[eventID]
	if e != nil && e.ix != nil {
		if time.Since(e.fetchedAt) > c.maxAge && !e.refreshing {
			e.refreshing = true
			go c.refresh(eventID)
		}
		ix := e.ix
		c.mu.Unlock()
		return ix
	}
	if e != nil && time.Since(e.triedAt) < time.Minute {
		// Luma failed moments ago: don't make every scan wait on it again.
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	return c.refresh(eventID)
}

// Fresh blocks until the index is no older than maxAge (used for exports).
func (c *GuestCache) Fresh(eventID string, maxAge time.Duration) *GuestIndex {
	c.mu.Lock()
	e := c.entries[eventID]
	c.mu.Unlock()
	if e != nil && e.ix != nil && time.Since(e.fetchedAt) <= maxAge {
		return e.ix
	}
	return c.refresh(eventID)
}

// refresh fetches the guest list. On failure the previous index is kept.
func (c *GuestCache) refresh(eventID string) *GuestIndex {
	guests, err := luma.GetGuests(eventID, c.apiKey)

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[eventID]
	if e == nil {
		e = &guestEntry{}
		c.entries[eventID] = e
	}
	e.refreshing = false
	e.triedAt = time.Now()
	if err != nil {
		log.Printf("Guest refresh for %s failed: %v", eventID, err)
		return e.ix
	}
	e.ix = NewGuestIndex(guests)
	e.fetchedAt = time.Now()
	log.Printf("Guest list for %s: %d approved guests", eventID, len(guests))
	return e.ix
}
