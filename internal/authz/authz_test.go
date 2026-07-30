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

func TestParseLevelClosedSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want Tier
	}{
		{"view", "view", TierView},
		{"poweruser", "poweruser", TierPowerUser},
		{"dba", "dba", TierDBA},
		{"trimmed", "  dba  ", TierDBA},
		{"empty", "", TierNone},
		{"unknown word", "operate", TierNone},
		{"superuser is not a level", "superuser", TierNone},
		{"case sensitive", "DBA", TierNone},
		{"leading junk", "dba;poweruser", TierNone},
		{"control character rejected", "db\ta", TierNone},
		{"newline rejected", "dba\n", TierNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseLevel(tc.in); got != tc.want {
				t.Errorf("ParseLevel(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestTierOrdering proves the ladder is strictly increasing, so a
// minimum-level gate admits exactly the tiers at or above it.
func TestTierOrdering(t *testing.T) {
	t.Parallel()
	if !(TierNone < TierView && TierView < TierPowerUser && TierPowerUser < TierDBA) {
		t.Fatalf("tier ladder not strictly increasing: %d %d %d %d",
			TierNone, TierView, TierPowerUser, TierDBA)
	}
}

func TestTierString(t *testing.T) {
	t.Parallel()
	cases := map[Tier]string{
		TierNone:      "none",
		TierView:      "view",
		TierPowerUser: "poweruser",
		TierDBA:       "dba",
		Tier(99):      "none",
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}

// TestParseLevelNeverPanicsOnControl sweeps every low control byte to
// prove none escapes the rejection into an elevated tier.
func TestParseLevelNeverPanicsOnControl(t *testing.T) {
	t.Parallel()
	for b := 0; b < 0x20; b++ {
		in := "dba" + string(rune(b))
		if got := ParseLevel(in); got != TierNone {
			t.Errorf("ParseLevel(%q) = %s, want none", strings.TrimSpace(in), got)
		}
	}
	if got := ParseLevel("dba" + string(rune(0x7f))); got != TierNone {
		t.Errorf("ParseLevel with DEL = %s, want none", got)
	}
}
