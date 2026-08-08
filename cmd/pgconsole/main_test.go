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

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fyannk/pgConsole/internal/config"
)

// lookupFrom turns a map into a config.Lookup, so a case states exactly
// the environment it depends on and inherits nothing from the test
// process.
func lookupFrom(env map[string]string) config.Lookup {
	return func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
}

// minimalEnv is the smallest environment that builds: the two required
// variables and nothing else. Cases copy it and add the one variable
// under test, so a failure names that variable and not a shared fixture.
func minimalEnv() map[string]string {
	return map[string]string{
		config.EnvClusterName: "orders",
		config.EnvNamespace:   "payments",
	}
}

// unusableDirPath returns an absolute path whose parent is a regular
// file. Every filesystem refuses to create a child of a non-directory, so
// this is an unusable path that needs no permission games to produce.
func unusableDirPath(t *testing.T, leaf string) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding the blocking file: %v", err)
	}
	return filepath.Join(blocker, leaf)
}

func TestCLIVersionFlagPrintsVersionAndSkipsRun(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--version", "-version"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr strings.Builder
			ran := false
			code := cli([]string{flag}, &stdout, &stderr, func() error {
				ran = true
				return nil
			})
			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			// The released image is distroless: this output is the only
			// way to ask a binary what it is, so it must be the bare
			// version and nothing else.
			if got := stdout.String(); got != version+"\n" {
				t.Errorf("stdout = %q, want %q", got, version+"\n")
			}
			if got := stderr.String(); got != "" {
				t.Errorf("stderr = %q, want empty", got)
			}
			// Asking the version must never start a console.
			if ran {
				t.Error("run was called for a version query")
			}
		})
	}
}

func TestCLIUnknownArgumentRefusesRatherThanIgnores(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	ran := false
	code := cli([]string{"--allow-operations"}, &stdout, &stderr, func() error {
		ran = true
		return nil
	})
	// A misspelled flag that changes nothing silently is how an operator
	// concludes the setting had no effect; 2 distinguishes it from a
	// startup failure, which is 1.
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if ran {
		t.Error("run was called despite an unknown argument")
	}
	if !strings.Contains(stderr.String(), "--allow-operations") {
		t.Errorf("stderr = %q, want it to name the rejected argument", stderr.String())
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
}

func TestCLIWithoutArgumentsReportsRunOutcome(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr strings.Builder
		code := cli(nil, &stdout, &stderr, func() error { return nil })
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if got := stderr.String(); got != "" {
			t.Errorf("stderr = %q, want empty", got)
		}
	})
	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr strings.Builder
		code := cli(nil, &stdout, &stderr, func() error {
			return errors.New("CLUSTER_NAME must be a DNS-1123 label")
		})
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "CLUSTER_NAME") {
			t.Errorf("stderr = %q, want the run error reported", stderr.String())
		}
	})
}

// TestBuildAssemblesWithoutListening pins the seam the fail-before-listen
// cases below depend on: build returns a usable application and opens no
// listener, so a refusal in one of those cases is a refusal to start and
// not merely a late failure.
func TestBuildAssemblesWithoutListening(t *testing.T) {
	t.Parallel()
	app, err := build(lookupFrom(minimalEnv()), io.Discard)
	if err != nil {
		t.Fatalf("build with a valid minimal environment: %v", err)
	}
	if app == nil {
		t.Fatal("build returned no application and no error")
	}
	if addr := app.Addr(); addr != "" {
		t.Errorf("Addr() = %q before Listen, want empty: build must not bind", addr)
	}
}

// TestBuildRefusesBeforeListening covers the contracts that decide whether
// the console starts at all. Each one is a deployment that asked for a
// capability and mounted it wrong: half a contract must not half-work, so
// the refusal has to happen here rather than degrade a panel later.
func TestBuildRefusesBeforeListening(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  func(t *testing.T) map[string]string
		want string
	}{
		{
			name: "invalid configuration",
			env: func(*testing.T) map[string]string {
				env := minimalEnv()
				delete(env, config.EnvClusterName)
				return env
			},
			want: config.EnvClusterName,
		},
		{
			name: "unusable history journal",
			env: func(t *testing.T) map[string]string {
				env := minimalEnv()
				env[config.EnvHistoryEnabled] = "true"
				env[config.EnvHistoryPath] = unusableDirPath(t, "history.db")
				return env
			},
		},
		{
			name: "unusable metrics snapshot path",
			env: func(t *testing.T) map[string]string {
				env := minimalEnv()
				env[config.EnvMetricsEnabled] = "true"
				env[config.EnvMetricsPath] = unusableDirPath(t, "metrics.json")
				return env
			},
		},
		{
			name: "unreadable evidence token",
			env: func(t *testing.T) map[string]string {
				env := minimalEnv()
				env[config.EnvRepositoryEvidenceURL] = "unix:///run/pgconsole/evidence.sock"
				env[config.EnvRepositoryEvidenceTokenFile] = filepath.Join(t.TempDir(), "absent-token")
				env[config.EnvRepositoryExpectedFingerprint] = "sha256:" + strings.Repeat("a", 64)
				env[config.EnvRepositoryBarmanServer] = "orders"
				return env
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, err := build(lookupFrom(tc.env(t)), io.Discard)
			if err == nil {
				t.Fatal("build succeeded, want a refusal before listen")
			}
			if app != nil {
				t.Error("build returned an application alongside an error")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %s", err, tc.want)
			}
		})
	}
}

// TestBuildKeepsWritersBehindTheirFlags proves the capability flags decide
// whether a mutation surface is constructed at all. Without a cluster
// there is no client to hand over, so this asserts the reachable half:
// the flags parse, and building stays a read-only assembly.
func TestBuildDisabledCapabilitiesStillAssemble(t *testing.T) {
	t.Parallel()
	env := minimalEnv()
	env[config.EnvAllowOperations] = "false"
	env[config.EnvAllowAccessReview] = "false"
	env[config.EnvAllowClusterCatalogs] = "false"
	env[config.EnvAllowLogs] = "false"
	env[config.EnvHistoryEnabled] = "false"
	env[config.EnvMetricsEnabled] = "false"

	app, err := build(lookupFrom(env), io.Discard)
	if err != nil {
		t.Fatalf("build with every optional capability off: %v", err)
	}
	if app == nil {
		t.Fatal("build returned no application and no error")
	}
}

// TestBuildRejectsNonBooleanCapabilityFlag keeps the strict-boolean
// contract visible from the entrypoint: a flag that is neither "true" nor
// "false" fails startup rather than being read as off, because silently
// treating a typo as disabled is how an operator ships an unguarded
// console believing it is guarded.
func TestBuildRejectsNonBooleanCapabilityFlag(t *testing.T) {
	t.Parallel()
	env := minimalEnv()
	env[config.EnvAllowOperations] = "yes"

	if _, err := build(lookupFrom(env), io.Discard); err == nil {
		t.Fatal("build accepted ALLOW_OPERATIONS=yes, want a refusal")
	} else if !strings.Contains(err.Error(), config.EnvAllowOperations) {
		t.Errorf("error = %q, want it to name %s", err, config.EnvAllowOperations)
	}
}
