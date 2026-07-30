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

// Package kube owns every interaction with the Kubernetes API. It is the
// only package allowed to import client-go, and it translates every
// client error into a redact category at this boundary. The consumers
// define the interfaces they need; this package provides the concrete
// and fake implementations.
package kube

import (
	"context"

	"github.com/fyannk/pgconsole/internal/redact"
)

// UnavailableProber reports readiness as unavailable. It is the wired
// prober when no Kubernetes API accessor is configured, so readiness
// stays honest instead of defaulting to ready.
type UnavailableProber struct{}

// Ready always reports the API probe as unavailable.
func (UnavailableProber) Ready(_ context.Context) error {
	return redact.NewError("readiness probe", redact.CategoryUnavailable, nil)
}

// FakeProber is a test double whose readiness outcome is set by the
// test. A nil Err reports ready.
type FakeProber struct {
	// Err is returned verbatim by Ready.
	Err error
}

// Ready returns the configured outcome.
func (f FakeProber) Ready(_ context.Context) error {
	return f.Err
}
