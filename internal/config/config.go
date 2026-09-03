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

// Package config owns the environment configuration contract: parsing,
// defaulting, and total validation before the application listens. It is
// the only package that reads the process environment. Validation errors
// name the offending variable and the constraint, and never echo the
// supplied value.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Variable names of the configuration contract.
const (
	// EnvClusterName names the one target CloudNativePG Cluster.
	EnvClusterName = "CLUSTER_NAME"
	// EnvNamespace names the namespace of the target cluster.
	EnvNamespace = "NAMESPACE"
	// EnvListenAddr is the plain HTTP listen address.
	EnvListenAddr = "LISTEN_ADDR"
	// EnvTrustedUserHeader is the proxy identity header, used for display
	// and audit attribution only.
	EnvTrustedUserHeader = "TRUSTED_USER_HEADER"
	// EnvTrustedLevelHeader is the proxy authorization-level header. The
	// trusted proxy asserts the console level (view, poweruser, dba); the
	// deployment's NetworkPolicy confining ingress to the proxy is what
	// makes it trustworthy. The console performs no capability probing.
	EnvTrustedLevelHeader = "TRUSTED_LEVEL_HEADER"
	// EnvAllowOperations enables the enumerated day-2 operation routes.
	EnvAllowOperations = "ALLOW_OPERATIONS"
	// EnvAllowClusterCatalogs lets the console read the one
	// cluster-scoped ClusterImageCatalog its Cluster references. It is
	// the only capability that needs authority outside the namespace, so
	// it is opt-in, needs its own ClusterRole, and degrades to unknown
	// when the flag is set but the binding is absent.
	EnvAllowClusterCatalogs = "ALLOW_CLUSTER_CATALOGS"
	// EnvAllowAccessReview enables the dba access-request review panel.
	EnvAllowAccessReview = "ALLOW_ACCESS_REVIEW"
	// EnvAllowDiagnostics enables the diagnostics screen.
	EnvAllowDiagnostics = "ALLOW_DIAGNOSTICS"
	// EnvLogStreamEnabled follows the member containers' logs
	// continuously so the diagnostics matcher can analyse each line as
	// it arrives. The matcher keeps only what matched, so this retains
	// no log text on its own.
	EnvLogStreamEnabled = "LOG_STREAM_ENABLED"
	// EnvLogBufferBytes retains recent log lines per container so the
	// screens can show what happened before the reader arrived, and so
	// a container that has died still has an account of itself.
	//
	// Zero — the default — retains nothing. A non-zero value is a
	// deliberate exposure decision: PostgreSQL logs can contain
	// statement text and its literal values, so this holds a standing
	// corpus of it in memory.
	EnvLogBufferBytes = "LOG_BUFFER_BYTES"
	// EnvLogBufferTotalBytes caps the buffer across every container.
	EnvLogBufferTotalBytes = "LOG_BUFFER_TOTAL_BYTES"
	// EnvLogBufferMaxAge drops retained lines older than this.
	EnvLogBufferMaxAge = "LOG_BUFFER_MAX_AGE"
	// EnvLogMatchMaxAge expires a diagnostics log observation this long
	// after its last matching line.
	EnvLogMatchMaxAge = "LOG_MATCH_MAX_AGE"
	// EnvAllowLogs enables the bounded instance log tail.
	EnvAllowLogs = "ALLOW_LOGS"
	// EnvLogTailLines bounds the lines returned per log request.
	EnvLogTailLines = "LOG_TAIL_LINES"
	// EnvLogTailMaxBytes bounds the bytes returned per log request.
	EnvLogTailMaxBytes = "LOG_TAIL_MAX_BYTES"
	// EnvEventsMaxAge is the age window for the rendered event list.
	EnvEventsMaxAge = "EVENTS_MAX_AGE"
	// EnvAPIRequestTimeout bounds an individual Kubernetes API request.
	EnvAPIRequestTimeout = "API_REQUEST_TIMEOUT"
	// EnvHistoryEnabled enables the bounded in-memory revision history of
	// the watched object definitions.
	EnvHistoryEnabled = "HISTORY_ENABLED"
	// EnvHistoryMaxRevisions bounds the retained revisions across all
	// objects.
	EnvHistoryMaxRevisions = "HISTORY_MAX_REVISIONS"
	// EnvHistoryMaxBytes bounds the summed stored manifest bytes.
	EnvHistoryMaxBytes = "HISTORY_MAX_BYTES"
	// EnvHistoryPerObjectRevisions bounds the retained revisions of one
	// object.
	EnvHistoryPerObjectRevisions = "HISTORY_PER_OBJECT_REVISIONS"
	// EnvHistoryCoalesceWindow folds consecutive status transitions of
	// one object closer together than this into a single revision.
	EnvHistoryCoalesceWindow = "HISTORY_COALESCE_WINDOW"
	// EnvHistoryPath is the journal file the history is mirrored into so
	// it survives restarts. Empty keeps history in memory only — the
	// default, which preserves the stateless deployment. Setting it
	// implies a writable volume and a single replica, and requires
	// history to be enabled.
	EnvHistoryPath = "HISTORY_PATH"
	// EnvMetricsEnabled enables the bounded in-memory metrics window
	// scraped from the instance pods' metrics endpoints.
	EnvMetricsEnabled = "METRICS_ENABLED"
	// EnvMetricsInterval is the scrape cadence.
	EnvMetricsInterval = "METRICS_INTERVAL"
	// EnvMetricsRetention is how long the rollup tier is kept.
	EnvMetricsRetention = "METRICS_RETENTION"
	// EnvMetricsPath is the snapshot file the metrics window is
	// periodically written to so it survives restarts. Empty keeps the
	// window in memory only — the default, which preserves the
	// stateless deployment. Setting it implies a writable volume
	// (typically the history journal's) and requires metrics to be
	// enabled.
	EnvMetricsPath = "METRICS_PATH"
	// EnvObjectStoreViewerURL is the link-out base URL to the cluster's
	// ObjectStoreViewer.
	EnvObjectStoreViewerURL = "OBJECTSTOREVIEWER_URL"
	// EnvPgAdminURL is the link-out base URL to the cluster's pgAdmin.
	EnvPgAdminURL = "PGADMIN_URL"
	// EnvMonitoringURL is the link-out base URL to the operator's
	// dashboard for the cluster.
	EnvMonitoringURL = "MONITORING_URL"
	// EnvAllowInsecureLinks permits http link-out URLs, for lab use only.
	EnvAllowInsecureLinks = "ALLOW_INSECURE_LINKS"
	// EnvRepositoryEvidenceURL is the repository-evidence sidecar API
	// address: a unix:// socket URI or an absolute socket path, and
	// nothing else.
	EnvRepositoryEvidenceURL = "REPOSITORY_EVIDENCE_URL"
	// EnvRepositoryEvidenceTokenFile is the mounted pod-local bearer
	// token file of the evidence channel.
	EnvRepositoryEvidenceTokenFile = "REPOSITORY_EVIDENCE_TOKEN_FILE"
	// EnvRepositoryExpectedFingerprint is the operator-computed
	// destination fingerprint the evidence responses must carry.
	EnvRepositoryExpectedFingerprint = "REPOSITORY_EXPECTED_FINGERPRINT"
	// EnvRepositoryBarmanServer is the exact Barman server name of the
	// operator-supplied identity mapping.
	EnvRepositoryBarmanServer = "REPOSITORY_BARMAN_SERVER"
)

// Bounds of the numeric and duration variables.
const (
	// MinLogTailLines is the lowest accepted LOG_TAIL_LINES.
	MinLogTailLines = 1
	// MaxLogTailLines is the highest accepted LOG_TAIL_LINES.
	MaxLogTailLines = 2000
	// MinLogTailMaxBytes is the lowest accepted LOG_TAIL_MAX_BYTES.
	MinLogTailMaxBytes = 4 * 1024
	// MaxLogTailMaxBytes is the highest accepted LOG_TAIL_MAX_BYTES.
	MaxLogTailMaxBytes = 8 * 1024 * 1024
	// MinEventsMaxAge is the shortest accepted EVENTS_MAX_AGE.
	MinEventsMaxAge = time.Minute
	// MaxEventsMaxAge is the longest accepted EVENTS_MAX_AGE.
	MaxEventsMaxAge = 24 * time.Hour
	// MinAPIRequestTimeout is the shortest accepted API_REQUEST_TIMEOUT.
	MinAPIRequestTimeout = time.Second
	// MaxAPIRequestTimeout is the longest accepted API_REQUEST_TIMEOUT.
	MaxAPIRequestTimeout = time.Minute
	// MaxLogBufferBytes is the highest accepted LOG_BUFFER_BYTES per
	// container. The ceiling exists because retained log text shares the
	// process memory budget with every snapshot store.
	MaxLogBufferBytes = 8 * 1024 * 1024
	// DefaultLogBufferTotalBytes and MaxLogBufferTotalBytes bound the
	// buffer across every container.
	DefaultLogBufferTotalBytes = 32 * 1024 * 1024
	MaxLogBufferTotalBytes     = 128 * 1024 * 1024
	// The retained-line age bounds. Log value decays quickly: an old
	// line is a question for a log system, not for this console.
	DefaultLogBufferMaxAge = time.Hour
	MinLogBufferMaxAge     = time.Minute
	MaxLogBufferMaxAge     = 24 * time.Hour

	// The match window is wider than the buffer's by default: a finding
	// should outlive the verbatim text that raised it, but not by days.
	DefaultLogMatchMaxAge = 6 * time.Hour
	MinLogMatchMaxAge     = time.Minute
	MaxLogMatchMaxAge     = 7 * 24 * time.Hour
	// MinHistoryMaxRevisions is the lowest accepted HISTORY_MAX_REVISIONS.
	MinHistoryMaxRevisions = 100
	// MaxHistoryMaxRevisions is the highest accepted HISTORY_MAX_REVISIONS.
	MaxHistoryMaxRevisions = 20000
	// MinHistoryMaxBytes is the lowest accepted HISTORY_MAX_BYTES.
	MinHistoryMaxBytes = 1024 * 1024
	// MaxHistoryMaxBytes is the highest accepted HISTORY_MAX_BYTES. The
	// ceiling exists because history shares the process memory budget
	// with every snapshot store.
	MaxHistoryMaxBytes = 64 * 1024 * 1024
	// MinHistoryPerObjectRevisions is the lowest accepted
	// HISTORY_PER_OBJECT_REVISIONS.
	MinHistoryPerObjectRevisions = 2
	// MaxHistoryPerObjectRevisions is the highest accepted
	// HISTORY_PER_OBJECT_REVISIONS.
	MaxHistoryPerObjectRevisions = 200
	// MinHistoryCoalesceWindow is the shortest accepted
	// HISTORY_COALESCE_WINDOW.
	MinHistoryCoalesceWindow = time.Second
	// MaxHistoryCoalesceWindow is the longest accepted
	// HISTORY_COALESCE_WINDOW.
	MaxHistoryCoalesceWindow = time.Hour
	// MinMetricsInterval is the shortest accepted METRICS_INTERVAL: the
	// exporters refresh their own caches on the order of seconds, so a
	// faster sweep only rereads the same claims.
	MinMetricsInterval = 5 * time.Second
	// MaxMetricsInterval is the longest accepted METRICS_INTERVAL.
	MaxMetricsInterval = 5 * time.Minute
	// MinMetricsRetention is the shortest accepted METRICS_RETENTION.
	MinMetricsRetention = time.Hour
	// MaxMetricsRetention is the longest accepted METRICS_RETENTION; it
	// bounds the rollup ring the window is stored in.
	MaxMetricsRetention = 30 * 24 * time.Hour
)

// Defaults of the optional variables.
const (
	// DefaultListenAddr is the listen address applied when LISTEN_ADDR is
	// unset.
	DefaultListenAddr = ":3000"
	// DefaultTrustedUserHeader is the identity header applied when
	// TRUSTED_USER_HEADER is unset. An explicitly empty value disables
	// identity display.
	DefaultTrustedUserHeader = "X-Forwarded-User"
	// DefaultTrustedLevelHeader is the authorization-level header applied
	// when TRUSTED_LEVEL_HEADER is unset. An explicitly empty value
	// disables level gating: no route above the read-only baseline can be
	// reached, because no level is ever asserted.
	DefaultTrustedLevelHeader = "X-PgToolBox-Level"
	// DefaultLogTailLines is applied when LOG_TAIL_LINES is unset.
	DefaultLogTailLines = 200
	// DefaultLogTailMaxBytes is applied when LOG_TAIL_MAX_BYTES is unset.
	DefaultLogTailMaxBytes = 1024 * 1024
	// DefaultEventsMaxAge is applied when EVENTS_MAX_AGE is unset.
	DefaultEventsMaxAge = time.Hour
	// DefaultAPIRequestTimeout is applied when API_REQUEST_TIMEOUT is
	// unset.
	DefaultAPIRequestTimeout = 10 * time.Second
	// DefaultHistoryMaxRevisions is applied when HISTORY_MAX_REVISIONS is
	// unset.
	DefaultHistoryMaxRevisions = 2000
	// DefaultHistoryMaxBytes is applied when HISTORY_MAX_BYTES is unset.
	DefaultHistoryMaxBytes = 16 * 1024 * 1024
	// DefaultHistoryPerObjectRevisions is applied when
	// HISTORY_PER_OBJECT_REVISIONS is unset.
	DefaultHistoryPerObjectRevisions = 20
	// DefaultHistoryCoalesceWindow is applied when
	// HISTORY_COALESCE_WINDOW is unset.
	DefaultHistoryCoalesceWindow = time.Minute
	// DefaultMetricsInterval is applied when METRICS_INTERVAL is unset.
	DefaultMetricsInterval = 10 * time.Second
	// DefaultMetricsRetention is applied when METRICS_RETENTION is unset.
	DefaultMetricsRetention = 7 * 24 * time.Hour
)

// Config is the validated runtime configuration. A Config only exists in
// a valid state: Load returns either a fully validated value or an error.
type Config struct {
	// ClusterName is the one target CloudNativePG Cluster.
	ClusterName string
	// Namespace is the namespace of the target cluster.
	Namespace string
	// ListenAddr is the plain HTTP listen address.
	ListenAddr string
	// TrustedUserHeader is the proxy identity header; empty disables
	// identity display and audit attribution.
	TrustedUserHeader string
	// TrustedLevelHeader is the proxy authorization-level header. Every
	// screen is decided by the level it carries, so setting it empty
	// does not open the console — it closes it: with no level to read,
	// no request is admitted to anything but the denial page and the
	// readiness endpoints.
	TrustedLevelHeader string
	// AllowOperations enables the enumerated day-2 operation routes.
	AllowOperations bool
	// AllowClusterCatalogs enables the cluster-scoped catalog read.
	AllowClusterCatalogs bool
	// LogStreamEnabled follows container logs continuously for the
	// diagnostics matcher. It retains no log text by itself.
	LogStreamEnabled bool
	// LogBufferBytes retains recent lines per container, 0 for none.
	LogBufferBytes int
	// LogBufferTotalBytes caps retention across all containers.
	LogBufferTotalBytes int
	// LogBufferMaxAge drops retained lines older than this.
	LogBufferMaxAge time.Duration
	// LogMatchMaxAge expires a diagnostics log observation this long
	// after its last matching line, so a finding cannot outlive its
	// relevance.
	LogMatchMaxAge time.Duration
	// AllowDiagnostics enables the diagnostics screen, which correlates
	// facts the other screens already carry into findings. It grants no
	// authority: every detector reads snapshots that exist regardless.
	AllowDiagnostics bool
	// AllowAccessReview enables the dba access-request review panel.
	AllowAccessReview bool
	// AllowLogs enables the bounded instance log tail.
	AllowLogs bool
	// LogTailLines bounds the lines returned per log request.
	LogTailLines int
	// LogTailMaxBytes bounds the bytes returned per log request.
	LogTailMaxBytes int64
	// EventsMaxAge is the age window for the rendered event list.
	EventsMaxAge time.Duration
	// APIRequestTimeout bounds an individual Kubernetes API request.
	APIRequestTimeout time.Duration
	// HistoryEnabled enables the bounded in-memory revision history of
	// the watched object definitions.
	HistoryEnabled bool
	// HistoryMaxRevisions bounds the retained revisions across all
	// objects.
	HistoryMaxRevisions int
	// HistoryMaxBytes bounds the summed stored manifest bytes.
	HistoryMaxBytes int
	// HistoryPerObjectRevisions bounds the retained revisions of one
	// object.
	HistoryPerObjectRevisions int
	// HistoryCoalesceWindow folds consecutive status transitions of one
	// object closer together than this into a single revision.
	HistoryCoalesceWindow time.Duration
	// HistoryPath is the durable journal file; empty keeps history in
	// memory only.
	HistoryPath string
	// MetricsEnabled enables the bounded in-memory metrics window.
	MetricsEnabled bool
	// MetricsInterval is the scrape cadence.
	MetricsInterval time.Duration
	// MetricsRetention is how long the rollup tier is kept.
	MetricsRetention time.Duration
	// MetricsPath is the snapshot file; empty keeps the window in
	// memory only.
	MetricsPath string
	// ObjectStoreViewerURL is the ObjectStoreViewer link-out base URL;
	// empty hides the link.
	ObjectStoreViewerURL string
	// PgAdminURL is the pgAdmin link-out base URL; empty hides the link.
	PgAdminURL string
	// MonitoringURL is the monitoring link-out base URL; empty hides the
	// link.
	MonitoringURL string
	// AllowInsecureLinks permits http link-out URLs.
	AllowInsecureLinks bool
	// RepositoryEvidenceURL is the evidence sidecar Unix socket address;
	// empty disables the evidence panels.
	RepositoryEvidenceURL string
	// RepositoryEvidenceTokenFile is the mounted pod-local bearer token
	// file path of the evidence channel.
	RepositoryEvidenceTokenFile string
	// RepositoryExpectedFingerprint is the operator-computed sha256
	// destination fingerprint evidence responses must carry.
	RepositoryExpectedFingerprint string
	// RepositoryBarmanServer is the exact Barman server name of the
	// operator-supplied identity mapping.
	RepositoryBarmanServer string
}

// RepositoryEvidenceEnabled reports whether the repository-evidence
// consumer is configured. The four evidence variables validate
// all-or-nothing, so a true result implies every one of them is set.
func (c Config) RepositoryEvidenceEnabled() bool {
	return c.RepositoryEvidenceURL != ""
}

// Lookup reports the value of one environment variable and whether it was
// set, mirroring os.LookupEnv so tests can substitute a map.
type Lookup func(name string) (value string, ok bool)

// VarError reports that one variable violated one constraint. Its message
// contains the variable name and the constraint, never the supplied
// value.
type VarError struct {
	// Name is the environment variable name.
	Name string
	// Constraint states the violated rule without echoing the value.
	Constraint string
}

// Error returns "NAME: constraint".
func (e *VarError) Error() string {
	return e.Name + ": " + e.Constraint
}

// dns1123Label matches a lowercase RFC 1123 label.
var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// headerName matches a conventional HTTP header field name.
var headerName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// Load reads the whole contract through lookup and returns a validated
// Config, or an error joining one VarError per violated constraint.
// Validation is total: every variable is checked even after the first
// failure, so an operator sees all problems at once.
func Load(lookup Lookup) (Config, error) {
	var errs []error
	fail := func(name, constraint string) {
		errs = append(errs, &VarError{Name: name, Constraint: constraint})
	}

	cfg := Config{
		ListenAddr:                DefaultListenAddr,
		TrustedUserHeader:         DefaultTrustedUserHeader,
		TrustedLevelHeader:        DefaultTrustedLevelHeader,
		AllowLogs:                 true,
		LogTailLines:              DefaultLogTailLines,
		LogTailMaxBytes:           DefaultLogTailMaxBytes,
		EventsMaxAge:              DefaultEventsMaxAge,
		APIRequestTimeout:         DefaultAPIRequestTimeout,
		HistoryEnabled:            true,
		HistoryMaxRevisions:       DefaultHistoryMaxRevisions,
		HistoryMaxBytes:           DefaultHistoryMaxBytes,
		HistoryPerObjectRevisions: DefaultHistoryPerObjectRevisions,
		HistoryCoalesceWindow:     DefaultHistoryCoalesceWindow,
		MetricsEnabled:            true,
		MetricsInterval:           DefaultMetricsInterval,
		MetricsRetention:          DefaultMetricsRetention,
	}

	cfg.ClusterName = requiredLabel(lookup, EnvClusterName, fail)
	cfg.Namespace = requiredLabel(lookup, EnvNamespace, fail)

	if raw, ok := lookup(EnvListenAddr); ok {
		if err := validateListenAddr(raw); err != nil {
			fail(EnvListenAddr, err.Error())
		} else {
			cfg.ListenAddr = raw
		}
	}

	if raw, ok := lookup(EnvTrustedUserHeader); ok {
		switch {
		case raw == "":
			cfg.TrustedUserHeader = ""
		case !headerName.MatchString(raw):
			fail(EnvTrustedUserHeader, "must be a valid HTTP header name")
		default:
			cfg.TrustedUserHeader = raw
		}
	}
	if raw, ok := lookup(EnvTrustedLevelHeader); ok {
		switch {
		case raw == "":
			cfg.TrustedLevelHeader = ""
		case !headerName.MatchString(raw):
			fail(EnvTrustedLevelHeader, "must be a valid HTTP header name")
		default:
			cfg.TrustedLevelHeader = raw
		}
	}

	cfg.AllowOperations = boolVar(lookup, EnvAllowOperations, false, fail)
	cfg.AllowClusterCatalogs = boolVar(lookup, EnvAllowClusterCatalogs, false, fail)
	cfg.AllowAccessReview = boolVar(lookup, EnvAllowAccessReview, false, fail)
	cfg.AllowDiagnostics = boolVar(lookup, EnvAllowDiagnostics, false, fail)
	cfg.AllowLogs = boolVar(lookup, EnvAllowLogs, true, fail)
	// Following defaults to on wherever it would be read: the continuous
	// matcher exists to feed the diagnostics screen, and the log-backed
	// checks are the ones that quote the failure in the server's own
	// words rather than inferring it from a phase — the largest group in
	// the catalog, and mostly critical. A deployment that asked for
	// diagnostics and left this alone was getting a screen with those
	// checks dark.
	//
	// No count is named here on purpose: the catalog grows, and a number
	// written into a comment goes quietly wrong with nothing to catch
	// it. What holds is the property, not the arithmetic.
	//
	// It is derived rather than fixed at true so the two ways that would
	// be wrong cannot happen. With the tail switched off there is no
	// permission to follow anything, and a fixed default would turn a
	// working ALLOW_LOGS=false deployment into one that refuses to
	// start. With diagnostics off nothing reads the matcher, so
	// following would be cost with no reader. Setting it explicitly
	// still wins either way, including the explicit conflict below.
	cfg.LogStreamEnabled = boolVar(lookup, EnvLogStreamEnabled, cfg.AllowLogs && cfg.AllowDiagnostics, fail)
	cfg.LogBufferBytes = intVar(lookup, EnvLogBufferBytes, 0, 0, MaxLogBufferBytes, fail)
	cfg.LogBufferTotalBytes = intVar(lookup, EnvLogBufferTotalBytes,
		DefaultLogBufferTotalBytes, 0, MaxLogBufferTotalBytes, fail)
	cfg.LogBufferMaxAge = durationVar(lookup, EnvLogBufferMaxAge,
		DefaultLogBufferMaxAge, MinLogBufferMaxAge, MaxLogBufferMaxAge, fail)
	cfg.LogMatchMaxAge = durationVar(lookup, EnvLogMatchMaxAge,
		DefaultLogMatchMaxAge, MinLogMatchMaxAge, MaxLogMatchMaxAge, fail)
	cfg.AllowInsecureLinks = boolVar(lookup, EnvAllowInsecureLinks, false, fail)

	cfg.LogTailLines = intVar(lookup, EnvLogTailLines, DefaultLogTailLines, MinLogTailLines, MaxLogTailLines, fail)
	cfg.LogTailMaxBytes = int64(intVar(lookup, EnvLogTailMaxBytes, DefaultLogTailMaxBytes, MinLogTailMaxBytes, MaxLogTailMaxBytes, fail))
	cfg.EventsMaxAge = durationVar(lookup, EnvEventsMaxAge, DefaultEventsMaxAge, MinEventsMaxAge, MaxEventsMaxAge, fail)
	cfg.APIRequestTimeout = durationVar(lookup, EnvAPIRequestTimeout, DefaultAPIRequestTimeout, MinAPIRequestTimeout, MaxAPIRequestTimeout, fail)

	cfg.HistoryEnabled = boolVar(lookup, EnvHistoryEnabled, true, fail)
	cfg.HistoryMaxRevisions = intVar(lookup, EnvHistoryMaxRevisions, DefaultHistoryMaxRevisions, MinHistoryMaxRevisions, MaxHistoryMaxRevisions, fail)
	cfg.HistoryMaxBytes = intVar(lookup, EnvHistoryMaxBytes, DefaultHistoryMaxBytes, MinHistoryMaxBytes, MaxHistoryMaxBytes, fail)
	cfg.HistoryPerObjectRevisions = intVar(lookup, EnvHistoryPerObjectRevisions, DefaultHistoryPerObjectRevisions, MinHistoryPerObjectRevisions, MaxHistoryPerObjectRevisions, fail)
	cfg.HistoryCoalesceWindow = durationVar(lookup, EnvHistoryCoalesceWindow, DefaultHistoryCoalesceWindow, MinHistoryCoalesceWindow, MaxHistoryCoalesceWindow, fail)
	if raw, ok := lookup(EnvHistoryPath); ok && raw != "" {
		switch {
		case !strings.HasPrefix(raw, "/") || len(raw) < 2 || strings.ContainsAny(raw, "\x00\r\n"):
			fail(EnvHistoryPath, "must be an absolute file path")
		case !cfg.HistoryEnabled:
			// A mounted journal with history switched off is conflicting
			// intent, not a silent no-op.
			fail(EnvHistoryPath, "requires "+EnvHistoryEnabled+"=true")
		default:
			cfg.HistoryPath = raw
		}
	}

	// Following logs with the tail switched off, or retaining lines with
	// nothing following them, is conflicting intent rather than a silent
	// no-op — the same reading the history and metrics paths get below.
	if cfg.LogStreamEnabled && !cfg.AllowLogs {
		fail(EnvLogStreamEnabled, "requires "+EnvAllowLogs+"=true")
	}
	if cfg.LogBufferBytes > 0 && !cfg.LogStreamEnabled {
		fail(EnvLogBufferBytes, "requires "+EnvLogStreamEnabled+"=true")
	}
	if cfg.LogBufferBytes > cfg.LogBufferTotalBytes {
		fail(EnvLogBufferBytes, "must not exceed "+EnvLogBufferTotalBytes)
	}

	cfg.MetricsEnabled = boolVar(lookup, EnvMetricsEnabled, true, fail)
	cfg.MetricsInterval = durationVar(lookup, EnvMetricsInterval, DefaultMetricsInterval, MinMetricsInterval, MaxMetricsInterval, fail)
	cfg.MetricsRetention = durationVar(lookup, EnvMetricsRetention, DefaultMetricsRetention, MinMetricsRetention, MaxMetricsRetention, fail)
	if raw, ok := lookup(EnvMetricsPath); ok && raw != "" {
		switch {
		case !strings.HasPrefix(raw, "/") || len(raw) < 2 || strings.ContainsAny(raw, "\x00\r\n"):
			fail(EnvMetricsPath, "must be an absolute file path")
		case !cfg.MetricsEnabled:
			// A mounted snapshot with metrics switched off is conflicting
			// intent, not a silent no-op.
			fail(EnvMetricsPath, "requires "+EnvMetricsEnabled+"=true")
		default:
			cfg.MetricsPath = raw
		}
	}

	cfg.ObjectStoreViewerURL = linkVar(lookup, EnvObjectStoreViewerURL, cfg.AllowInsecureLinks, fail)
	cfg.PgAdminURL = linkVar(lookup, EnvPgAdminURL, cfg.AllowInsecureLinks, fail)
	cfg.MonitoringURL = linkVar(lookup, EnvMonitoringURL, cfg.AllowInsecureLinks, fail)

	loadRepositoryEvidence(lookup, &cfg, fail)

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// requiredLabel reads a mandatory DNS-1123 label variable.
func requiredLabel(lookup Lookup, name string, fail func(name, constraint string)) string {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		fail(name, "is required")
		return ""
	}
	if len(raw) > 63 || !dns1123Label.MatchString(raw) {
		fail(name, "must be a DNS-1123 label")
		return ""
	}
	return raw
}

// boolVar reads an optional strict boolean ("true" or "false").
func boolVar(lookup Lookup, name string, def bool, fail func(name, constraint string)) bool {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return def
	}
	switch raw {
	case "true":
		return true
	case "false":
		return false
	default:
		fail(name, `must be "true" or "false"`)
		return def
	}
}

// intVar reads an optional bounded integer.
func intVar(lookup Lookup, name string, def, minimum, maximum int, fail func(name, constraint string)) int {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < minimum || v > maximum {
		fail(name, fmt.Sprintf("must be an integer between %d and %d", minimum, maximum))
		return def
	}
	return v
}

// durationVar reads an optional bounded Go duration.
func durationVar(lookup Lookup, name string, def, minimum, maximum time.Duration, fail func(name, constraint string)) time.Duration {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < minimum || v > maximum {
		fail(name, fmt.Sprintf("must be a duration between %s and %s", minimum, maximum))
		return def
	}
	return v
}

// linkVar reads an optional link-out target. Two shapes are accepted.
//
// An absolute URL must be https — or http only when insecure links are
// explicitly allowed — and never carries user information.
//
// A root-relative path such as "/pgadmin" names a sibling application
// served from the console's own origin, which is how a single Route or
// Ingress usually exposes the family. It is accepted without the scheme
// argument applying at all: a relative reference inherits whatever scheme
// the reader already has, so it cannot downgrade the way an http:// link
// can, and ALLOW_INSECURE_LINKS has nothing to say about it. Behind a
// proxy that terminates TLS and rewrites Host, it is also the more honest
// value, because the console cannot know its own external URL.
func linkVar(lookup Lookup, name string, allowInsecure bool, fail func(name, constraint string)) string {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		if err := validateRelativeLink(raw); err != nil {
			fail(name, err.Error())
			return ""
		}
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		fail(name, "must be an absolute URL or a root-relative path")
		return ""
	}
	if u.User != nil {
		fail(name, "must not contain user information")
		return ""
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecure {
			fail(name, "http scheme requires "+EnvAllowInsecureLinks+"=true")
			return ""
		}
	default:
		fail(name, "must use the https scheme")
		return ""
	}
	return raw
}

// validateRelativeLink accepts a root-relative path on the console's own
// origin and rejects everything that only looks like one.
//
// The trap it exists for is the protocol-relative form. Go parses
// "//evil.com/x" as a relative reference carrying an authority — Host is
// set, Scheme is empty — so a check that asks only "does it start with a
// slash?" waves an entirely different origin straight through. Browsers
// also normalise a backslash in that position, which makes "/\evil.com"
// the same attack spelled differently. Both are refused on the second
// byte, before any parsing, because a parser's opinion is not the thing a
// browser will act on.
func validateRelativeLink(raw string) error {
	constraint := errors.New(`must be a root-relative path such as "/pgadmin", not protocol-relative`)
	if len(raw) > 1 && (raw[1] == '/' || raw[1] == '\\') {
		return constraint
	}
	for _, character := range raw {
		if character < 0x20 || character == 0x7f || character == ' ' {
			return errors.New("must not contain spaces or control characters; percent-encode them")
		}
	}
	// Belt and braces: a relative reference must carry no authority, no
	// scheme, and no user information once parsed either.
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return constraint
	}
	return nil
}

// validateListenAddr accepts host:port with an optional host.
func validateListenAddr(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	if err != nil || port == "" {
		return errors.New("must be a host:port listen address")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return errors.New("must use a numeric port")
	}
	_ = host
	return nil
}

// loadRepositoryEvidence reads the four-variable repository-evidence
// consumer contract. Validation is all-or-nothing per the evidence API
// contract gate: with any of the four set, all four must be present and
// valid, or the process fails before listening; with none set, the
// evidence panels are disabled.
func loadRepositoryEvidence(lookup Lookup, cfg *Config, fail func(name, constraint string)) {
	read := func(name string) string {
		raw, _ := lookup(name)
		return raw
	}
	url := read(EnvRepositoryEvidenceURL)
	tokenFile := read(EnvRepositoryEvidenceTokenFile)
	fingerprint := read(EnvRepositoryExpectedFingerprint)
	server := read(EnvRepositoryBarmanServer)
	if url == "" && tokenFile == "" && fingerprint == "" && server == "" {
		return
	}

	valid := true
	check := func(name string, err error) {
		if err != nil {
			fail(name, err.Error())
			valid = false
		}
	}
	check(EnvRepositoryEvidenceURL, validateEvidenceAddr(url))
	check(EnvRepositoryEvidenceTokenFile, validateEvidenceTokenFile(tokenFile))
	check(EnvRepositoryExpectedFingerprint, validateEvidenceFingerprint(fingerprint))
	check(EnvRepositoryBarmanServer, validateBarmanServerName(server))
	if !valid {
		return
	}
	cfg.RepositoryEvidenceURL = url
	cfg.RepositoryEvidenceTokenFile = tokenFile
	cfg.RepositoryExpectedFingerprint = fingerprint
	cfg.RepositoryBarmanServer = server
}

// validateEvidenceAddr accepts a unix:// socket URI or an absolute
// socket path. Every other address — loopback and any TCP form
// included — is a configuration error: the evidence API of the sidecar
// contract exists only on a pod-private Unix socket.
func validateEvidenceAddr(raw string) error {
	if raw == "" {
		return errRequiredWithEvidence
	}
	const constraint = "must be a unix:// socket URI or an absolute socket path"
	path := raw
	if strings.HasPrefix(raw, "unix://") {
		path = strings.TrimPrefix(raw, "unix://")
	}
	if !strings.HasPrefix(path, "/") || len(path) < 2 {
		return errors.New(constraint)
	}
	return nil
}

// errRequiredWithEvidence is the shared all-or-nothing constraint: once
// any repository-evidence variable is set, every one of the four is
// required.
var errRequiredWithEvidence = errors.New("is required when repository evidence is configured")

// validateEvidenceTokenFile accepts an absolute file path to the
// mounted pod-local token. The file's content is validated where it is
// read, before the listener opens; the path alone is a configuration
// concern.
func validateEvidenceTokenFile(raw string) error {
	if raw == "" {
		return errRequiredWithEvidence
	}
	if !strings.HasPrefix(raw, "/") || len(raw) < 2 || strings.ContainsAny(raw, "\x00\r\n") {
		return errors.New("must be an absolute file path")
	}
	return nil
}

// validateEvidenceFingerprint accepts the full destination fingerprint
// shape of the evidence contract: "sha256:" plus 64 lowercase hex
// characters.
func validateEvidenceFingerprint(raw string) error {
	if raw == "" {
		return errRequiredWithEvidence
	}
	constraint := errors.New(`must be "sha256:" plus 64 lowercase hex characters`)
	rest, ok := strings.CutPrefix(raw, "sha256:")
	if !ok || len(rest) != 64 {
		return constraint
	}
	for _, character := range rest {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return constraint
		}
	}
	return nil
}

// validateBarmanServerName accepts an exact Barman server name under
// the evidence contract's identifier rules: bounded, free of control
// characters and path separators, and never a directory dot name.
func validateBarmanServerName(raw string) error {
	if raw == "" {
		return errRequiredWithEvidence
	}
	constraint := errors.New("must be a bounded name without path separators or control characters")
	if len(raw) > 256 || raw == "." || raw == ".." || strings.ContainsAny(raw, "/\\") {
		return constraint
	}
	if strings.ToValidUTF8(raw, "") != raw || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return constraint
	}
	return nil
}
