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
	"html/template"
	"strings"
)

// Manifests are highlighted here rather than in the browser. The
// console's script-src forbids eval, so a highlighting library is out;
// more to the point, the drawing rule applies to text as well — the
// document ships finished, so a reader without scripting sees the same
// coloured manifest as everyone else, and the dialog needs no hook to
// colour what was swapped into it.
//
// The scanner is deliberately small. Its input is always the output of
// json.Indent over a manifest the API server produced, so it never has
// to recover from malformed input: it classifies what it recognises and
// passes anything else through as plain text. Every emitted segment is
// HTML-escaped, so a string value carrying markup cannot leave its span.

// highlightJSON wraps a pretty-printed JSON document's tokens in spans.
// A key is told from a string value by what follows it, which is the
// only lookahead the grammar needs at this depth.
func highlightJSON(src string) template.HTML {
	var b strings.Builder
	b.Grow(len(src) + len(src)/2)
	plain := 0 // start of the pending run of unclassified bytes

	flush := func(upto int) {
		if upto > plain {
			b.WriteString(template.HTMLEscapeString(src[plain:upto]))
		}
	}
	span := func(class, text string) {
		b.WriteString(`<span class="`)
		b.WriteString(class)
		b.WriteString(`">`)
		b.WriteString(template.HTMLEscapeString(text))
		b.WriteString(`</span>`)
	}

	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			end, ok := jsonStringEnd(src, i)
			if !ok {
				i++
				continue
			}
			flush(i)
			class := "j-str"
			if jsonIsKey(src, end) {
				class = "j-key"
			}
			span(class, src[i:end])
			i, plain = end, end
		case c == '-' || (c >= '0' && c <= '9'):
			end := i
			for end < len(src) && strings.IndexByte("-+.eE0123456789", src[end]) >= 0 {
				end++
			}
			flush(i)
			span("j-num", src[i:end])
			i, plain = end, end
		case strings.HasPrefix(src[i:], "true"), strings.HasPrefix(src[i:], "false"):
			end := i + 4
			if src[i] == 'f' {
				end = i + 5
			}
			flush(i)
			span("j-bool", src[i:end])
			i, plain = end, end
		case strings.HasPrefix(src[i:], "null"):
			flush(i)
			span("j-null", src[i:i+4])
			i, plain = i+4, i+4
		default:
			i++
		}
	}
	flush(len(src))
	return template.HTML(b.String()) //nolint:gosec // every segment above is HTML-escaped.
}

// jsonStringEnd returns the index just past the string literal opening
// at start, honouring backslash escapes so an escaped quote does not end
// it early.
func jsonStringEnd(src string, start int) (int, bool) {
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// jsonIsKey reports whether the string ending at end is an object key,
// which it is exactly when a colon is the next thing that matters.
func jsonIsKey(src string, end int) bool {
	for i := end; i < len(src); i++ {
		switch src[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}
