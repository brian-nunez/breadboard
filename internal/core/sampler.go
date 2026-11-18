package core

import (
	"context"
	"sync"
	"time"

	"github.com/brian-nunez/breadboard/internal/runtime"
)

// Restart delays for a stats stream that ended while the sampler should
// still run (the Docker daemon restarted, say): doubling from the first,
// capped at the second.
const (
	samplerRetryFirst = time.Second
	samplerRetryMax   = 30 * time.Second
)

// usageCache is the latest usage of each container a stats stream reported.
type usageCache struct {
	mu    sync.RWMutex
	usage map[string]runtime.Usage
}

func (c *usageCache) set(id string, u runtime.Usage) {
	c.mu.Lock()
	c.usage[id] = u
	c.mu.Unlock()
}

// get returns the cached usage of ids; an id with no sample yet is left out,
// which Status reports as zero (unknown).
func (c *usageCache) get(ids []string) map[string]runtime.Usage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]runtime.Usage, len(ids))
	for _, id := range ids {
		if u, ok := c.usage[id]; ok {
			out[id] = u
		}
	}
	return out
}

// StartStatsSampler keeps the latest CPU and memory use of every running
// container, from one streaming docker stats, until ctx ends. While it runs,
// Status reads that sample instead of taking its own, which costs about
// two seconds (docker stats measures CPU across an interval). A long-lived
// front end that calls Status often (bb serve, every page and every live
// view) starts it once; one-shot callers (bb status, MCP) do not, and keep
// a fresh sample.
//
// stop ends the sampler and returns only once its docker stats process
// has been killed and reaped, so a caller that defers it never exits
// leaving the process behind; it is safe to call more than once. Calling
// StartStatsSampler while a sampler runs starts nothing and returns a stop
// that does nothing.
func (e *Engine) StartStatsSampler(ctx context.Context) (stop func()) {
	c := &usageCache{usage: map[string]runtime.Usage{}}
	if !e.sampler.CompareAndSwap(nil, c) {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer e.sampler.CompareAndSwap(c, nil)
		delay := samplerRetryFirst
		for ctx.Err() == nil {
			got := false
			for s, err := range e.rt.StatsStream(ctx) {
				if err != nil {
					break
				}
				got = true
				c.set(s.ID, s.Usage)
			}
			if got {
				delay = samplerRetryFirst
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(2*delay, samplerRetryMax)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// usage is the CPU and memory use of the running containers ids: the
// sampler's latest when one runs, otherwise a fresh sample.
func (e *Engine) usage(ctx context.Context, ids []string) (map[string]runtime.Usage, error) {
	if c := e.sampler.Load(); c != nil {
		return c.get(ids), nil
	}
	return e.rt.Stats(ctx, ids)
}
