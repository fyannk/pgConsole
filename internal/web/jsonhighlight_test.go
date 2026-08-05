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
	"strings"
	"testing"
)

// A key and a string value look identical until you see what follows,
// so the classification is asserted on a document that carries both.
func TestHighlightJSONTellsKeysFromValues(t *testing.T) {
	t.Parallel()
	got := string(highlightJSON("{\n  \"kind\": \"Pod\",\n  \"n\": 3,\n  \"ok\": true,\n  \"gone\": null\n}"))
	for _, want := range []string{
		`<span class="j-key">&#34;kind&#34;</span>`,
		`<span class="j-str">&#34;Pod&#34;</span>`,
		`<span class="j-num">3</span>`,
		`<span class="j-bool">true</span>`,
		`<span class="j-null">null</span>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("highlight misses %s\ngot: %s", want, got)
		}
	}
	// The indentation is the whole point of pretty-printing, so it must
	// survive being wrapped in spans.
	if !strings.Contains(got, "\n  <span") {
		t.Errorf("highlighting flattened the indentation: %q", got)
	}
}

// The highlighter emits markup, so it is a place a manifest value could
// escape into the page. It must not: every segment is escaped as it is
// wrapped, including the parts that never reach a span.
func TestHighlightJSONEscapesEverySegment(t *testing.T) {
	t.Parallel()
	got := string(highlightJSON(`{"note": "<img src=x onerror=alert(1)>"}`))
	if strings.Contains(got, "<img") {
		t.Errorf("a string value emitted live markup: %s", got)
	}
	if !strings.Contains(got, "&lt;img") {
		t.Errorf("the value was lost rather than escaped: %s", got)
	}
}

// A key whose text contains a quote must not end its own span early,
// and a lone unterminated quote must not swallow the document.
func TestHighlightJSONSurvivesAwkwardStrings(t *testing.T) {
	t.Parallel()
	got := string(highlightJSON(`{"a\"b": "v"}`))
	if !strings.Contains(got, `<span class="j-key">&#34;a\&#34;b&#34;</span>`) {
		t.Errorf("an escaped quote ended the key early: %s", got)
	}
	if plain := string(highlightJSON(`{"unterminated`)); !strings.Contains(plain, "unterminated") {
		t.Errorf("an unterminated string dropped its text: %s", plain)
	}
}
