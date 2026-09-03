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

package config

import (
	"maps"
	"strings"
	"testing"
	"time"
)

// mapLookup builds a Lookup from a map for hermetic tests.
func mapLookup(env map[string]string) Lookup {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// base returns the minimal valid environment.
func base() map[string]string {
	return map[string]string{
		EnvClusterName: "orders",
		EnvNamespace:   "payments",
	}
}

// evidenceEnv returns a complete valid repository-evidence variable set
// with the given overrides applied, so a case can vary one variable
// while satisfying the all-or-nothing rule.
func evidenceEnv(overrides map[string]string) map[string]string {
	env := map[string]string{
		EnvRepositoryEvidenceURL:         "unix:///var/run/objectstoreviewer/evidence.sock",
		EnvRepositoryEvidenceTokenFile:   "/var/run/objectstoreviewer/token",
		EnvRepositoryExpectedFingerprint: "sha256:" + strings.Repeat("ab", 32),
		EnvRepositoryBarmanServer:        "orders",
	}
	maps.Copy(env, overrides)
	return env
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Load(mapLookup(base()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ClusterName != "orders" || cfg.Namespace != "payments" {
		t.Fatalf("target identity not loaded: %+v", cfg)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.TrustedUserHeader != DefaultTrustedUserHeader {
		t.Errorf("TrustedUserHeader = %q, want %q", cfg.TrustedUserHeader, DefaultTrustedUserHeader)
	}
	if cfg.AllowOperations {
		t.Error("AllowOperations must default to false")
	}
	if cfg.AllowAccessReview {
		t.Error("AllowAccessReview must default to false")
	}
	if cfg.TrustedLevelHeader != DefaultTrustedLevelHeader {
		t.Errorf("TrustedLevelHeader = %q, want %q", cfg.TrustedLevelHeader, DefaultTrustedLevelHeader)
	}
	if !cfg.AllowLogs {
		t.Error("AllowLogs must default to true")
	}
	if cfg.AllowInsecureLinks {
		t.Error("AllowInsecureLinks must default to false")
	}
	if cfg.LogTailLines != DefaultLogTailLines {
		t.Errorf("LogTailLines = %d, want %d", cfg.LogTailLines, DefaultLogTailLines)
	}
	if cfg.LogTailMaxBytes != DefaultLogTailMaxBytes {
		t.Errorf("LogTailMaxBytes = %d, want %d", cfg.LogTailMaxBytes, DefaultLogTailMaxBytes)
	}
	if cfg.EventsMaxAge != DefaultEventsMaxAge {
		t.Errorf("EventsMaxAge = %s, want %s", cfg.EventsMaxAge, DefaultEventsMaxAge)
	}
	if cfg.APIRequestTimeout != DefaultAPIRequestTimeout {
		t.Errorf("APIRequestTimeout = %s, want %s", cfg.APIRequestTimeout, DefaultAPIRequestTimeout)
	}
	if cfg.ObjectStoreViewerURL != "" || cfg.PgAdminURL != "" || cfg.MonitoringURL != "" || cfg.RepositoryEvidenceURL != "" {
		t.Error("optional URLs must default to empty")
	}
	if !cfg.HistoryEnabled {
		t.Error("HistoryEnabled must default to true")
	}
	if cfg.HistoryMaxRevisions != DefaultHistoryMaxRevisions {
		t.Errorf("HistoryMaxRevisions = %d, want %d", cfg.HistoryMaxRevisions, DefaultHistoryMaxRevisions)
	}
	if cfg.HistoryMaxBytes != DefaultHistoryMaxBytes {
		t.Errorf("HistoryMaxBytes = %d, want %d", cfg.HistoryMaxBytes, DefaultHistoryMaxBytes)
	}
	if cfg.HistoryPerObjectRevisions != DefaultHistoryPerObjectRevisions {
		t.Errorf("HistoryPerObjectRevisions = %d, want %d", cfg.HistoryPerObjectRevisions, DefaultHistoryPerObjectRevisions)
	}
	if cfg.HistoryCoalesceWindow != DefaultHistoryCoalesceWindow {
		t.Errorf("HistoryCoalesceWindow = %s, want %s", cfg.HistoryCoalesceWindow, DefaultHistoryCoalesceWindow)
	}
}

func TestLoadMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  map[string]string
		wantErr string
		check   func(t *testing.T, cfg Config)
	}{
		{
			name:    "missing cluster name",
			mutate:  map[string]string{EnvClusterName: ""},
			wantErr: EnvClusterName + ": is required",
		},
		{
			name:    "missing namespace",
			mutate:  map[string]string{EnvNamespace: ""},
			wantErr: EnvNamespace + ": is required",
		},
		{
			name:    "cluster name not a label",
			mutate:  map[string]string{EnvClusterName: "Orders_DB"},
			wantErr: EnvClusterName + ": must be a DNS-1123 label",
		},
		{
			name:    "cluster name too long",
			mutate:  map[string]string{EnvClusterName: strings.Repeat("a", 64)},
			wantErr: EnvClusterName + ": must be a DNS-1123 label",
		},
		{
			name:    "namespace not a label",
			mutate:  map[string]string{EnvNamespace: "pay ments"},
			wantErr: EnvNamespace + ": must be a DNS-1123 label",
		},
		{
			name:   "custom listen address",
			mutate: map[string]string{EnvListenAddr: "127.0.0.1:8080"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ListenAddr != "127.0.0.1:8080" {
					t.Errorf("ListenAddr = %q", cfg.ListenAddr)
				}
			},
		},
		{
			name:    "listen address without port",
			mutate:  map[string]string{EnvListenAddr: "localhost"},
			wantErr: EnvListenAddr + ": must be a host:port listen address",
		},
		{
			name:    "listen address with symbolic port",
			mutate:  map[string]string{EnvListenAddr: ":http"},
			wantErr: EnvListenAddr + ": must use a numeric port",
		},
		{
			name:   "empty trusted header disables display",
			mutate: map[string]string{EnvTrustedUserHeader: ""},
			check: func(t *testing.T, cfg Config) {
				if cfg.TrustedUserHeader != "" {
					t.Errorf("TrustedUserHeader = %q, want empty", cfg.TrustedUserHeader)
				}
			},
		},
		{
			name:    "invalid trusted header",
			mutate:  map[string]string{EnvTrustedUserHeader: "X Forwarded User"},
			wantErr: EnvTrustedUserHeader + ": must be a valid HTTP header name",
		},
		{
			name:   "operations enabled explicitly",
			mutate: map[string]string{EnvAllowOperations: "true"},
			check: func(t *testing.T, cfg Config) {
				if !cfg.AllowOperations {
					t.Error("AllowOperations = false, want true")
				}
			},
		},
		{
			name:    "operations flag not a strict boolean",
			mutate:  map[string]string{EnvAllowOperations: "yes"},
			wantErr: EnvAllowOperations + `: must be "true" or "false"`,
		},
		{
			name:   "access review enabled",
			mutate: map[string]string{EnvAllowAccessReview: "true"},
			check: func(t *testing.T, cfg Config) {
				if !cfg.AllowAccessReview {
					t.Error("AllowAccessReview = false, want true")
				}
			},
		},
		{
			name:    "access review flag not a strict boolean",
			mutate:  map[string]string{EnvAllowAccessReview: "sometimes"},
			wantErr: EnvAllowAccessReview + `: must be "true" or "false"`,
		},
		{
			name:    "logs flag not a strict boolean",
			mutate:  map[string]string{EnvAllowLogs: "1"},
			wantErr: EnvAllowLogs + `: must be "true" or "false"`,
		},
		{
			name:    "log tail lines below bound",
			mutate:  map[string]string{EnvLogTailLines: "0"},
			wantErr: EnvLogTailLines + ": must be an integer between 1 and 2000",
		},
		{
			name:    "log tail lines above bound",
			mutate:  map[string]string{EnvLogTailLines: "2001"},
			wantErr: EnvLogTailLines + ": must be an integer between 1 and 2000",
		},
		{
			name:    "log tail lines not numeric",
			mutate:  map[string]string{EnvLogTailLines: "many"},
			wantErr: EnvLogTailLines + ": must be an integer between 1 and 2000",
		},
		{
			name:   "log tail bytes at bound",
			mutate: map[string]string{EnvLogTailMaxBytes: "4096"},
			check: func(t *testing.T, cfg Config) {
				if cfg.LogTailMaxBytes != 4096 {
					t.Errorf("LogTailMaxBytes = %d", cfg.LogTailMaxBytes)
				}
			},
		},
		{
			name:    "log tail bytes above bound",
			mutate:  map[string]string{EnvLogTailMaxBytes: "8388609"},
			wantErr: EnvLogTailMaxBytes + ": must be an integer between 4096 and 8388608",
		},
		{
			name:    "events age below bound",
			mutate:  map[string]string{EnvEventsMaxAge: "30s"},
			wantErr: EnvEventsMaxAge + ": must be a duration between 1m0s and 24h0m0s",
		},
		{
			name:    "events age malformed",
			mutate:  map[string]string{EnvEventsMaxAge: "soon"},
			wantErr: EnvEventsMaxAge + ": must be a duration between 1m0s and 24h0m0s",
		},
		{
			name:    "api timeout above bound",
			mutate:  map[string]string{EnvAPIRequestTimeout: "2m"},
			wantErr: EnvAPIRequestTimeout + ": must be a duration between 1s and 1m0s",
		},
		{
			name:   "history disabled explicitly",
			mutate: map[string]string{EnvHistoryEnabled: "false"},
			check: func(t *testing.T, cfg Config) {
				if cfg.HistoryEnabled {
					t.Error("HistoryEnabled = true, want false")
				}
			},
		},
		{
			name:    "history flag not a strict boolean",
			mutate:  map[string]string{EnvHistoryEnabled: "on"},
			wantErr: EnvHistoryEnabled + `: must be "true" or "false"`,
		},
		{
			name:   "metrics disabled explicitly",
			mutate: map[string]string{EnvMetricsEnabled: "false"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MetricsEnabled {
					t.Error("MetricsEnabled = true, want false")
				}
			},
		},
		{
			name:   "metrics cadence and retention configured",
			mutate: map[string]string{EnvMetricsInterval: "30s", EnvMetricsRetention: "48h"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MetricsInterval != 30*time.Second || cfg.MetricsRetention != 48*time.Hour {
					t.Errorf("metrics bounds = %v/%v", cfg.MetricsInterval, cfg.MetricsRetention)
				}
			},
		},
		{
			name:    "metrics interval below bound",
			mutate:  map[string]string{EnvMetricsInterval: "1s"},
			wantErr: EnvMetricsInterval + ": must be a duration between 5s and 5m0s",
		},
		{
			name:    "metrics retention above bound",
			mutate:  map[string]string{EnvMetricsRetention: "1000h"},
			wantErr: EnvMetricsRetention + ": must be a duration between 1h0m0s and 720h0m0s",
		},
		{
			name:   "metrics snapshot path configured",
			mutate: map[string]string{EnvMetricsPath: "/var/lib/pgconsole/metrics.snapshot"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MetricsPath != "/var/lib/pgconsole/metrics.snapshot" {
					t.Errorf("MetricsPath = %q", cfg.MetricsPath)
				}
			},
		},
		{
			name:    "metrics path must be absolute",
			mutate:  map[string]string{EnvMetricsPath: "metrics.snapshot"},
			wantErr: EnvMetricsPath + ": must be an absolute file path",
		},
		{
			name:    "metrics path conflicts with disabled metrics",
			mutate:  map[string]string{EnvMetricsEnabled: "false", EnvMetricsPath: "/var/lib/pgconsole/metrics.snapshot"},
			wantErr: EnvMetricsPath + ": requires " + EnvMetricsEnabled + "=true",
		},
		{
			name:    "history revisions below bound",
			mutate:  map[string]string{EnvHistoryMaxRevisions: "99"},
			wantErr: EnvHistoryMaxRevisions + ": must be an integer between 100 and 20000",
		},
		{
			name:    "history bytes above bound",
			mutate:  map[string]string{EnvHistoryMaxBytes: "67108865"},
			wantErr: EnvHistoryMaxBytes + ": must be an integer between 1048576 and 67108864",
		},
		{
			name:    "history per-object revisions above bound",
			mutate:  map[string]string{EnvHistoryPerObjectRevisions: "201"},
			wantErr: EnvHistoryPerObjectRevisions + ": must be an integer between 2 and 200",
		},
		{
			name:    "history coalesce window malformed",
			mutate:  map[string]string{EnvHistoryCoalesceWindow: "soon"},
			wantErr: EnvHistoryCoalesceWindow + ": must be a duration between 1s and 1h0m0s",
		},
		{
			name:   "history path accepted",
			mutate: map[string]string{EnvHistoryPath: "/var/lib/pgconsole/history.db"},
			check: func(t *testing.T, cfg Config) {
				if cfg.HistoryPath != "/var/lib/pgconsole/history.db" {
					t.Errorf("HistoryPath = %q", cfg.HistoryPath)
				}
			},
		},
		{
			name:   "history path defaults to empty",
			mutate: map[string]string{},
			check: func(t *testing.T, cfg Config) {
				if cfg.HistoryPath != "" {
					t.Errorf("HistoryPath = %q, want empty", cfg.HistoryPath)
				}
			},
		},
		{
			name:    "history path must be absolute",
			mutate:  map[string]string{EnvHistoryPath: "history.db"},
			wantErr: EnvHistoryPath + ": must be an absolute file path",
		},
		{
			name: "history path with history disabled is conflicting intent",
			mutate: map[string]string{
				EnvHistoryEnabled: "false",
				EnvHistoryPath:    "/var/lib/pgconsole/history.db",
			},
			wantErr: EnvHistoryPath + ": requires " + EnvHistoryEnabled + "=true",
		},
		{
			name:   "history bounds accepted at the edges",
			mutate: map[string]string{EnvHistoryMaxRevisions: "100", EnvHistoryCoalesceWindow: "1s"},
			check: func(t *testing.T, cfg Config) {
				if cfg.HistoryMaxRevisions != 100 || cfg.HistoryCoalesceWindow != time.Second {
					t.Errorf("bounds not applied: %d, %s", cfg.HistoryMaxRevisions, cfg.HistoryCoalesceWindow)
				}
			},
		},
		{
			name:   "https link accepted",
			mutate: map[string]string{EnvObjectStoreViewerURL: "https://viewer.example.com/repo"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ObjectStoreViewerURL == "" {
					t.Error("ObjectStoreViewerURL not loaded")
				}
			},
		},
		{
			name:    "http link rejected by default",
			mutate:  map[string]string{EnvPgAdminURL: "http://pgadmin.lab.internal"},
			wantErr: EnvPgAdminURL + ": http scheme requires " + EnvAllowInsecureLinks + "=true",
		},
		{
			name: "http link accepted for lab use",
			mutate: map[string]string{
				EnvPgAdminURL:         "http://pgadmin.lab.internal",
				EnvAllowInsecureLinks: "true",
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.PgAdminURL == "" {
					t.Error("PgAdminURL not loaded")
				}
			},
		},
		{
			name:    "link with user information rejected",
			mutate:  map[string]string{EnvMonitoringURL: "https://user:pass@grafana.example.com"},
			wantErr: EnvMonitoringURL + ": must not contain user information",
		},
		{
			name:    "bare host rejected",
			mutate:  map[string]string{EnvMonitoringURL: "grafana.example.com"},
			wantErr: EnvMonitoringURL + ": must be an absolute URL or a root-relative path",
		},
		{
			name:   "root-relative path accepted",
			mutate: map[string]string{EnvPgAdminURL: "/pgadmin"},
			check: func(t *testing.T, cfg Config) {
				if cfg.PgAdminURL != "/pgadmin" {
					t.Errorf("PgAdminURL = %q, want /pgadmin", cfg.PgAdminURL)
				}
			},
		},
		{
			name:   "root-relative path keeps its query and trailing slash",
			mutate: map[string]string{EnvMonitoringURL: "/grafana/d/abc/?orgId=1"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MonitoringURL != "/grafana/d/abc/?orgId=1" {
					t.Errorf("MonitoringURL = %q, want it preserved verbatim", cfg.MonitoringURL)
				}
			},
		},
		{
			// A relative reference inherits the reader's scheme, so it
			// cannot downgrade and the insecure-link flag does not apply.
			name:   "root-relative path needs no insecure-link flag",
			mutate: map[string]string{EnvPgAdminURL: "/pgadmin", EnvAllowInsecureLinks: "false"},
			check: func(t *testing.T, cfg Config) {
				if cfg.PgAdminURL != "/pgadmin" {
					t.Errorf("PgAdminURL = %q, want /pgadmin", cfg.PgAdminURL)
				}
			},
		},
		{
			// Go parses this as a relative reference carrying an
			// authority, so "starts with a slash" is not "same origin".
			name:    "protocol-relative link rejected",
			mutate:  map[string]string{EnvPgAdminURL: "//evil.example.com/pgadmin"},
			wantErr: EnvPgAdminURL + `: must be a root-relative path such as "/pgadmin", not protocol-relative`,
		},
		{
			// Browsers normalise the backslash, making this the same
			// escape spelled differently.
			name:    "backslash protocol-relative link rejected",
			mutate:  map[string]string{EnvPgAdminURL: `/\evil.example.com`},
			wantErr: EnvPgAdminURL + `: must be a root-relative path such as "/pgadmin", not protocol-relative`,
		},
		{
			name:    "root-relative path with a space rejected",
			mutate:  map[string]string{EnvPgAdminURL: "/pg admin"},
			wantErr: EnvPgAdminURL + ": must not contain spaces or control characters; percent-encode them",
		},
		{
			name:    "root-relative path with a control character rejected",
			mutate:  map[string]string{EnvPgAdminURL: "/pgadmin\nSet-Cookie: x=1"},
			wantErr: EnvPgAdminURL + ": must not contain spaces or control characters; percent-encode them",
		},
		{
			name:    "ftp link rejected",
			mutate:  map[string]string{EnvMonitoringURL: "ftp://grafana.example.com"},
			wantErr: EnvMonitoringURL + ": must use the https scheme",
		},
		{
			name:   "level header defaulted",
			mutate: map[string]string{},
			check: func(t *testing.T, cfg Config) {
				if cfg.TrustedLevelHeader != DefaultTrustedLevelHeader {
					t.Errorf("TrustedLevelHeader = %q, want default", cfg.TrustedLevelHeader)
				}
			},
		},
		{
			name:   "level header overridden",
			mutate: map[string]string{EnvTrustedLevelHeader: "X-Console-Level"},
			check: func(t *testing.T, cfg Config) {
				if cfg.TrustedLevelHeader != "X-Console-Level" {
					t.Errorf("TrustedLevelHeader = %q", cfg.TrustedLevelHeader)
				}
			},
		},
		{
			name:   "level header explicitly disabled",
			mutate: map[string]string{EnvTrustedLevelHeader: ""},
			check: func(t *testing.T, cfg Config) {
				if cfg.TrustedLevelHeader != "" {
					t.Errorf("TrustedLevelHeader = %q, want empty", cfg.TrustedLevelHeader)
				}
			},
		},
		{
			name:    "invalid level header",
			mutate:  map[string]string{EnvTrustedLevelHeader: "X PgToolBox Level"},
			wantErr: EnvTrustedLevelHeader + ": must be a valid HTTP header name",
		},
		{
			name:   "evidence complete set accepted",
			mutate: evidenceEnv(map[string]string{EnvRepositoryEvidenceURL: "unix:///var/run/objectstoreviewer/evidence.sock"}),
			check: func(t *testing.T, cfg Config) {
				if !cfg.RepositoryEvidenceEnabled() {
					t.Error("repository evidence not enabled")
				}
				if cfg.RepositoryEvidenceTokenFile == "" || cfg.RepositoryExpectedFingerprint == "" || cfg.RepositoryBarmanServer == "" {
					t.Error("companion evidence variables not loaded")
				}
			},
		},
		{
			name:   "evidence socket path accepted",
			mutate: evidenceEnv(map[string]string{EnvRepositoryEvidenceURL: "/var/run/objectstoreviewer/evidence.sock"}),
			check: func(t *testing.T, cfg Config) {
				if cfg.RepositoryEvidenceURL == "" {
					t.Error("RepositoryEvidenceURL not loaded")
				}
			},
		},
		{
			name:    "evidence url alone rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "unix:///var/run/objectstoreviewer/evidence.sock"},
			wantErr: EnvRepositoryEvidenceTokenFile + ": is required when repository evidence is configured",
		},
		{
			name:    "evidence token file alone rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceTokenFile: "/var/run/objectstoreviewer/token"},
			wantErr: EnvRepositoryEvidenceURL + ": is required when repository evidence is configured",
		},
		{
			name:    "evidence fingerprint alone rejected",
			mutate:  map[string]string{EnvRepositoryExpectedFingerprint: "sha256:" + strings.Repeat("ab", 32)},
			wantErr: EnvRepositoryBarmanServer + ": is required when repository evidence is configured",
		},
		{
			name:    "evidence relative token file rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryEvidenceTokenFile: "run/token"}),
			wantErr: EnvRepositoryEvidenceTokenFile + ": must be an absolute file path",
		},
		{
			name:    "evidence fingerprint without prefix rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryExpectedFingerprint: strings.Repeat("ab", 32)}),
			wantErr: EnvRepositoryExpectedFingerprint + `: must be "sha256:" plus 64 lowercase hex characters`,
		},
		{
			name:    "evidence fingerprint uppercase rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryExpectedFingerprint: "sha256:" + strings.Repeat("AB", 32)}),
			wantErr: EnvRepositoryExpectedFingerprint + `: must be "sha256:" plus 64 lowercase hex characters`,
		},
		{
			name:    "evidence fingerprint short digest rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryExpectedFingerprint: "sha256:" + strings.Repeat("ab", 31)}),
			wantErr: EnvRepositoryExpectedFingerprint + `: must be "sha256:" plus 64 lowercase hex characters`,
		},
		{
			name:    "evidence barman server with slash rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryBarmanServer: "srv/../other"}),
			wantErr: EnvRepositoryBarmanServer + ": must be a bounded name without path separators or control characters",
		},
		{
			name:    "evidence barman server dot rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryBarmanServer: ".."}),
			wantErr: EnvRepositoryBarmanServer + ": must be a bounded name without path separators or control characters",
		},
		{
			name:    "evidence barman server overlong rejected",
			mutate:  evidenceEnv(map[string]string{EnvRepositoryBarmanServer: strings.Repeat("a", 257)}),
			wantErr: EnvRepositoryBarmanServer + ": must be a bounded name without path separators or control characters",
		},
		{
			name:    "evidence loopback url rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "http://127.0.0.1:9090"},
			wantErr: EnvRepositoryEvidenceURL + ": must be a unix:// socket URI or an absolute socket path",
		},
		{
			name:    "evidence service url rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "http://viewer.payments.svc:9090"},
			wantErr: EnvRepositoryEvidenceURL + ": must be a unix:// socket URI or an absolute socket path",
		},
		{
			name:    "evidence https url rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "https://127.0.0.1:9090"},
			wantErr: EnvRepositoryEvidenceURL + ": must be a unix:// socket URI or an absolute socket path",
		},
		{
			name:    "evidence bare slash rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "/"},
			wantErr: EnvRepositoryEvidenceURL + ": must be a unix:// socket URI or an absolute socket path",
		},
		{
			name:    "evidence relative uri rejected",
			mutate:  map[string]string{EnvRepositoryEvidenceURL: "unix://relative.sock"},
			wantErr: EnvRepositoryEvidenceURL + ": must be a unix:// socket URI or an absolute socket path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := base()
			for k, v := range tc.mutate {
				env[k] = v
			}
			cfg, err := Load(mapLookup(env))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("Load succeeded, want error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestLoadReportsEveryViolationAtOnce(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		EnvClusterName:  "Bad_Name",
		EnvLogTailLines: "0",
		EnvAllowLogs:    "maybe",
	}
	_, err := Load(mapLookup(env))
	if err == nil {
		t.Fatal("Load succeeded with three violations")
	}
	for _, want := range []string{EnvClusterName, EnvNamespace, EnvLogTailLines, EnvAllowLogs} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %q", want, err.Error())
		}
	}
}

// TestLoadRedactsValues proves the P-CONFIG redaction rule: no supplied
// value, however malformed, appears in the error output.
func TestLoadRedactsValues(t *testing.T) {
	t.Parallel()
	const canary = "sekret-canary-value"
	env := map[string]string{
		EnvClusterName:           canary + "_",
		EnvNamespace:             "_" + canary,
		EnvListenAddr:            canary,
		EnvTrustedUserHeader:     canary + " " + canary,
		EnvAllowOperations:       canary,
		EnvAllowLogs:             canary,
		EnvLogTailLines:          canary,
		EnvLogTailMaxBytes:       canary,
		EnvEventsMaxAge:          canary,
		EnvAPIRequestTimeout:     canary,
		EnvObjectStoreViewerURL:  "https://user:" + canary + "@host",
		EnvPgAdminURL:            "ftp://" + canary,
		EnvMonitoringURL:         canary,
		EnvAllowInsecureLinks:    canary,
		EnvRepositoryEvidenceURL: "https://" + canary,
	}
	_, err := Load(mapLookup(env))
	if err == nil {
		t.Fatal("Load succeeded with a fully malformed environment")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("error output leaks a configured value: %q", err.Error())
	}
	if got := strings.Count(err.Error(), "\n") + 1; got < 10 {
		t.Errorf("expected one line per violation, got %d lines", got)
	}
}

func TestVarErrorMessageShape(t *testing.T) {
	t.Parallel()
	e := &VarError{Name: "SOME_VAR", Constraint: "is required"}
	if e.Error() != "SOME_VAR: is required" {
		t.Fatalf("Error() = %q", e.Error())
	}
}

func TestBoundsAreTheDocumentedContract(t *testing.T) {
	t.Parallel()
	if MinEventsMaxAge != time.Minute || MaxEventsMaxAge != 24*time.Hour {
		t.Error("events age bounds diverge from the documented contract")
	}
	if MinAPIRequestTimeout != time.Second || MaxAPIRequestTimeout != time.Minute {
		t.Error("api timeout bounds diverge from the documented contract")
	}
	if MinLogTailLines != 1 || MaxLogTailLines != 2000 {
		t.Error("log tail line bounds diverge from the documented contract")
	}
	if MinLogTailMaxBytes != 4096 || MaxLogTailMaxBytes != 8*1024*1024 {
		t.Error("log tail byte bounds diverge from the documented contract")
	}
}

// TestLogStreamingCrossValidation proves the documented dependencies are
// enforced rather than merely written down: following with the tail off,
// or retaining lines with nothing following, is conflicting intent and
// fails startup instead of silently doing nothing.
func TestLogStreamingCrossValidation(t *testing.T) {
	t.Parallel()
	base := map[string]string{EnvClusterName: "orders", EnvNamespace: "payments"}
	with := func(extra map[string]string) Lookup {
		merged := map[string]string{}
		for k, v := range base {
			merged[k] = v
		}
		for k, v := range extra {
			merged[k] = v
		}
		return func(name string) (string, bool) { v, ok := merged[name]; return v, ok }
	}

	cases := map[string]struct {
		env  map[string]string
		want string
	}{
		"following with the tail off": {
			env:  map[string]string{EnvLogStreamEnabled: "true", EnvAllowLogs: "false"},
			want: EnvLogStreamEnabled,
		},
		"retaining with nothing following": {
			env:  map[string]string{EnvLogBufferBytes: "1048576"},
			want: EnvLogBufferBytes,
		},
		"per-container bound above the total": {
			env: map[string]string{
				EnvLogStreamEnabled: "true", EnvLogBufferBytes: "8388608",
				EnvLogBufferTotalBytes: "1048576",
			},
			want: EnvLogBufferBytes,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(with(tc.env)); err == nil {
				t.Fatal("conflicting configuration was accepted")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %s", err, tc.want)
			}
		})
	}
}

// TestLogRetentionDefaultsToNothing proves the default posture: the
// console follows nothing and retains no log text unless asked.
func TestLogRetentionDefaultsToNothing(t *testing.T) {
	t.Parallel()
	cfg, err := Load(func(name string) (string, bool) {
		switch name {
		case EnvClusterName:
			return "orders", true
		case EnvNamespace:
			return "payments", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogStreamEnabled {
		t.Error("log streaming is on by default")
	}
	if cfg.LogBufferBytes != 0 {
		t.Errorf("LogBufferBytes = %d by default, want 0", cfg.LogBufferBytes)
	}
}

// TestLogFollowingFollowsTheScreenThatReadsIt pins the derived default.
// The continuous matcher exists to feed the diagnostics screen, so it
// comes on where that screen is on and the tail is permitted, and stays
// off where either is missing — following logs nobody may read, or
// matching them for a screen nobody can open, is cost without a reader.
func TestLogFollowingFollowsTheScreenThatReadsIt(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"diagnostics on, tail permitted": {
			map[string]string{EnvAllowDiagnostics: "true"}, true},
		"diagnostics off": {
			map[string]string{}, false},
		"tail switched off": {
			map[string]string{EnvAllowDiagnostics: "true", EnvAllowLogs: "false"}, false},
		"asked for explicitly against the default": {
			map[string]string{EnvAllowDiagnostics: "true", EnvLogStreamEnabled: "false"}, false},
	} {
		env := map[string]string{EnvClusterName: "orders", EnvNamespace: "payments"}
		for k, v := range tc.env {
			env[k] = v
		}
		cfg, err := Load(func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		})
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if cfg.LogStreamEnabled != tc.want {
			t.Errorf("%s: LogStreamEnabled = %v, want %v", name, cfg.LogStreamEnabled, tc.want)
		}
	}
}

// TestTheTailSwitchedOffStillStartsWithDiagnosticsOn is the reason the
// default is derived rather than fixed at true. Following requires the
// tail's permission, and a fixed default would have turned a working
// ALLOW_LOGS=false deployment into one that refuses to start on upgrade.
func TestTheTailSwitchedOffStillStartsWithDiagnosticsOn(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		EnvClusterName: "orders", EnvNamespace: "payments",
		EnvAllowDiagnostics: "true", EnvAllowLogs: "false",
	}
	if _, err := Load(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}); err != nil {
		t.Fatalf("a deployment with the tail switched off no longer starts: %v", err)
	}
}
