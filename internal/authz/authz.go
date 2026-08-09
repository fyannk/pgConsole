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

// Package authz maps the trusted proxy's asserted authorization level
// onto the console's capability tier. The console does not decide the
// level; the operator's proxy authenticates the user and forwards it,
// trustworthy only because the deployment confines the console's ingress
// to that proxy. Parsing is total and closed: a missing, empty,
// malformed, or unknown level yields TierNone, so nothing above the
// read-only baseline is ever reached by an unrecognized value.
package authz

import "strings"

// Tier is a decided capability, derived from the proxy-asserted level.
type Tier int

// The tiers, ordered so a higher value strictly includes the lower
// ones. An unrecognized level is always TierNone.
const (
	// TierNone reaches only the read-only baseline the proxy already
	// authenticated; no gated capability is granted.
	TierNone Tier = iota
	// TierView is the explicit read-only level.
	TierView
	// TierPowerUser additionally reaches the bounded instance and pooler
	// log tails.
	TierPowerUser
	// TierDBA additionally may execute the enumerated day-2 operations,
	// subject to the operations master switch, and reaches the
	// access-request review panel.
	TierDBA
)

// Level names of the proxy's closed authorization set.
const (
	// LevelView is the read-only level.
	LevelView = "view"
	// LevelPowerUser is the operations level.
	LevelPowerUser = "poweruser"
	// LevelDBA is the review level.
	LevelDBA = "dba"
)

// GrantableLevels lists the closed level set in ascending order, for the
// one caller that has to offer a choice between them: the access-request
// review panel's approval picker.
//
// The set is hardcoded on both sides of the contract — the operator's
// RoleLevel enum and this constant block — because there is nothing an
// operator could add. That is why the picker enumerates a constant here
// rather than listing objects from the API: there is no PgToolBoxRole kind
// to list, and a level that is not one of these three means nothing to
// either side.
//
// The returned slice is a copy, so a caller cannot reorder the set for
// everyone else.
func GrantableLevels() []string {
	return []string{LevelView, LevelPowerUser, LevelDBA}
}

// String names the tier for logs and display.
func (t Tier) String() string {
	switch t {
	case TierView:
		return "view"
	case TierPowerUser:
		return "poweruser"
	case TierDBA:
		return "dba"
	default:
		return "none"
	}
}

// ParseLevel maps one proxy-asserted level value onto a tier. It trims
// surrounding whitespace, rejects any control character, and matches the
// closed set exactly; every other value — empty, unknown, or malformed —
// is TierNone. Nothing here trusts the value beyond what the deployment
// invariant already guarantees; the mapping is what turns it into a
// capability.
func ParseLevel(raw string) Tier {
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return TierNone
		}
	}
	switch strings.TrimSpace(raw) {
	case LevelView:
		return TierView
	case LevelPowerUser:
		return TierPowerUser
	case LevelDBA:
		return TierDBA
	default:
		return TierNone
	}
}
