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

package authz

import (
	"strings"
	"testing"
)

// FuzzParseLevel exercises invariant 3 at its entry point. The level is
// asserted by a trusted proxy, but the value that arrives is still bytes
// off the wire: a misconfigured proxy, a header an attacker controls
// upstream, or a truncated value all reach this function. The invariant
// is that the fail-safe direction is closed — anything that is not
// exactly a grantable level admits nothing.
func FuzzParseLevel(f *testing.F) {
	for _, level := range GrantableLevels() {
		f.Add(level)
		f.Add(" " + level + " ")
		f.Add(strings.ToUpper(level))
	}
	f.Add("")
	f.Add("dba\x00")
	f.Add("dba\nview")
	f.Add(strings.Repeat("dba", 4096))

	grantable := make(map[string]bool, 3)
	for _, level := range GrantableLevels() {
		grantable[level] = true
	}

	f.Fuzz(func(t *testing.T, raw string) {
		tier := ParseLevel(raw)

		// The set is closed. A tier outside it would mean a comparison
		// elsewhere in the codebase silently changes meaning.
		switch tier {
		case TierNone, TierView, TierPowerUser, TierDBA:
		default:
			t.Fatalf("ParseLevel(%q) = %d, outside the closed tier set", raw, tier)
		}

		// The fail-safe direction. Only an exact grantable level, modulo
		// surrounding whitespace, may admit anything at all.
		if tier != TierNone && !grantable[strings.TrimSpace(raw)] {
			t.Fatalf("ParseLevel(%q) = %v, admitted a value that is not a grantable level", raw, tier)
		}

		// A control character means the value did not come from a
		// well-behaved proxy, so it admits nothing regardless of what
		// surrounds it.
		for _, r := range raw {
			if r < 0x20 || r == 0x7f {
				if tier != TierNone {
					t.Fatalf("ParseLevel(%q) = %v, admitted a value carrying a control character", raw, tier)
				}
				break
			}
		}

		if again := ParseLevel(raw); again != tier {
			t.Fatalf("ParseLevel(%q) is not deterministic: %v then %v", raw, tier, again)
		}
	})
}
