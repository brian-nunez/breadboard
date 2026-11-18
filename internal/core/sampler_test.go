package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brian-nunez/breadboard/internal/runtime"
)

// samplerHarness is an experiment with one running service, "a", and a
// fake stats stream the test feeds.
func samplerHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.init()
	if _, err := h.eng.AddService(h.ctx(), AddServiceRequest{Dir: h.dir, Type: "redis"}); err != nil {
		t.Fatal(err)
	}
	h.rt.containers["lab"] = []runtime.Container{{ID: "a", Service: "redis", State: "running", Health: "healthy"}}
	h.rt.stream = make(chan []runtime.Sample)
	h.rt.streamErr = make(chan error)
	return h
}

func redisRow(t *testing.T, h *harness) ServiceStatus {
	t.Helper()
	st, err := h.eng.Status(h.ctx(), StatusRequest{Dir: h.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Services {
		if s.Name == "redis" {
			return s
		}
	}
	t.Fatalf("no redis row in %+v", st.Services)
	return ServiceStatus{}
}

// eventually polls cond for up to two seconds.
func eventuallyTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// With a sampler running, Status reads its latest sample (unknown, zero,
// before the first) and never takes its own; without one it samples.
func TestStatusReadsTheSampler(t *testing.T) {
	h := samplerHarness(t)
	if r := redisRow(t, h); r.CPUPercent != 1.5 || h.rt.statsCalls != 1 {
		t.Fatalf("without a sampler: cpu %v, Stats calls %d; want a fresh sample", r.CPUPercent, h.rt.statsCalls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.eng.StartStatsSampler(ctx)
	h.eng.StartStatsSampler(ctx)() // a second start does nothing, nor does its stop
	if r := redisRow(t, h); r.CPUPercent != 0 || r.MemoryBytes != 0 {
		t.Errorf("before the first sample: cpu %v mem %d, want zero (unknown)", r.CPUPercent, r.MemoryBytes)
	}
	h.rt.stream <- []runtime.Sample{{ID: "a", Usage: runtime.Usage{CPUPercent: 42, MemoryBytes: 7 << 20}}, {ID: "other-lab", Usage: runtime.Usage{CPUPercent: 99}}}
	eventuallyTrue(t, "the sample", func() bool { return redisRow(t, h).CPUPercent == 42 })
	if r := redisRow(t, h); r.MemoryBytes != 7<<20 {
		t.Errorf("memory = %d, want the sampled 7 MiB", r.MemoryBytes)
	}
	if h.rt.statsCalls != 1 {
		t.Errorf("Stats called %d times with a sampler running, want no new calls", h.rt.statsCalls)
	}
	h.rt.mu.Lock()
	streams := h.rt.streams
	h.rt.mu.Unlock()
	if streams != 1 {
		t.Errorf("%d stats streams, want one however often the sampler is started", streams)
	}

	// Once ctx ends the sampler stops and Status samples again.
	cancel()
	eventuallyTrue(t, "the sampler to stop", func() bool { return h.eng.sampler.Load() == nil })
	if r := redisRow(t, h); r.CPUPercent != 1.5 || h.rt.statsCalls != 2 {
		t.Errorf("after the sampler stopped: cpu %v, Stats calls %d; want a fresh sample", r.CPUPercent, h.rt.statsCalls)
	}
}

// stop returns only after the stream has ended, so bb serve cannot exit
// while docker stats still runs; a second stop is harmless.
func TestSamplerStopWaitsForTheStream(t *testing.T) {
	h := samplerHarness(t)
	h.rt.streamExit = 100 * time.Millisecond
	stop := h.eng.StartStatsSampler(context.Background())
	h.rt.stream <- []runtime.Sample{{ID: "a", Usage: runtime.Usage{CPUPercent: 3}}}
	stop()
	h.rt.mu.Lock()
	ended := h.rt.streamsEnded
	h.rt.mu.Unlock()
	if ended != 1 || h.eng.sampler.Load() != nil {
		t.Errorf("after stop: %d streams ended, sampler running %v; want the stream ended and no sampler", ended, h.eng.sampler.Load() != nil)
	}
	stop()
	// A new sampler can start once the old one stopped.
	h.eng.StartStatsSampler(context.Background())()
}

// A stream that ends (the daemon restarted) is started again, and the last
// sample stays readable meanwhile.
func TestSamplerRestartsItsStream(t *testing.T) {
	h := samplerHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.eng.StartStatsSampler(ctx)
	h.rt.stream <- []runtime.Sample{{ID: "a", Usage: runtime.Usage{CPUPercent: 10}}}
	h.rt.streamErr <- errors.New("docker stats: daemon went away")
	if r := redisRow(t, h); r.CPUPercent != 10 {
		t.Errorf("after the stream ended: cpu %v, want the last sample, 10", r.CPUPercent)
	}
	// The retry waits samplerRetryFirst (a second), then streams again.
	select {
	case h.rt.stream <- []runtime.Sample{{ID: "a", Usage: runtime.Usage{CPUPercent: 20}}}:
	case <-time.After(3 * samplerRetryFirst):
		t.Fatal("the sampler did not start a new stream")
	}
	eventuallyTrue(t, "the new sample", func() bool { return redisRow(t, h).CPUPercent == 20 })
}
