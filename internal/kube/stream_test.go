// Copyright 2026 The pgConsole Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kube

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/watch"
)

// scriptedWatch is a watch.Interface whose events are supplied by the
// test. Stop closes the channel, as a real watch does when released.
type scriptedWatch struct {
	ch   chan watch.Event
	once sync.Once
}

func newScriptedWatch(buffer int) *scriptedWatch {
	return &scriptedWatch{ch: make(chan watch.Event, buffer)}
}

func (w *scriptedWatch) ResultChan() <-chan watch.Event { return w.ch }

func (w *scriptedWatch) Stop() { w.once.Do(func() { close(w.ch) }) }

// passThrough is the simplest pump: every event is one item.
func passThrough(watch.Event) (int, bool, bool) { return 1, true, false }

// TestFanInStopReleasesAPumpBlockedMidSend proves stopping a stream
// releases a pump that is blocked handing an item to a consumer which
// has stopped receiving.
//
// That is not a hypothetical: the collector loop returns on
// cancellation, and whatever the watch had already converted is then
// waiting on a channel nobody will ever read again. Before the send was
// guarded by the stream's context, the pump goroutine stayed parked
// there for the life of the process — one leak per watch, per re-seed.
//
// Deliberately not parallel, and deliberately never receiving from the
// merged channel: the leak only exists for a consumer that has stopped
// receiving, so a test that drains the channel unblocks the very pump it
// is meant to catch and passes against the bug. Goroutine count is
// therefore the assertion — waiting for the channel to close is not
// available, because detecting a close requires a receive.
func TestFanInStopReleasesAPumpBlockedMidSend(t *testing.T) {
	source := newScriptedWatch(1)
	source.ch <- watch.Event{Type: watch.Added}

	before := runtime.NumGoroutine()
	_, stop := fanIn(context.Background(),
		[]watch.Interface{source}, []pump[int]{passThrough})

	// The pump converts the queued event and parks offering it. The
	// sleep makes it certain it is parked there rather than racing the
	// stop below.
	time.Sleep(20 * time.Millisecond)
	stop()

	// Both the pump and the goroutine waiting on it must return. If the
	// send is unguarded, both stay parked for the life of the process.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("goroutines %d before, %d after stop: a pump is stranded on a send nobody will receive",
		before, runtime.NumGoroutine())
}

// TestFanInEndsTheMergedStreamWhenOneSourceEnds proves a partial stream
// is never served. The backup catalog merges two kinds, and a collector
// that kept publishing while one of them had silently stopped arriving
// would report a half-current generation as current.
func TestFanInEndsTheMergedStreamWhenOneSourceEnds(t *testing.T) {
	t.Parallel()
	first := newScriptedWatch(0)
	second := newScriptedWatch(0)

	items, stop := fanIn(context.Background(),
		[]watch.Interface{first, second},
		[]pump[int]{passThrough, passThrough})
	defer stop()

	// One source ends; the merged stream must end with it.
	first.Stop()

	select {
	case _, ok := <-items:
		if ok {
			t.Fatal("received an item after a source ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("one source ending did not end the merged stream")
	}
}

// TestFanInSkipsWithoutEndingTheStream proves the difference between an
// item worth skipping and one worth re-seeding over. A bookmark, or an
// object belonging to another cluster, must not tear down a watch that
// is otherwise healthy.
func TestFanInSkipsWithoutEndingTheStream(t *testing.T) {
	t.Parallel()
	source := newScriptedWatch(3)
	source.ch <- watch.Event{Type: watch.Bookmark}
	source.ch <- watch.Event{Type: watch.Added}
	source.ch <- watch.Event{Type: watch.Bookmark}

	skipBookmarks := func(e watch.Event) (int, bool, bool) {
		if e.Type == watch.Bookmark {
			return 0, false, false
		}
		return 7, true, false
	}
	items, stop := fanIn(context.Background(),
		[]watch.Interface{source}, []pump[int]{skipBookmarks})
	defer stop()

	select {
	case item, ok := <-items:
		if !ok {
			t.Fatal("the stream ended on a skipped item")
		}
		if item != 7 {
			t.Errorf("item = %d, want the one item the pump kept", item)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no item arrived past the skipped ones")
	}
}
