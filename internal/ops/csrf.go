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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/fyannk/pgconsole/internal/observe"
)

// CSRFMaxAge bounds a confirmation token's lifetime. A restart mints a
// fresh key and invalidates in-flight tokens, which is acceptable: the
// application is stateless and holds no session store.
const CSRFMaxAge = 10 * time.Minute

// CSRF issues and verifies confirmation tokens with an HMAC over a
// per-process key. The key is random and lives only in memory, so no
// session store is needed and a cross-site attacker — unable to read a
// same-origin confirmation page — cannot obtain a valid token.
type CSRF struct {
	key   []byte
	clock observe.Clock
}

// NewCSRF builds a CSRF with a fresh random per-process key.
func NewCSRF(clock observe.Clock) (*CSRF, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &CSRF{key: key, clock: clock}, nil
}

// Issue mints a token binding the context string and an issue time.
func (c *CSRF) Issue(context string) string {
	issued := c.clock.Now().Unix()
	return strconv.FormatInt(issued, 10) + "." + c.mac(context, issued)
}

// Verify checks that token binds context and is within the age bound.
// The comparison is constant time.
func (c *CSRF) Verify(context, token string) bool {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return false
	}
	issued, err := strconv.ParseInt(token[:dot], 10, 64)
	if err != nil {
		return false
	}
	now := c.clock.Now().Unix()
	if now-issued < 0 || now-issued > int64(CSRFMaxAge/time.Second) {
		return false
	}
	want := c.mac(context, issued)
	return hmac.Equal([]byte(token[dot+1:]), []byte(want))
}

// mac computes the hex HMAC over the context and issue time.
func (c *CSRF) mac(context string, issued int64) string {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(context))
	m.Write([]byte{0})
	m.Write([]byte(strconv.FormatInt(issued, 10)))
	return hex.EncodeToString(m.Sum(nil))
}
