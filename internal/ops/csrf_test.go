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

package ops

import (
	"context"
	"testing"
	"time"
)

// advancingClock returns a settable clock for CSRF age tests.
type advancingClock struct{ t time.Time }

func (c *advancingClock) Now() time.Time                                { return c.t }
func (c *advancingClock) Wait(_ context.Context, _ time.Duration) error { return nil }

func TestCSRFRoundTrip(t *testing.T) {
	t.Parallel()
	clock := &advancingClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	c, err := NewCSRF(clock)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	token := c.Issue("restart\x00")
	if !c.Verify("restart\x00", token) {
		t.Fatal("fresh token rejected")
	}
}

// TestCSRFRejectsForgedAndCrossBound proves a token is bound to its
// context and cannot be reused for a different operation, and that a
// token minted under a different per-process key does not verify.
func TestCSRFRejectsForgedAndCrossBound(t *testing.T) {
	t.Parallel()
	clock := &advancingClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	c, _ := NewCSRF(clock)
	token := c.Issue("reload\x00")

	if c.Verify("restart\x00", token) {
		t.Error("token verified for a different operation")
	}
	if c.Verify("reload\x00", "garbage") {
		t.Error("garbage token verified")
	}
	if c.Verify("reload\x00", "9999999999.deadbeef") {
		t.Error("forged HMAC verified")
	}

	other, _ := NewCSRF(clock)
	if other.Verify("reload\x00", token) {
		t.Error("token verified under a different per-process key")
	}
}

func TestCSRFExpires(t *testing.T) {
	t.Parallel()
	clock := &advancingClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	c, _ := NewCSRF(clock)
	token := c.Issue("promote\x00orders-2")
	clock.t = clock.t.Add(CSRFMaxAge + time.Second)
	if c.Verify("promote\x00orders-2", token) {
		t.Fatal("expired token verified")
	}
}
