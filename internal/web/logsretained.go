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

package web

import (
	"fmt"
	"strings"
)

// retainedLog assembles the buffer's stream for one container into
// rendered segments, reporting whether the buffer holds that stream at
// all. Not holding it is a fallback signal, never an error: the caller
// serves the live tail instead.
//
// No membership check runs here, and none can: the pod may no longer
// exist, and its retained stream outliving it is precisely the value.
// The proof happened at collection time — the follower verifies
// controller ownership before every stream it opens — and that is the
// only proof a dead pod can ever have.
func (h *Handler) retainedLog(pod, container string) (segments []LogSegmentView, note string, ok bool) {
	buffer := h.sources.LogBuffer
	if !buffer.Enabled() {
		return nil, "", false
	}
	entries, held := buffer.Tail(pod, container, h.now())
	if !held {
		return nil, "", false
	}

	// Consecutive lines join into one text segment; a gap ends the run
	// and stands alone. The grouping is presentation only — nothing is
	// reordered, and nothing joins across a marker. Each text segment
	// carries the observation stamp of its first line, so the reader
	// knows how far back each stretch of the record starts.
	var run []string
	var runStart Stamp
	flush := func() {
		if len(run) > 0 {
			segments = append(segments, LogSegmentView{At: runStart, Text: strings.Join(run, "\n")})
			run = nil
		}
	}
	for _, entry := range entries {
		if entry.Gap {
			flush()
			segments = append(segments, LogSegmentView{
				Gap:  true,
				At:   stampAt(entry.At),
				Note: entry.Text,
			})
			continue
		}
		if len(run) == 0 {
			runStart = stampAt(entry.At)
		}
		run = append(run, entry.Text)
	}
	flush()

	note = fmt.Sprintf(
		"retained while following, best effort — kept at most %s and bounded in bytes, "+
			"so older lines may be gone; a break in following is shown as a gap, and "+
			"lines emitted during one were not observed", buffer.MaxAge())
	return segments, note, true
}
