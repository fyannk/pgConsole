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
	"sync"

	"k8s.io/apimachinery/pkg/watch"
)

// pump converts one raw watch event into zero or one stream items.
//
// ok false skips the event without ending the stream: a bookmark, or an
// object that is not a member of the target cluster. fatal true ends the
// whole stream, because the only honest reading of an undecodable or
// errored watch is to stop and let the collector re-seed rather than
// publish a generation with a hole in it.
type pump[E any] func(event watch.Event) (item E, ok bool, fatal bool)

// fanIn merges one or more Kubernetes watches into a single item stream,
// one pump per watch.
//
// The stream closes when any pump returns, not when all of them do: a
// partial stream would let a collector keep publishing while one of the
// kinds it reports had silently stopped arriving. Stop is idempotent and
// releases every underlying watch.
//
// Every send is guarded by the stream's own context, which Stop cancels.
// Without that guard a pump blocked mid-send when its consumer stops
// receiving — which is exactly what happens when the collector's loop
// returns on cancellation — waits forever on a channel nobody will ever
// read again, and the goroutine is stranded for the life of the process.
func fanIn[E any](ctx context.Context, inner []watch.Interface, pumps []pump[E]) (<-chan E, func()) {
	streamCtx, cancel := context.WithCancel(ctx)
	items := make(chan E)

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			for _, w := range inner {
				w.Stop()
			}
		})
	}

	var wg sync.WaitGroup
	wg.Add(len(inner))
	for i := range inner {
		go func(source watch.Interface, convert pump[E]) {
			defer wg.Done()
			// Any pump returning tears down the merged stream.
			defer stop()
			for event := range source.ResultChan() {
				item, ok, fatal := convert(event)
				if fatal {
					return
				}
				if !ok {
					continue
				}
				select {
				case <-streamCtx.Done():
					return
				case items <- item:
				}
			}
		}(inner[i], pumps[i])
	}
	go func() {
		wg.Wait()
		close(items)
	}()

	return items, stop
}

// stream is the common half of the watch adapters: the merged item
// channel and the idempotent stop. The observe package names each
// watch's accessor differently, so the accessor itself is added by one
// of the three wrappers below rather than by this type.
type stream[E any] struct {
	items <-chan E
	stop  func()
}

// Stop releases every underlying watch.
func (s stream[E]) Stop() { s.stop() }

// changeStream satisfies the watch interfaces whose accessor is
// Changes: events, the backup catalog and the access-review queue.
type changeStream[E any] struct{ stream[E] }

// Changes streams converted changes until the watch ends.
func (s changeStream[E]) Changes() <-chan E { return s.items }

// resultStream satisfies observe.Watch, whose accessor is Results.
type resultStream[E any] struct{ stream[E] }

// Results streams converted observations until the watch ends.
func (s resultStream[E]) Results() <-chan E { return s.items }

// eventStream satisfies observe.PodWatch, whose accessor is Events.
type eventStream[E any] struct{ stream[E] }

// Events streams converted pod events until the watch ends.
func (s eventStream[E]) Events() <-chan E { return s.items }
