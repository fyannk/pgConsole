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

// Package instancestatus reads what each instance manager says about its
// own PostgreSQL: the /pg/status report on the instance pod's status
// port, the same report the operator reads to make its decisions and
// kubectl cnpg status prints. It is the richest instance-side source the
// console has that involves no SQL — archiver state, pending restarts,
// replication slots, the manager's own version — and it is read the way
// the metrics exporter is: a bounded HTTP GET to each instance pod's IP
// on a sweep, nothing stored beyond the last report per instance.
//
// The report is the instance's claim about itself, and everything here
// is attributed as such. A pod the sweep cannot reach keeps its last
// report with the failure beside it; a pod that leaves the roster is
// forgotten. From CloudNativePG 1.30 the status port's mutating paths
// require the operator's client certificate, and /pg/status does not;
// a status port configured TLS-only is one the console cannot read, and
// the sweep says so per instance rather than guessing.
package instancestatus

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

const (
	// Port is the instance manager's status port.
	Port = "8000"
	// path is the status report's path.
	path = "/pg/status"
	// maxBody bounds one report; the report is a few kilobytes, and
	// anything larger is cut, not buffered.
	maxBody = 1 << 20
	// requestTimeout bounds one request.
	requestTimeout = 5 * time.Second
	// maxStandbys and maxSlots bound the lists one report may carry.
	maxStandbys = 64
	maxSlots    = 64
	// maxText bounds each string the report carries.
	maxText = 256
)

// Standby is one row of the primary's pg_stat_replication as the
// instance manager reports it.
type Standby struct {
	ApplicationName string
	State           string
	SyncState       string
	SyncPriority    string
	WriteLag        string
	FlushLag        string
	ReplayLag       string
}

// Slot is one row of the instance's pg_replication_slots.
type Slot struct {
	Name      string
	Type      string
	Database  string
	Plugin    string
	WALStatus string
	Active    bool
}

// Reading is one instance's report, converted to a source-neutral shape.
// A string the instance did not report is empty; a time it did not
// report is nil.
type Reading struct {
	// Instance is the pod the report came from.
	Instance string
	// ObservedAt is when the sweep read it.
	ObservedAt time.Time
	// IsPrimary is the instance's own claim to the primary role.
	IsPrimary bool
	// ReplayPaused reports a replica whose WAL replay is paused.
	ReplayPaused bool
	// PendingRestart reports a configuration change waiting for a
	// restart; PendingRestartForDecrease that it is a decrease of a
	// hot-standby-sensitive parameter.
	PendingRestart            bool
	PendingRestartForDecrease bool
	// IsWalReceiverActive reports a replica with a live WAL receiver.
	IsWalReceiverActive bool
	// IsPgRewindRunning reports a rewind in progress.
	IsPgRewindRunning bool
	// MightBeUnavailable is the instance manager's own flag that its
	// PostgreSQL may not be reachable, with the error it masked.
	MightBeUnavailable            bool
	MightBeUnavailableMaskedError string
	// IsArchivingWAL reports that this instance archives WAL.
	IsArchivingWAL bool
	// IsInstanceManagerUpgrading reports an in-place manager upgrade.
	IsInstanceManagerUpgrading bool
	// CurrentLSN, ReceivedLSN and ReplayLSN are the instance's positions.
	CurrentLSN  string
	ReceivedLSN string
	ReplayLSN   string
	// CurrentWAL is the segment being written; ReadyWALFiles counts the
	// segments waiting to be archived.
	CurrentWAL    string
	ReadyWALFiles int
	// LastArchivedWAL and LastFailedWAL are pg_stat_archiver's, with the
	// instants they carry.
	LastArchivedWAL     string
	LastArchivedWALTime *time.Time
	LastFailedWAL       string
	LastFailedWALTime   *time.Time
	// SystemID and TimelineID identify what the instance is replaying.
	SystemID   string
	TimelineID int
	// InstanceManagerVersion and InstanceArch are the manager's own.
	InstanceManagerVersion string
	InstanceArch           string
	// Node is the node the instance reports running on.
	Node string
	// Standbys is the primary's replication view, bounded; Slots the
	// instance's replication slots, bounded.
	Standbys []Standby
	Slots    []Slot
}

// Snapshot is what the console reads: the last report per instance,
// and per instance the failure of the last sweep, when it failed.
type Snapshot struct {
	// Generation increases by one on every sweep that published.
	Generation uint64
	// SweptAt is when the last sweep ran.
	SweptAt time.Time
	// Interval is the sweep cadence, so a reader can judge a report's
	// age against it.
	Interval time.Duration
	// Readings are the last report per instance, by instance name.
	Readings map[string]Reading
	// Failing records, per instance the last sweep could not read, the
	// failure's category. An instance present here may still have a
	// reading from an earlier sweep.
	Failing map[string]string
}

// Instances lists the instances with a reading, sorted.
func (s Snapshot) Instances() []string {
	names := make([]string, 0, len(s.Readings))
	for name := range s.Readings {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Store holds the current snapshot for concurrent readers.
type Store struct {
	mu       sync.RWMutex
	snap     Snapshot
	has      bool
	interval time.Duration
}

// NewStore returns an empty store for the given sweep cadence.
func NewStore(interval time.Duration) *Store {
	return &Store{interval: interval, snap: Snapshot{Interval: interval}}
}

// Interval is the sweep cadence.
func (s *Store) Interval() time.Duration { return s.interval }

// CurrentInstanceStatus returns the snapshot and whether a sweep has
// published one.
func (s *Store) CurrentInstanceStatus() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot with the sweep's outcome: fresh
// readings for the instances reached, the previous reading kept beside
// the failure for the ones not reached, and instances no longer on the
// roster dropped.
func (s *Store) publish(at time.Time, roster []string, fresh map[string]Reading, failing map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := Snapshot{
		Generation: s.snap.Generation + 1,
		SweptAt:    at,
		Interval:   s.interval,
		Readings:   make(map[string]Reading, len(roster)),
		Failing:    make(map[string]string, len(failing)),
	}
	for _, name := range roster {
		if reading, ok := fresh[name]; ok {
			next.Readings[name] = reading
			continue
		}
		if previous, ok := s.snap.Readings[name]; ok {
			next.Readings[name] = previous
		}
		if category, ok := failing[name]; ok {
			next.Failing[name] = category
		}
	}
	s.snap = next
	s.has = true
}

// PodsSource is the roster the sweep reads: the instance pods and
// their IPs.
type PodsSource interface {
	CurrentPods() (observe.PodsSnapshot, bool)
}

// Collector sweeps the instance pods' status ports on a cadence.
type Collector struct {
	source   PodsSource
	store    *Store
	clock    observe.Clock
	logger   *slog.Logger
	interval time.Duration
	client   *http.Client
	port     string
	// failing tracks which instances the previous sweep could not
	// reach, so reachability changes log once instead of every sweep.
	failing map[string]bool
}

// New wires a roster to a store. The port is the status port, fixed to
// the CloudNativePG default and overridden only by tests.
func New(source PodsSource, store *Store, port string, clock observe.Clock, logger *slog.Logger) *Collector {
	if port == "" {
		port = Port
	}
	return &Collector{
		source:   source,
		store:    store,
		clock:    clock,
		logger:   logger,
		interval: store.Interval(),
		client: &http.Client{Timeout: requestTimeout, Transport: &http.Transport{
			// See readOne: the CA is in a Secret the console never reads.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // identity rests on the API-server-reported pod IP, as for metrics
		}},
		port:    port,
		failing: map[string]bool{},
	}
}

// Run sweeps until ctx is done.
func (c *Collector) Run(ctx context.Context) error {
	for {
		if err := c.clock.Wait(ctx, c.interval); err != nil {
			return err
		}
		c.sweep(ctx)
	}
}

// sweep reads every instance pod with an IP once. A pod without an IP
// has not started and is not a failure; a deleting pod is not read.
func (c *Collector) sweep(ctx context.Context) {
	snap, ok := c.source.CurrentPods()
	if !ok {
		return
	}
	at := c.clock.Now()
	var roster []string
	fresh := map[string]Reading{}
	failing := map[string]string{}
	for _, pod := range snap.Pods {
		if pod.IP == "" || pod.Deleting {
			continue
		}
		roster = append(roster, pod.Name)
		reading, err := c.readOne(ctx, pod.IP)
		if err != nil {
			category := redact.Safe(err)
			failing[pod.Name] = category
			if !c.failing[pod.Name] {
				c.failing[pod.Name] = true
				c.logger.Info("instance status read failed",
					slog.String("instance", pod.Name),
					slog.String("category", category))
			}
			continue
		}
		if c.failing[pod.Name] {
			delete(c.failing, pod.Name)
			c.logger.Info("instance status read recovered", slog.String("instance", pod.Name))
		}
		reading.Instance = pod.Name
		reading.ObservedAt = at
		fresh[pod.Name] = reading
	}
	c.store.publish(at, roster, fresh, failing)
}

// readOne fetches and converts one instance's report. The status port
// serves TLS from CloudNativePG 1.30 and plain HTTP before, so TLS is
// tried first and plain HTTP when TLS is refused. The certificate is
// the cluster's own, signed by a CA that lives in a Secret this console
// never reads, so it is not verified: the trust rests on the pod IP the
// API server reported, exactly as the metrics sweep's does, and TLS
// here adds confidentiality on the wire, not identity.
func (c *Collector) readOne(ctx context.Context, ip string) (Reading, error) {
	var last error
	for _, scheme := range []string{"https", "http"} {
		reading, err := c.readScheme(ctx, scheme, ip)
		if err == nil {
			return reading, nil
		}
		last = err
	}
	return Reading{}, last
}

func (c *Collector) readScheme(ctx context.Context, scheme, ip string) (Reading, error) {
	url := scheme + "://" + net.JoinHostPort(ip, c.port) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Reading{}, redact.NewError("instance status read", redact.CategoryInternal, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Reading{}, redact.NewError("instance status read", redact.CategoryUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Reading{}, redact.NewError("instance status read", redact.CategoryUnavailable,
			fmt.Errorf("status %d", resp.StatusCode))
	}
	return Parse(io.LimitReader(resp.Body, maxBody))
}

// report is the subset of the instance manager's JSON the console reads.
// The field names are the operator's own JSON tags, pinned by the
// catalog rules that read them.
type report struct {
	CurrentLsn                    string `json:"currentLsn"`
	ReceivedLsn                   string `json:"receivedLsn"`
	ReplayLsn                     string `json:"replayLsn"`
	SystemID                      string `json:"systemID"`
	IsPrimary                     bool   `json:"isPrimary"`
	ReplayPaused                  bool   `json:"replayPaused"`
	PendingRestart                bool   `json:"pendingRestart"`
	PendingRestartForDecrease     bool   `json:"pendingRestartForDecrease"`
	IsWalReceiverActive           bool   `json:"isWalReceiverActive"`
	IsPgRewindRunning             bool   `json:"isPgRewindRunning"`
	MightBeUnavailable            bool   `json:"mightBeUnavailable"`
	MightBeUnavailableMaskedError string `json:"mightBeUnavailableMaskedError"`
	IsArchivingWAL                bool   `json:"isArchivingWAL"`
	Node                          string `json:"node"`
	LastArchivedWAL               string `json:"lastArchivedWAL"`
	LastArchivedWALTime           string `json:"lastArchivedWALTime"`
	LastFailedWAL                 string `json:"lastFailedWAL"`
	LastFailedWALTime             string `json:"lastFailedWALTime"`
	CurrentWAL                    string `json:"currentWAL"`
	ReadyWALFiles                 int    `json:"readyWalFiles"`
	TimeLineID                    int    `json:"timeLineID"`
	InstanceManagerVersion        string `json:"instanceManagerVersion"`
	InstanceArch                  string `json:"instanceArch"`
	IsInstanceManagerUpgrading    bool   `json:"isInstanceManagerUpgrading"`
	ReplicationInfo               []struct {
		ApplicationName string `json:"applicationName"`
		State           string `json:"state"`
		WriteLag        string `json:"writeLag"`
		FlushLag        string `json:"flushLag"`
		ReplayLag       string `json:"replayLag"`
		SyncState       string `json:"syncState"`
		SyncPriority    string `json:"syncPriority"`
	} `json:"replicationInfo"`
	ReplicationSlotsInfo []struct {
		SlotName  string `json:"slotName"`
		Plugin    string `json:"plugin"`
		SlotType  string `json:"slotType"`
		Database  string `json:"database"`
		Active    bool   `json:"active"`
		WalStatus string `json:"walStatus"`
	} `json:"replicationSlotsInfo"`
}

// Parse decodes one report into a bounded Reading. A malformed body is
// an error; an absent field stays at its zero value.
func Parse(body io.Reader) (Reading, error) {
	var r report
	if err := json.NewDecoder(body).Decode(&r); err != nil {
		return Reading{}, redact.NewError("instance status decode", redact.CategoryUnavailable, err)
	}
	reading := Reading{
		IsPrimary:                     r.IsPrimary,
		ReplayPaused:                  r.ReplayPaused,
		PendingRestart:                r.PendingRestart,
		PendingRestartForDecrease:     r.PendingRestartForDecrease,
		IsWalReceiverActive:           r.IsWalReceiverActive,
		IsPgRewindRunning:             r.IsPgRewindRunning,
		MightBeUnavailable:            r.MightBeUnavailable,
		MightBeUnavailableMaskedError: bound(r.MightBeUnavailableMaskedError),
		IsArchivingWAL:                r.IsArchivingWAL,
		IsInstanceManagerUpgrading:    r.IsInstanceManagerUpgrading,
		CurrentLSN:                    bound(r.CurrentLsn),
		ReceivedLSN:                   bound(r.ReceivedLsn),
		ReplayLSN:                     bound(r.ReplayLsn),
		CurrentWAL:                    bound(r.CurrentWAL),
		ReadyWALFiles:                 r.ReadyWALFiles,
		LastArchivedWAL:               bound(r.LastArchivedWAL),
		LastArchivedWALTime:           instant(r.LastArchivedWALTime),
		LastFailedWAL:                 bound(r.LastFailedWAL),
		LastFailedWALTime:             instant(r.LastFailedWALTime),
		SystemID:                      bound(r.SystemID),
		TimelineID:                    r.TimeLineID,
		InstanceManagerVersion:        bound(r.InstanceManagerVersion),
		InstanceArch:                  bound(r.InstanceArch),
		Node:                          bound(r.Node),
	}
	for i, standby := range r.ReplicationInfo {
		if i == maxStandbys {
			break
		}
		reading.Standbys = append(reading.Standbys, Standby{
			ApplicationName: bound(standby.ApplicationName), State: bound(standby.State),
			SyncState: bound(standby.SyncState), SyncPriority: bound(standby.SyncPriority),
			WriteLag: bound(standby.WriteLag), FlushLag: bound(standby.FlushLag), ReplayLag: bound(standby.ReplayLag),
		})
	}
	for i, slot := range r.ReplicationSlotsInfo {
		if i == maxSlots {
			break
		}
		reading.Slots = append(reading.Slots, Slot{
			Name: bound(slot.SlotName), Type: bound(slot.SlotType), Database: bound(slot.Database),
			Plugin: bound(slot.Plugin), WALStatus: bound(slot.WalStatus), Active: slot.Active,
		})
	}
	return reading, nil
}

// bound cuts one reported string at the text ceiling.
func bound(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return s
}

// instantLayouts are the forms the instance manager writes a timestamp
// in: RFC3339 from Go, and PostgreSQL's own text for pg_stat_archiver.
var instantLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999-07",
}

// instant parses one reported timestamp; nil for an absent or
// unparseable value, never a wrong instant.
func instant(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	for _, layout := range instantLayouts {
		if at, err := time.Parse(layout, raw); err == nil {
			at = at.UTC()
			return &at
		}
	}
	return nil
}
