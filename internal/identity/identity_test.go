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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func request(headers map[string]string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestExtractorReadsUser(t *testing.T) {
	t.Parallel()
	e := NewExtractor("X-Forwarded-User")
	id, ok := e.FromRequest(request(map[string]string{"X-Forwarded-User": " alice "}))
	if !ok {
		t.Fatal("extraction failed")
	}
	if id.User != "alice" {
		t.Errorf("User = %q", id.User)
	}
}

func TestExtractorRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		header  string
		headers map[string]string
	}{
		{"no header", "X-Forwarded-User", map[string]string{}},
		{"empty user", "X-Forwarded-User", map[string]string{"X-Forwarded-User": "  "}},
		{"control characters", "X-Forwarded-User", map[string]string{"X-Forwarded-User": "al\tice"}},
		{"oversized user", "X-Forwarded-User", map[string]string{"X-Forwarded-User": strings.Repeat("a", MaxUserLength+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := NewExtractor(tc.header)
			if _, ok := e.FromRequest(request(tc.headers)); ok {
				t.Error("extraction accepted a bound violation")
			}
		})
	}
}

func TestExtractorDisabled(t *testing.T) {
	t.Parallel()
	e := NewExtractor("")
	if _, ok := e.FromRequest(request(map[string]string{"X-Forwarded-User": "alice"})); ok {
		t.Fatal("disabled extractor extracted an identity")
	}
}
