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

// Package identity extracts the proxy-forwarded identity from a request
// header: a bounded, control-character-free username. The identity is
// display and audit-attribution material only; authorization is the
// proxy-asserted level, decided elsewhere, and nothing here authorizes
// anything.
package identity

import (
	"net/http"
	"strings"
)

// MaxUserLength bounds the username header value in bytes. A proxy
// forwarding something larger is misconfigured; the excess is refused,
// not truncated into a different identity.
const MaxUserLength = 512

// Identity is one proxy-forwarded identity.
type Identity struct {
	// User is the forwarded username.
	User string
}

// Extractor reads identities from the configured header.
type Extractor struct {
	userHeader string
}

// NewExtractor builds an extractor for the configured header name. An
// empty user header name disables extraction entirely.
func NewExtractor(userHeader string) *Extractor {
	return &Extractor{userHeader: userHeader}
}

// FromRequest extracts the identity. ok is false when extraction is
// disabled, the user header is absent or empty, or the length bound is
// violated — a violated bound is a misconfigured proxy, never a partial
// identity.
func (e *Extractor) FromRequest(r *http.Request) (id Identity, ok bool) {
	if e.userHeader == "" {
		return Identity{}, false
	}
	user := sanitize(r.Header.Get(e.userHeader))
	if user == "" || len(user) > MaxUserLength {
		return Identity{}, false
	}
	return Identity{User: user}, true
}

// sanitize trims space and refuses control characters, which have no
// place in an identity and could corrupt logs or displays.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return s
}
