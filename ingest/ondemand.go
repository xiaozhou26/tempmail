// Package ingest coordinates on-demand mail fetching from Graph or IMAP.
// Polling only runs when a client hits a message-related API endpoint.
package ingest

import (
	"log"
	"sync"
	"time"
)

// Fetcher pulls new mail from the relay inbox into the local DB once.
type Fetcher interface {
	FetchOnce() error
}

// OnDemand runs at most one in-flight fetch and optionally coalesces
// rapid client requests within MinInterval.
type OnDemand struct {
	Fetcher     Fetcher
	MinInterval time.Duration // 0 = always fetch; e.g. 1s coalesces bursts

	mu       sync.Mutex
	inflight *call
	lastDone time.Time
}

type call struct {
	done chan struct{}
	err  error
}

// Sync fetches new mail if needed. Concurrent callers share one in-flight fetch.
// Errors are returned to the caller but do not panic; handlers may still serve DB data.
func (o *OnDemand) Sync() error {
	if o == nil || o.Fetcher == nil {
		return nil
	}

	o.mu.Lock()
	// Coalesce: if we just synced successfully within MinInterval, skip.
	if o.MinInterval > 0 && !o.lastDone.IsZero() && time.Since(o.lastDone) < o.MinInterval {
		o.mu.Unlock()
		return nil
	}
	if o.inflight != nil {
		c := o.inflight
		o.mu.Unlock()
		<-c.done
		return c.err
	}
	c := &call{done: make(chan struct{})}
	o.inflight = c
	o.mu.Unlock()

	err := o.Fetcher.FetchOnce()
	if err != nil {
		log.Printf("on-demand fetch: %v", err)
	}

	o.mu.Lock()
	c.err = err
	if err == nil {
		o.lastDone = time.Now()
	}
	o.inflight = nil
	close(c.done)
	o.mu.Unlock()
	return err
}
