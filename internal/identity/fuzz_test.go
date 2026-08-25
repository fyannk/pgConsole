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

package identity

import (
	"net/http"
	"strings"
	"testing"
)

// FuzzExtractorFromRequest drives the forwarded-identity extractor with
// arbitrary header bytes. The value is written by a trusted proxy, but
// "trusted" describes the hop, not the content: the username it forwards
// often originates with the user. Whatever arrives, an accepted identity
// has to be bounded, free of control characters, and already trimmed,
// because it is rendered and logged downstream.
func FuzzExtractorFromRequest(f *testing.F) {
	f.Add("X-PgToolBox-User", "alice")
	f.Add("X-PgToolBox-User", "  alice  ")
	f.Add("X-PgToolBox-User", "")
	f.Add("X-PgToolBox-User", strings.Repeat("a", MaxUserLength+1))
	f.Add("X-PgToolBox-User", "alice\r\nX-Injected: yes")
	f.Add("", "alice")

	f.Fuzz(func(t *testing.T, header, value string) {
		// http.Header.Set panics on nothing, but a header name with
		// separators is not a header this extractor could ever be
		// configured with, and net/http would reject it upstream.
		if header == "" || strings.ContainsAny(header, " \t\r\n:") {
			return
		}
		if strings.ContainsAny(value, "\r\n") {
			return // net/http refuses to carry these; the proxy cannot send one.
		}

		r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.invalid/", nil)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		r.Header.Set(header, value)

		id, ok := NewExtractor(header).FromRequest(r)

		if !ok {
			if id != (Identity{}) {
				t.Fatalf("refused header %q=%q but returned %+v, not the zero identity", header, value, id)
			}
			return
		}

		if id.User == "" {
			t.Fatalf("accepted header %q=%q as an empty user", header, value)
		}
		if len(id.User) > MaxUserLength {
			t.Fatalf("accepted a user of %d bytes, over the %d bound", len(id.User), MaxUserLength)
		}
		if id.User != strings.TrimSpace(id.User) {
			t.Fatalf("accepted an untrimmed user %q", id.User)
		}
		for _, r := range id.User {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("accepted a user carrying a control character: %q", id.User)
			}
		}

		again, againOK := NewExtractor(header).FromRequest(r)
		if againOK != ok || again != id {
			t.Fatalf("extraction is not deterministic for %q=%q", header, value)
		}
	})
}
