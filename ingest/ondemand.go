// Package ingest coordinates on-demand mail fetching from Graph or IMAP.
// Polling only runs when a client hits a message-related API endpoint.
package ingest

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Fetcher pulls new mail from the relay inbox into the local DB once.
type Fetcher interface {
	FetchOnce(context.Context) error
}

// OnDemand runs at most one in-flight fetch and optionally coalesces
// rapid client requests within MinInterval.
type OnDemand struct {
	Fetcher     Fetcher
	MinInterval time.Duration // 0 = always fetch; e.g. 1s coalesces bursts

	mu          sync.Mutex
	inflight    *call
	lastDone    time.Time
	lastErr     error
	fetchCtx    context.Context
	fetchCancel context.CancelFunc
	closed      bool
	wg          sync.WaitGroup
}

type call struct {
	done chan struct{}
	err  error
}

// Sync fetches new mail if needed. Concurrent callers share one in-flight fetch.
// Errors are returned to the caller but do not panic; handlers may still serve DB data.
func (o *OnDemand) Sync() error {
	return o.SyncContext(context.Background())
}

// SyncContext is like Sync, but lets a caller stop waiting when its context is
// cancelled. The shared fetch continues for other callers and updates the cache.
func (o *OnDemand) SyncContext(ctx context.Context) error {
	if o == nil || o.Fetcher == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return context.Canceled
	}
	if o.inflight != nil {
		c := o.inflight
		o.mu.Unlock()
		select {
		case <-c.done:
			return c.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Apply the interval to all completed attempts, including failures, so a
	// broken relay cannot be hammered by every request in a burst.
	if o.MinInterval > 0 && !o.lastDone.IsZero() && time.Since(o.lastDone) < o.MinInterval {
		err := o.lastErr
		o.mu.Unlock()
		return err
	}
	c := &call{done: make(chan struct{})}
	if o.fetchCtx == nil {
		o.fetchCtx, o.fetchCancel = context.WithCancel(context.Background())
	}
	o.inflight = c
	o.wg.Add(1)
	o.mu.Unlock()

	// The fetch belongs to all concurrent callers, not only the request that
	// started it. Run it independently so cancellation of that request does not
	// abandon the shared work.
	go o.fetch(c)

	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *OnDemand) fetch(c *call) {
	defer o.wg.Done()
	ctx, cancel := context.WithCancel(o.fetchCtx)
	defer cancel()

	var err error
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("fetch panic: %v", v)
		}
		if err != nil {
			log.Printf("on-demand fetch: %v", err)
		}

		o.mu.Lock()
		c.err = err
		o.lastDone = time.Now()
		o.lastErr = err
		o.inflight = nil
		close(c.done)
		o.mu.Unlock()
	}()

	err = o.Fetcher.FetchOnce(ctx)
}

// Close cancels an in-flight fetch and waits for it to release its resources.
// No new fetches are started after Close begins.
func (o *OnDemand) Close() {
	if o == nil {
		return
	}

	o.mu.Lock()
	if !o.closed {
		o.closed = true
		if o.fetchCancel != nil {
			o.fetchCancel()
		}
	}
	o.mu.Unlock()
	o.wg.Wait()
}
