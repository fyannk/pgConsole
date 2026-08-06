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

package web

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/ops"
)

// Origin identifies which observer a rendered claim comes from.
// Attribution is a type, not a convention: a template receives claims
// only inside view models that carry their Origin, so a claim cannot be
// rendered without its source, and the vocabularies never blend.
type Origin string

// The three claim origins of the product.
const (
	// OriginOperator marks state reported by the CloudNativePG operator.
	OriginOperator Origin = "operator-reported"
	// OriginKubernetes marks state observed from the Kubernetes API.
	OriginKubernetes Origin = "kubernetes-observed"
	// OriginRepository marks independent repository evidence.
	OriginRepository Origin = "repository-evidence"
)

// Label returns the origin text rendered next to a claim.
func (o Origin) Label() string {
	return string(o)
}

// unknown is the explicit rendering of any fact the sources did not
// report. Absence of data is never an empty cell and never a value.
const unknown = "unknown"

// maxDisplayMessage bounds rendered operator messages in runes.
const maxDisplayMessage = 512

// templateFuncs are the helpers the templates may call. They classify
// text that is already rendered; none of them produce a fact, and none
// may hide one.
var templateFuncs = template.FuncMap{"stateToken": stateToken, "add": func(a, b int) int { return a + b }}

// stateToken reduces a rendered state string to one of the four tokens
// the stylesheet keys its status treatment on: current, stale, degraded
// or unknown. It is presentation only — the state word stays in the
// text, so a reader who never receives the stylesheet loses nothing.
// Anything unrecognized classifies as unknown rather than as healthy.
func stateToken(state string) string {
	switch {
	case state == "current":
		return "current"
	case state == "stale" || strings.HasPrefix(state, "stale"):
		return "stale"
	case strings.HasPrefix(state, "absent") || strings.HasPrefix(state, "degraded"):
		return "degraded"
	default:
		return unknown
	}
}

// Panel is one attributed region of the console page. Every panel names
// its claim origin and an explicit state; absence of data is the state
// "unknown", never an empty healthy panel.
type Panel struct {
	// Title names the panel.
	Title string
	// Origin attributes every claim the panel shows.
	Origin Origin
	// State is the explicit panel state, such as "unknown".
	State string
	// Detail explains the state in one short sentence.
	Detail string
}

// ConditionView is one operator-reported condition prepared for
// rendering: bounded, with empty facts already replaced by "unknown".
type ConditionView struct {
	// Type is the condition type.
	Type string
	// Status is the reported status.
	Status string
	// Reason is the machine reason or "unknown".
	Reason string
	// Message is the bounded human message, possibly empty.
	Message string
}

// ClusterView is the operator-reported cluster section. All fields are
// display strings: a fact the operator did not report is "unknown".
type ClusterView struct {
	// Origin attributes the whole section.
	Origin Origin
	// Absent reports that the API server confirmed the cluster does not
	// exist; the fact fields are meaningless then.
	Absent bool
	// Phase is the operator-reported phase.
	Phase string
	// PhaseReason is the operator-reported phase reason.
	PhaseReason string
	// CurrentPrimary is the current primary instance.
	CurrentPrimary string
	// TargetPrimary is the target primary instance.
	TargetPrimary string
	// Instances is "ready/desired" or "unknown".
	Instances string
	// Timeline is the PostgreSQL timeline or "unknown".
	Timeline string
	// Image is the reported container image or "unknown".
	Image string
	// PostgresVersion is the reported major version or "unknown".
	PostgresVersion string
	// Conditions are the bounded operator conditions.
	Conditions []ConditionView
}

// Link is one operator-configured link-out, rendered as a plain anchor.
type Link struct {
	// Label names the destination.
	Label string
	// URL is the validated link-out base URL.
	URL string
}

// SectionMeta is the per-section snapshot line: sources fail
// independently, so each section states its own freshness.
type SectionMeta struct {
	// State is "current" or "stale".
	State string
	// Age is the snapshot age.
	Age string
	// Generation is the snapshot generation.
	Generation string
}

// PodRowView is one instance pod prepared for rendering; unreported
// facts are "unknown".
type PodRowView struct {
	// Name is the pod name.
	Name string
	// Role is the observed instance role or "unknown".
	Role string
	// Phase is the pod phase, suffixed when the pod is deleting.
	Phase string
	// Ready is "true", "false", or "unknown".
	Ready string
	// Restarts is the restart count or "unknown".
	Restarts string
	// Node is the assigned node or "unknown".
	Node string
	// Image is the PostgreSQL container image or "unknown".
	Image string
	// LogsURL links the pod's bounded log tail; empty renders no link.
	LogsURL string
}

// DisagreementView renders a primary-role disagreement between the two
// observers. Both claims keep their origins; the console never resolves
// the conflict silently.
type DisagreementView struct {
	// OperatorClaim states the operator's primary claim.
	OperatorClaim string
	// OperatorOrigin attributes it.
	OperatorOrigin Origin
	// ObservedClaim states what the pod labels report.
	ObservedClaim string
	// ObservedOrigin attributes it.
	ObservedOrigin Origin
}

// EventRowView is one rendered event; unreported facts are "unknown".
type EventRowView struct {
	// Type is the event type, such as "Warning".
	Type string
	// Reason is the machine reason or "unknown".
	Reason string
	// Object is "kind/name" of the involved object.
	Object string
	// Message is the bounded human message, possibly empty.
	Message string
	// Count is the delivery count.
	Count string
	// Age is the time since the last occurrence.
	Age string
}

// EventsView is the Kubernetes-observed event section.
type EventsView struct {
	// Origin attributes the whole section.
	Origin Origin
	// Meta is the section's own snapshot line.
	Meta SectionMeta
	// Window states the configured age window.
	Window string
	// Rows are the bounded, newest-first events.
	Rows []EventRowView
	// Truncated reports that more candidates existed than the bound.
	Truncated bool
	// PodEventsWithheld reports that pod events exist but membership is
	// unknown, so they are withheld rather than guessed.
	PodEventsWithheld bool
}

// BackupRowView is one operator-reported Backup. A completed phase remains an
// attributed operator claim; it is never presented as repository evidence.
type BackupRowView struct {
	// Name is the Backup resource name.
	Name string
	// Phase is the attributed phase display.
	Phase string
	// Method is the reported backup method.
	Method string
	// Started is the UTC start time or unknown.
	Started Stamp
	// Stopped is the UTC stop time or unknown.
	Stopped Stamp
	// Age is relative to stop time, falling back to creation time.
	Age string
	// SnapshotState is explicitly unknown for volume snapshots.
	SnapshotState string
	// SourceInstance is the operator-reported instance the backup was
	// taken from, or unknown when it reported none.
	SourceInstance string
}

// ScheduledBackupRowView is one operator-reported ScheduledBackup.
type ScheduledBackupRowView struct {
	// Name is the ScheduledBackup resource name.
	Name string
	// Method is the configured backup method.
	Method string
	// Schedule is the reported six-field cron expression.
	Schedule string
	// Suspended is true, false, or unknown.
	Suspended string
	// LastSchedule is the reported UTC time or unknown.
	LastSchedule Stamp
	// NextSchedule is the reported UTC time or unknown.
	NextSchedule Stamp
}

// BackupsView is the bounded Backup and ScheduledBackup section.
type BackupsView struct {
	// Origin attributes all Backup and ScheduledBackup claims.
	Origin Origin
	// Meta is this catalog's own freshness line.
	Meta SectionMeta
	// LastCompletedAge is derived from the latest completed stop time.
	LastCompletedAge string
	// Rows are the bounded Backup rows.
	Rows []BackupRowView
	// ScheduledRows are the bounded ScheduledBackup rows.
	ScheduledRows []ScheduledBackupRowView
	// BackupsTruncated reports a Backup safety ceiling.
	BackupsTruncated bool
	// SchedulesTruncated reports a ScheduledBackup safety ceiling.
	SchedulesTruncated bool
	// EvidenceLink is the optional ObjectStoreViewer link.
	EvidenceLink *Link
	// BaseSourceInstance is the instance named by the most recent
	// Backup that reported one, empty when none did. The wiring
	// drawings use it to leave the base-backup wire from the instance
	// that actually served it rather than assuming the primary; when it
	// is empty no base wire is drawn, because the console then has no
	// word on where that flow began.
	BaseSourceInstance string
	// BaseVia is the short plugin name that same Backup reported, empty
	// when it named none. It is what lets the drawing say which
	// mechanism took the backup instead of asserting one.
	BaseVia string
}

// ObjectStoreView separates the operator-reported reference from the
// Kubernetes-observed metadata lookup.
type ObjectStoreView struct {
	// Name is the configured reference name or unknown.
	Name string
	// ReferenceState is the cluster configuration claim.
	ReferenceState string
	// ReferenceOrigin attributes the cluster configuration claim.
	ReferenceOrigin Origin
	// ObservationState is the metadata lookup outcome.
	ObservationState string
	// ObservationOrigin attributes the API lookup claim.
	ObservationOrigin Origin
}

// StateLineView is one repository semantic result: the four-state
// value plus the stable reason code. Producer messages never reach
// this type.
type StateLineView struct {
	// State is healthy, warning, unhealthy, or unknown.
	State string
	// Code is the stable reason code.
	Code string
}

// Display returns "state (code)" or "unknown".
func (s StateLineView) Display() string {
	if s.State == "" {
		return unknown
	}
	if s.Code == "" {
		return s.State
	}
	return s.State + " (" + s.Code + ")"
}

// CapabilityRowView is one repository capability row.
type CapabilityRowView struct {
	// ID identifies the evidence operation.
	ID string
	// Support states whether the format proves the capability.
	Support string
	// State is the capability's result display.
	State string
}

// BarmanSummaryView is the recognized barman-cloud summary block.
type BarmanSummaryView struct {
	// Backups is the backup evidence count display.
	Backups string
	// BackupStates summarizes backups per evidence state.
	BackupStates string
	// WAL is the WAL continuity result.
	WAL StateLineView
	// WALCounts summarizes WAL objects per class.
	WALCounts string
	// Timeline is the timeline traversal result.
	Timeline StateLineView
	// Coverage is the observed recovery coverage result.
	Coverage StateLineView
	// Retention is the retention comparison result.
	Retention StateLineView
	// RetentionBackups is the visible/usable backup count display.
	RetentionBackups string
	// RetentionOldest and RetentionNewest bracket the completion window.
	// Two stamps rather than one joined sentence, so each end can be
	// restated in the reader’s zone on its own.
	RetentionOldest Stamp
	RetentionNewest Stamp
	// RetentionMinimum is the configured redundancy expectation display.
	RetentionMinimum string
	// LatestArchiveAge is the newest WAL receipt age.
	LatestArchiveAge string
	// Truncated reports a producer safety ceiling on ranges or
	// diagnostics; the affected conclusions are already unknown.
	Truncated bool
}

// RepositoryView is the repository-evidence section: the independent
// observer's claims, re-rendered from the validated projection, never
// blended with operator or Kubernetes vocabulary.
type RepositoryView struct {
	// Origin attributes the whole section.
	Origin Origin
	// Meta is the console's own sidecar-contact line.
	Meta SectionMeta
	// ContactFailure is the latest poll failure kind, empty while
	// contact holds.
	ContactFailure string
	// Fingerprint is the redacted repository destination identity.
	Fingerprint string
	// Scope is the format-owned scope display.
	Scope string
	// Repository is the provider and format display.
	Repository string
	// ProducerVersion is the emitting sidecar build version.
	ProducerVersion string
	// ClusterIdentity states how the evidence's cluster binding relates
	// to the console's own observed cluster UID.
	ClusterIdentity string
	// Revision is the sidecar's publication revision.
	Revision string
	// EvidenceGeneration is the sidecar's complete-evidence generation.
	EvidenceGeneration string
	// Completeness is the sidecar's completeness claim.
	Completeness string
	// SourceStale reports the sidecar's own staleness claim against
	// the repository — distinct from the console's contact staleness.
	SourceStale bool
	// Overall is the snapshot state and reason code.
	Overall StateLineView
	// ScanCompleted is the evidence scan completion time or unknown.
	ScanCompleted Stamp
	// LastAttempt is the last refresh attempt time or unknown.
	LastAttempt Stamp
	// Inventory is the provider-neutral inventory display.
	Inventory string
	// InventoryFailure is the producer's redacted failure category of
	// a failed latest attempt, empty otherwise.
	InventoryFailure string
	// Capabilities are the complete capability rows.
	Capabilities []CapabilityRowView
	// DetailsUnknown reports an unrecognized details variant; the
	// bounded tag is shown, the payload was discarded by the consumer.
	DetailsUnknown bool
	// DetailsType is the tagged-union type display.
	DetailsType string
	// Barman is the recognized barman-cloud summary, nil otherwise.
	Barman *BarmanSummaryView
}

// PodsView is the Kubernetes-observed instance pod section.
type PodsView struct {
	// Origin attributes the whole section.
	Origin Origin
	// Meta is the section's own snapshot line.
	Meta SectionMeta
	// Rows are the bounded, sorted pods.
	Rows []PodRowView
	// Truncated reports that the member set exceeded the bound and was
	// cut, visibly.
	Truncated bool
	// Disagreement is non-nil when the operator-reported primary and
	// the observed role labels conflict.
	Disagreement *DisagreementView
	// LogsEnabled reports that the log tail affordance exists.
	LogsEnabled bool
}

// LogsView is the log tail page's view model.
type LogsView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// ClusterName is the configured target cluster.
	ClusterName string
	// Pod is the requested member pod.
	Pod string
	// Origin attributes the content.
	Origin Origin
	// State carries the refusal or unavailability text; empty on
	// success.
	State string
	// Bounds states the applied line and byte limits.
	Bounds string
	// Content is the tail content, rendered as escaped text only.
	Content string
	// Truncated reports the byte ceiling cut this tail.
	Truncated bool
}

// IdentityView is the display-only identity line. Both the user and the
// level are proxy-asserted — trustworthy under the deployment's
// ingress-confinement invariant, never the user's own RBAC. It is never
// used for authorization decisions here — those happen before rendering.
type IdentityView struct {
	// User is the forwarded username, escaped by the template.
	User string
	// Level is the proxy-asserted authorization level (view, poweruser,
	// dba, or none when unrecognized).
	Level string
	// Label states the value's worth: "proxy-asserted".
	Label string
}

// DeniedView is the constant denial page's view model.
type DeniedView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// Message is the constant denial text; it carries no identity and
	// no probe detail.
	Message string
}

// OperationsView lists the enumerated operations.
type OperationsView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// ClusterName is the target cluster.
	ClusterName string
	// Operations is the closed catalog.
	Operations []ops.Descriptor
}

// ConfirmView renders one operation's confirmation form.
type ConfirmView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// ClusterName is the target cluster.
	ClusterName string
	// Op is the operation being confirmed.
	Op ops.Descriptor
	// Target is the chosen instance, for instance operations.
	Target string
	// CSRFToken is the confirmation token bound to Op and Target.
	CSRFToken string
}

// ResultView renders the fire-and-observe outcome.
type ResultView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// ClusterName is the target cluster.
	ClusterName string
	// Op is the requested operation.
	Op ops.Descriptor
	// Target is the chosen instance, for instance operations.
	Target string
	// Accepted reports the operator accepted the request.
	Accepted bool
	// Outcome is the outcome category, safe to display.
	Outcome string
}

// SummaryCard is one plain-language restatement on the overview.
type SummaryCard struct {
	// Label names what the card answers.
	Label string
	// Value is the short answer, already a display string.
	Value string
	// State is the presentation token, empty when the value carries no
	// state of its own.
	State string
	// Note is one sentence of context in plain language.
	Note string
	// Origin attributes the card. Every card names exactly one source.
	Origin Origin
}

// SummaryGroup is a titled run of cards drawn from a single source.
type SummaryGroup struct {
	// Title names the group in plain language.
	Title string
	// Origin attributes every card in the group.
	Origin Origin
	// Cards are the group's restatements.
	Cards []SummaryCard
}

// SummaryView is the plain-language overview that opens the console
// page. It is the one surface permitted to speak across the three claim
// vocabularies — see AGENTS.md rule 8 — and it earns that only by being
// derived: buildSummary reads nothing but the already-assembled Page, so
// the summary cannot state a fact the attributed sections below do not
// already carry, and every card still names its own single origin. The
// headline paraphrases; the sub-line quotes the operator verbatim, so
// the paraphrase is always anchored to the literal claim beneath it.
type SummaryView struct {
	// Headline is the one-line answer in plain language.
	Headline string
	// HeadlineState is the presentation token for the headline.
	HeadlineState string
	// Sub quotes the underlying claim the headline paraphrases.
	Sub string
	// Groups are the card runs in render order.
	Groups []SummaryGroup
}

// buildSummary derives the overview from the assembled page. It reads
// only p, never a snapshot, so it has no way to invent a fact; where the
// page has nothing it says so rather than guessing.
func buildSummary(p *Page) *SummaryView {
	s := &SummaryView{}

	switch {
	case p.Cluster == nil:
		s.Headline = "No cluster snapshot yet"
		s.HeadlineState = unknown
		s.Sub = "Nothing has been observed for this cluster, so the console cannot say anything about it."
	case p.Cluster.Absent:
		s.Headline = "This cluster is not in the namespace"
		s.HeadlineState = "degraded"
		s.Sub = "The API server confirmed no cluster by this name exists here."
	case p.SnapshotState == "stale":
		s.Headline = "Showing the last good view"
		s.HeadlineState = "stale"
		s.Sub = "The watch is broken, so what follows is retained from the last successful observation and may no longer be true. The operator last reported: " + p.Cluster.Phase + "."
	default:
		s.Headline = "The operator reports the cluster is healthy"
		s.HeadlineState = "current"
		s.Sub = "Reported phase: " + p.Cluster.Phase + "."
		if !strings.Contains(strings.ToLower(p.Cluster.Phase), "healthy") {
			s.Headline = "The operator reports a state that is not plain healthy"
			s.HeadlineState = unknown
		}
	}

	if p.Cluster != nil && !p.Cluster.Absent {
		s.Groups = append(s.Groups, SummaryGroup{
			Title: "The database", Origin: p.Cluster.Origin,
			Cards: []SummaryCard{
				{Label: "Servers", Value: p.Cluster.Instances,
					Note:   "Ready instances against the number the cluster asks for.",
					Origin: p.Cluster.Origin},
				{Label: "Accepting writes", Value: p.Cluster.CurrentPrimary,
					Note:   "The current primary. Replicas follow it and do not take writes.",
					Origin: p.Cluster.Origin},
				{Label: "PostgreSQL version", Value: p.Cluster.PostgresVersion,
					Note:   "Major version reported by the operator.",
					Origin: p.Cluster.Origin},
				{Label: "Timeline", Value: p.Cluster.Timeline,
					Note:   "Increments on every promotion or point-in-time restore.",
					Origin: p.Cluster.Origin},
			},
		})
	}

	if p.Backups != nil {
		schedule := unknown
		if len(p.Backups.ScheduledRows) > 0 {
			schedule = p.Backups.ScheduledRows[0].Schedule
		}
		s.Groups = append(s.Groups, SummaryGroup{
			Title: "Backups the operator claims", Origin: p.Backups.Origin,
			Cards: []SummaryCard{
				{Label: "Most recent backup", Value: p.Backups.LastCompletedAge,
					Note:   "Age of the newest Backup the operator marked completed. This is the operator's claim, not proof the data is in the repository.",
					Origin: p.Backups.Origin},
				{Label: "Schedule", Value: schedule,
					Note:   "The first ScheduledBackup entry, in cron form.",
					Origin: p.Backups.Origin},
			},
		})
	}

	if p.Repository != nil {
		cards := []SummaryCard{
			{Label: "Repository", Value: p.Repository.Overall.Display(),
				Note:   "What the repository scan concluded about the stored evidence.",
				Origin: p.Repository.Origin},
			{Label: "Completeness", Value: p.Repository.Completeness,
				Note:   "Whether the scan covered the whole repository.",
				Origin: p.Repository.Origin},
			{Label: "Stored", Value: p.Repository.Inventory,
				Note:   "What the scan found in object storage.",
				Origin: p.Repository.Origin},
		}
		if p.Repository.Barman != nil {
			cards = append(cards, SummaryCard{
				Label: "Last archive received", Value: p.Repository.Barman.LatestArchiveAge,
				Note:   "Age of the newest write-ahead log segment to reach the repository.",
				Origin: p.Repository.Origin,
			})
		}
		s.Groups = append(s.Groups, SummaryGroup{
			Title: "What the repository actually holds", Origin: p.Repository.Origin, Cards: cards,
		})
	}

	return s
}

// ShellView is the chrome every page shares: the fixed top bar's target
// identity and the section map in the sidebar. It introduces no fact of
// its own — every value on it is one the page already carries — and the
// map it renders is static, so a destination this build does not serve
// is shown disabled rather than omitted.
type ShellView struct {
	// ClusterName is the configured target cluster.
	ClusterName string
	// Namespace is the configured target namespace.
	Namespace string
	// CurrentURL is the same-origin request URI that rendered this shell.
	// The refresh control requests it again; it is server-derived rather
	// than accepted from client markup.
	CurrentURL string
	// SnapshotState is "none", "current", or "stale"; empty on pages
	// that render no snapshot at all.
	SnapshotState string
	// SnapshotAge is the age of the snapshot, empty without one.
	SnapshotAge string
	// Generation is the snapshot generation, empty without one.
	Generation string
	// Identity is the display-only identity line, nil when none was
	// forwarded or display is disabled.
	Identity *IdentityView
	// Links are the configured link-outs, possibly empty.
	Links []Link
	// OperationsAvailable reports that this deployment serves the
	// operations route. It is separate from CanOperate so a disabled
	// deployment can stay legible without exposing a destination to a user
	// whose asserted level cannot reach it.
	OperationsAvailable bool
	// CanOperate reports that this request carries both a usable forwarded
	// identity and the poweruser-or-higher level needed by the route.
	CanOperate bool
	// AccessReviewAvailable reports that this deployment serves the access
	// review route.
	AccessReviewAvailable bool
	// CanReviewAccess reports that this request carries both a usable
	// forwarded identity and the dba level needed by the route.
	CanReviewAccess bool
	// HistoryAvailable reports that the in-memory history read side is
	// constructed and its route is registered.
	HistoryAvailable bool
	// MetricsAvailable reports that the metrics window is constructed
	// and its routes are registered.
	MetricsAvailable bool
	// Current is the key of the page being rendered, used for
	// aria-current. Empty on pages outside the map.
	Current string
}

// Page is the view model of the console page.
type Page struct {
	// Shell is the shared chrome.
	Shell ShellView
	// Summary is the plain-language overview, derived from this page's
	// own sections. Nil when nothing has been observed.
	Summary *SummaryView
	// Topology is the grouped wiring drawing that opens the Overview:
	// poolers, the cluster, its backup schedules and its storage, laid
	// out by stated rules. Nil without a cluster snapshot.
	Topology *TopologyView
	// BackupsDrawing, PoolersDrawing and DatabasesDrawing open their
	// section screens with the same schema language, each adapted to
	// the one question that screen answers. Nil when the section has
	// nothing to draw.
	BackupsDrawing   *TopologyView
	PoolersDrawing   *TopologyView
	DatabasesDrawing *TopologyView
	// ClusterName is the configured target cluster.
	ClusterName string
	// Namespace is the configured target namespace.
	Namespace string
	// SnapshotState is "none", "current", or "stale".
	SnapshotState string
	// SnapshotAge is the age of the snapshot, empty without one.
	SnapshotAge string
	// Generation is the snapshot generation, empty without one.
	Generation string
	// Cluster is the operator-reported section, nil without a snapshot.
	Cluster *ClusterView
	// Pods is the Kubernetes-observed section, nil without a snapshot.
	Pods *PodsView
	// Events is the Kubernetes-observed event section, nil without a
	// snapshot.
	Events *EventsView
	// Backups is the operator-reported backup catalog.
	Backups *BackupsView
	// Poolers is the connection-pooler section, nil when never observed.
	Poolers *PoolersView
	// Databases is the declarative-objects section, nil when never
	// observed.
	Databases *DatabaseObjectsView
	// PoolerPods is the pooler pod roster, nil when never observed. It
	// reuses the instance pod view: the observations are the same, only
	// the selection and the ownership proof differ.
	PoolerPods *PodsView
	// Quorum is the failover-quorum panel, nil when never observed.
	Quorum *FailoverQuorumView
	// ClusterOverview is the power-user wiring screen, derived from
	// this page; nil without a snapshot.
	ClusterOverview *ClusterOverviewView
	// Objects is the inventory screen, grouped by the resource each
	// object belongs to and carrying each kind's own freshness.
	Objects *ObjectsView
	// PodHistory is the roster screen's merged recent timeline, set by
	// its handler only.
	PodHistory []PodTimelineEntry
	// Infrastructure is the observed service, claim and snapshot set;
	// nil when it was never observed.
	Infrastructure *InfrastructureView
	// ObjectStoreDetail is what the referenced ObjectStore reports about
	// where backups go; nil when none is referenced or observable.
	ObjectStoreDetail *ObjectStoreDetailView
	// ImageCatalog is the resolved catalog panel, nil when never
	// observed.
	ImageCatalog *ImageCatalogView
	// ObjectStore is the optional plugin reference lookup.
	ObjectStore *ObjectStoreView
	// RepositoryUnavailable explains why the evidence section carries no
	// report while the consumer is configured. Nil when the consumer is
	// disabled, which is a different claim, and nil when a report exists.
	RepositoryUnavailable *RepositoryUnavailableView
	// Repository is the repository-evidence section, nil when the
	// consumer is disabled or has no report yet.
	Repository *RepositoryView
	// CrossCheck is the composed backup cross-check, nil unless the
	// evidence consumer is enabled and an operator catalog exists.
	CrossCheck *CrossCheckView
	// Panels are the attributed regions in render order.
	Panels []Panel
	// Links are the configured link-outs, possibly empty.
	Links []Link
	// ViewerLinked reports that the ObjectStoreViewer link-out in
	// particular is configured — not merely that some sibling tool is.
	// The evidence screen tells a reader to open the viewer from the
	// sidebar, and that sentence is only true when the viewer is the
	// link that is there.
	ViewerLinked bool
	// Identity is the display-only identity line, nil when no identity
	// was forwarded or display is disabled.
	Identity *IdentityView
}

// Links are the operator-configured link-out URLs; empty entries hide
// the corresponding anchor.
type Links struct {
	// ObjectStoreViewer is the repository evidence link-out.
	ObjectStoreViewer string
	// PgAdmin is the SQL console link-out.
	PgAdmin string
	// Monitoring is the metrics dashboard link-out.
	Monitoring string
}

// noSnapshot is the placeholder detail of an absent section snapshot.
const noSnapshot = "no snapshot"

// snapshots bundles what buildPage renders from.
type snapshots struct {
	cluster         observe.Snapshot
	ok              bool
	pods            observe.PodsSnapshot
	podsOK          bool
	events          observe.EventsSnapshot
	eventsOK        bool
	backups         observe.BackupsSnapshot
	backupsOK       bool
	poolers         observe.PoolersSnapshot
	poolersOK       bool
	poolerPods      observe.PodsSnapshot
	poolerPodsOK    bool
	quorum          observe.FailoverQuorumSnapshot
	quorumOK        bool
	catalogs        observe.ImageCatalogsSnapshot
	catalogsOK      bool
	declared        observe.DatabaseObjectsSnapshot
	declaredOK      bool
	infra           observe.InfrastructureSnapshot
	infraOK         bool
	evidence        evidence.Status
	evidenceEnabled bool
	window          time.Duration
	allowLogs       bool
}

// buildPage assembles the page from the current snapshots. Handlers
// call this and the template; nothing here touches any API.
func buildPage(ctx context.Context, clusterName, namespace string, s snapshots, now time.Time, links Links) Page {
	page := Page{
		ClusterName:   clusterName,
		Namespace:     namespace,
		SnapshotState: "none",
		Links:         buildLinks(links),
		ViewerLinked:  links.ObjectStoreViewer != "",
	}
	if s.evidenceEnabled {
		if s.evidence.HasReport {
			page.Repository = buildRepositoryView(s.evidence, s.cluster, s.ok, now)
		} else {
			detail := "no successful sidecar contact yet"
			if s.evidence.Failure != evidence.FailureNone {
				detail += " (" + string(s.evidence.Failure) + ")"
			}
			page.RepositoryUnavailable = &RepositoryUnavailableView{
				Origin: OriginRepository, State: unknown, Detail: detail,
			}
		}
		if s.backupsOK {
			page.CrossCheck = buildCrossCheckView(crossCheckInputs{
				backups:   s.backups,
				evidence:  s.evidence,
				cluster:   s.cluster,
				clusterOK: s.ok,
			})
		}
	}
	if s.backupsOK {
		page.Backups = buildBackupsView(s.backups, now, links.ObjectStoreViewer)
		page.ObjectStore = buildObjectStoreView(s.backups.ObjectStore)
	} else {
		page.Panels = append(page.Panels,
			Panel{Title: "Backups", Origin: OriginOperator, State: unknown, Detail: noSnapshot},
			Panel{Title: "ObjectStore reference", Origin: OriginKubernetes, State: unknown, Detail: noSnapshot},
		)
	}
	if s.declaredOK {
		page.Databases = buildDatabaseObjectsView(s.declared, now)
	} else {
		page.Panels = append(page.Panels,
			Panel{Title: "Declared database objects", Origin: OriginOperator, State: unknown, Detail: noSnapshot})
	}
	if s.quorumOK {
		page.Quorum = buildFailoverQuorumView(s.quorum, now)
	} else {
		page.Panels = append(page.Panels,
			Panel{Title: "Failover quorum", Origin: OriginOperator, State: unknown, Detail: noSnapshot})
	}
	if s.catalogsOK {
		page.ImageCatalog = buildImageCatalogView(s.catalogs, s.cluster.Cluster.ImageCatalogRef, now)
	} else {
		page.Panels = append(page.Panels,
			Panel{Title: "Image catalog", Origin: OriginOperator, State: unknown, Detail: noSnapshot})
	}
	if s.poolerPodsOK {
		page.PoolerPods = buildPodsView(s.poolerPods, now, s.allowLogs, "/poolers/logs/")
	}
	if s.poolersOK {
		page.Poolers = buildPoolersView(s.poolers, now)
	} else {
		page.Panels = append(page.Panels,
			Panel{Title: "Poolers", Origin: OriginOperator, State: unknown, Detail: noSnapshot})
	}
	if s.eventsOK {
		page.Events = buildEventsView(s.events, s.pods, s.podsOK, s.window, now)
	} else {
		page.Panels = append([]Panel{{
			Title: "Events", Origin: OriginKubernetes, State: unknown, Detail: noSnapshot,
		}}, page.Panels...)
	}
	if s.podsOK {
		page.Pods = buildPodsView(s.pods, now, s.allowLogs, "/logs/")
	} else {
		page.Panels = append([]Panel{{
			Title: "Instance pods", Origin: OriginKubernetes, State: unknown, Detail: noSnapshot,
		}}, page.Panels...)
	}
	snap, ok := s.cluster, s.ok
	if !ok {
		page.Panels = append([]Panel{{
			Title: "Cluster status", Origin: OriginOperator, State: unknown, Detail: noSnapshot,
		}}, page.Panels...)
		page.Summary = buildSummary(&page)
		return page
	}

	page.SnapshotState = "current"
	if snap.Stale {
		page.SnapshotState = "stale"
	}
	page.SnapshotAge = formatAge(now.Sub(snap.ObservedAt))
	page.Generation = strconv.FormatUint(snap.Generation, 10)
	page.Cluster = buildClusterView(snap.Cluster)
	if page.Pods != nil && snap.Cluster.Present {
		page.Pods.Disagreement = buildDisagreement(snap.Cluster, s.pods)
	}
	page.Infrastructure = buildInfrastructureView(s.infra, s.infraOK, now)
	if s.backupsOK {
		page.ObjectStoreDetail = buildObjectStoreDetail(s.backups.ObjectStore)
	}
	// Derived last, so they read the finished page and can restate only
	// what the attributed sections above already carry.
	page.Summary = buildSummary(&page)
	page.Topology = buildGroupedWiring(&page)
	page.ClusterOverview = buildClusterOverview(&page)
	page.Objects = buildObjectsView(&page)
	page.BackupsDrawing = buildBackupsDrawing(&page)
	page.PoolersDrawing = buildPoolersWiring(&page)
	page.DatabasesDrawing = buildDatabasesDrawing(&page)
	return page
}

// InfrastructureView is the cluster's observed physical resources: the
// services clients dial, the claims the instances keep their data on,
// and the volume snapshots taken of them.
type InfrastructureView struct {
	// Origin attributes every claim in the section.
	Origin Origin
	// Meta is the set's own freshness.
	Meta SectionMeta
	// Services, Volumes and Snapshots are the observed sets.
	Services  []ServiceRowView
	Volumes   []VolumeRowView
	Snapshots []SnapshotRowView
	// SnapshotsObservable reports that the VolumeSnapshot API answered.
	// False states that the console did not read snapshots, which is not
	// a claim that none exist.
	SnapshotsObservable bool
	// Children are the further owned kinds — secrets, config maps,
	// disruption budgets, the RBAC triple, jobs — each reduced to a
	// name and one or two observed details.
	Children []ChildRowView
	// ChildrenUnobserved lists the child kinds that were not granted,
	// which the drawing states instead of implying none exist.
	ChildrenUnobserved []string
	// Truncated reports a display ceiling on any list.
	Truncated bool
}

// ChildRowView is one further owned object, formatted for the children
// drawing: the kind token, the name, and up to two detail lines.
type ChildRowView struct {
	// Kind is the Kubernetes kind: Secret, ConfigMap,
	// PodDisruptionBudget, ServiceAccount, Role, RoleBinding, Job.
	Kind string
	// Name is the object name.
	Name string
	// Detail is the first observed line: a secret's type, a budget's
	// constraint, a binding's role. Empty when nothing was reported.
	Detail string
	// Extra is the second line: a key count, reported headroom, a
	// job's pod counts. Empty when nothing was reported.
	Extra string
	// Age is relative to the creation time, or unknown.
	Age string
}

// ServiceRowView is one observed service.
type ServiceRowView struct {
	Name string
	// Role is the plain-language job: read-write, read-only, any
	// instance, or empty for a service whose name says nothing.
	Role string
	Type string
	// Address is the ClusterIP, "headless", or unknown.
	Address string
	// Port is the first reported port, empty when none is reported.
	Port string
	// Headless reports the explicit None address.
	Headless bool
	// ClusterIP is the raw address, empty when headless or unreported.
	ClusterIP string
	// Selector is the reported label selector.
	Selector []string
}

// VolumeRowView is one observed claim.
type VolumeRowView struct {
	Name     string
	Instance string
	// Purpose is the claim's job as the operator labels it, such as
	// PG_DATA; empty when unlabelled.
	Purpose      string
	Phase        string
	PhaseState   string
	Capacity     string
	StorageClass string
	VolumeName   string
}

// SnapshotRowView is one observed volume snapshot.
type SnapshotRowView struct {
	Name        string
	SourceClaim string
	// Ready is "true", "false" or unknown — unreported readiness is not
	// the same as not ready.
	Ready       string
	RestoreSize string
	Age         string
}

// ObjectStoreDetailView is where the backups actually go, read from the
// referenced ObjectStore rather than inferred from the Cluster.
type ObjectStoreDetailView struct {
	Origin      Origin
	Name        string
	Destination string
	Endpoint    string
	Retention   string
	// Observed reports that the store itself was read, not merely
	// referenced.
	Observed bool
}

// buildInfrastructureView converts the observed set into rows.
func buildInfrastructureView(snap observe.InfrastructureSnapshot, ok bool, now time.Time) *InfrastructureView {
	if !ok {
		return nil
	}
	view := &InfrastructureView{
		Origin:              OriginKubernetes,
		Meta:                buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		SnapshotsObservable: snap.SnapshotsObservable,
		Truncated:           snap.Truncated,
	}
	for _, s := range snap.Services {
		row := ServiceRowView{
			Name: s.Name, Role: s.Role, Type: orUnknown(s.Type),
			Headless: s.Headless, ClusterIP: s.ClusterIP,
			Selector: append([]string(nil), s.TargetSelector...),
		}
		switch {
		case s.Headless:
			row.Address = "headless"
		case s.ClusterIP != "":
			row.Address = s.ClusterIP
		default:
			row.Address = unknown
		}
		if s.Port != nil {
			row.Port = strconv.FormatInt(int64(*s.Port), 10)
		}
		view.Services = append(view.Services, row)
	}
	for _, v := range snap.Volumes {
		row := VolumeRowView{
			Name: v.Name, Instance: orUnknown(v.Instance), Purpose: v.Role,
			Phase: orUnknown(v.Phase), Capacity: orUnknown(v.Capacity),
			StorageClass: orUnknown(v.StorageClass), VolumeName: orUnknown(v.VolumeName),
		}
		row.PhaseState = unknown
		if v.Phase == "Bound" {
			row.PhaseState = "current"
		} else if v.Phase != "" {
			row.PhaseState = "degraded"
		}
		view.Volumes = append(view.Volumes, row)
	}
	for _, s := range snap.Snapshots {
		row := SnapshotRowView{
			Name: s.Name, SourceClaim: orUnknown(s.SourceClaim),
			RestoreSize: orUnknown(s.RestoreSize), Ready: unknown, Age: unknown,
		}
		if s.Ready != nil {
			row.Ready = strconv.FormatBool(*s.Ready)
		}
		if s.CreatedAt != nil {
			row.Age = formatAge(now.Sub(*s.CreatedAt))
		}
		view.Snapshots = append(view.Snapshots, row)
	}
	for _, child := range snap.Children {
		view.Children = append(view.Children, buildChildRow(child, now))
	}
	view.ChildrenUnobserved = append([]string(nil), snap.ChildrenUnobserved...)
	return view
}

// buildChildRow states one owned object's observed details in the
// drawing's two lines. A fact the snapshot does not carry has no text.
func buildChildRow(child observe.ChildFacts, now time.Time) ChildRowView {
	row := ChildRowView{Kind: child.Kind, Name: child.Name, Age: unknown}
	if child.CreatedAt != nil {
		row.Age = formatAge(now.Sub(*child.CreatedAt))
	}
	keys := func() string {
		if child.Keys == nil {
			return ""
		}
		if *child.Keys == 1 {
			return "1 key"
		}
		return strconv.Itoa(*child.Keys) + " keys"
	}
	switch child.Kind {
	case "Secret":
		row.Detail = child.SecretType
		row.Extra = keys()
	case "ConfigMap":
		row.Extra = keys()
	case "PodDisruptionBudget":
		var terms []string
		if child.MinAvailable != "" {
			terms = append(terms, "min available "+child.MinAvailable)
		}
		if child.MaxUnavailable != "" {
			terms = append(terms, "max unavailable "+child.MaxUnavailable)
		}
		row.Detail = strings.Join(terms, " · ")
		if child.DisruptionsAllowed != nil {
			row.Extra = strconv.FormatInt(int64(*child.DisruptionsAllowed), 10) + " disruptions allowed"
		}
	case "Role":
		if child.Rules != nil {
			plural := " rules"
			if *child.Rules == 1 {
				plural = " rule"
			}
			row.Detail = strconv.Itoa(*child.Rules) + plural
		}
	case "RoleBinding":
		if child.RoleRef != "" {
			row.Detail = "grants " + child.RoleRef
		}
		if child.Subjects != nil {
			plural := " subjects"
			if *child.Subjects == 1 {
				plural = " subject"
			}
			row.Extra = strconv.Itoa(*child.Subjects) + plural
		}
	case "Job":
		var counts []string
		if child.Succeeded != nil && *child.Succeeded > 0 {
			counts = append(counts, strconv.FormatInt(int64(*child.Succeeded), 10)+" succeeded")
		}
		if child.Active != nil && *child.Active > 0 {
			counts = append(counts, strconv.FormatInt(int64(*child.Active), 10)+" active")
		}
		if child.Failed != nil && *child.Failed > 0 {
			counts = append(counts, strconv.FormatInt(int64(*child.Failed), 10)+" failed")
		}
		row.Detail = strings.Join(counts, " · ")
	}
	return row
}

// buildObjectStoreDetail states where backups go, when a store is
// referenced and was read.
func buildObjectStoreDetail(ref observe.ObjectStoreReference) *ObjectStoreDetailView {
	if ref.Name == "" {
		return nil
	}
	return &ObjectStoreDetailView{
		Origin:      OriginKubernetes,
		Name:        ref.Name,
		Destination: ref.Destination,
		Endpoint:    ref.Endpoint,
		Retention:   ref.RetentionPolicy,
		Observed:    ref.State == observe.ObjectStorePresent,
	}
}

// ClusterOverviewView is the power-user screen: the Cluster resource
// with every object attached to it, and the placement spread derived
// from the same pod snapshot.
type ClusterOverviewView struct {
	// Children is the inventory drawing — the Cluster, the objects it
	// owns grouped by kind, and the objects referencing it. Nil
	// without an observed cluster.
	Children *TopologyView
	// Placement is one row per observed instance pod.
	Placement []PlacementRowView
	// PlacementNote is the derived spread statement, empty when there
	// is nothing to say.
	PlacementNote string
	// PlacementWarn marks the note as a finding: instances share a node.
	PlacementWarn bool
}

// PlacementRowView is one instance's observed scheduling facts.
type PlacementRowView struct {
	Name string
	Role string
	Node string
}

// buildClusterOverview derives the power-user screen from the assembled
// page. Nil when nothing relevant has been observed, so the route
// renders its empty state instead of an empty diagram.
func buildClusterOverview(p *Page) *ClusterOverviewView {
	view := &ClusterOverviewView{
		Children: buildClusterChildren(p),
	}
	if p.Pods != nil {
		for _, row := range p.Pods.Rows {
			view.Placement = append(view.Placement, PlacementRowView{
				Name: row.Name, Role: row.Role, Node: row.Node,
			})
		}
	}
	view.PlacementNote, view.PlacementWarn = placementNote(view.Placement)
	if view.Children == nil && len(view.Placement) == 0 {
		return nil
	}
	return view
}

// placementNote states what the observed placement actually shows: a
// shared node is a finding, a clean spread is said in one line, and
// unreported placement is counted rather than glossed over.
func placementNote(rows []PlacementRowView) (string, bool) {
	if len(rows) < 2 {
		return "", false
	}
	byNode := map[string][]string{}
	unreported := 0
	for _, r := range rows {
		if r.Node == "" || r.Node == unknown {
			unreported++
			continue
		}
		byNode[r.Node] = append(byNode[r.Node], r.Name)
	}
	for node, names := range byNode {
		if len(names) > 1 {
			return strings.Join(names, " and ") + " share node " + node +
				" — a single node failure takes more than one instance.", true
		}
	}
	if unreported > 0 {
		return fmt.Sprintf("Placement is unreported for %d of %d instances, so the spread cannot be confirmed.",
			unreported, len(rows)), false
	}
	return "No two instances share a node.", false
}

// DeclaredView is the reconciliation spine shared by every declarative
// row: what was asked for, and what the operator reports it did.
type DeclaredView struct {
	// State is the reconciliation verdict in words: applied, failed, or
	// unknown when the operator has not reported one. Unknown is not a
	// failure — a freshly created declaration has simply not been acted
	// on yet.
	State string
	// Message is the operator's reconciliation output, empty when none.
	Message string
	// Generation is the spec generation the operator last synchronized.
	Generation string
}

// buildDeclaredView restates the operator's reconciliation report.
func buildDeclaredView(d observe.Declared) DeclaredView {
	view := DeclaredView{Message: d.Message, Generation: strconv.FormatInt(d.ObservedGeneration, 10)}
	switch {
	case d.Applied == nil:
		view.State = unknown
	case *d.Applied:
		view.State = "applied"
	default:
		view.State = "failed"
	}
	return view
}

// DatabaseRowView is one declared database as displayed.
type DatabaseRowView struct {
	Name     string
	Database string
	Owner    string
	Encoding string
	Ensure   string
	Declared DeclaredView
}

// DatabaseRoleRowView is one declared role as displayed. The privilege
// columns are the declaration, never a reading of the cluster's
// catalogs: pgConsole does not speak SQL.
type DatabaseRoleRowView struct {
	Name string
	Role string
	// Attributes is the declared privilege set in words, or "none".
	Attributes string
	// ConnectionLimit is the declared limit, or "unlimited".
	ConnectionLimit string
	// InRoles is the declared membership list, or "none".
	InRoles string
	// Password states how the role authenticates, without naming or
	// reading any Secret.
	Password string
	// ValidUntil is the declared password expiry, or "no expiry".
	ValidUntil string
	Declared   DeclaredView
}

// PublicationRowView is one declared publication as displayed.
type PublicationRowView struct {
	Name        string
	Publication string
	Database    string
	// Target is "all tables" or "selected objects". The objects
	// themselves are database content and are not rendered.
	Target   string
	Declared DeclaredView
}

// SubscriptionRowView is one declared subscription as displayed.
type SubscriptionRowView struct {
	Name            string
	Subscription    string
	Database        string
	Publication     string
	ExternalCluster string
	Declared        DeclaredView
}

// DatabaseObjectsView is the declarative-objects section: four lists of
// the same kind of claim, sharing one freshness because they come from
// one merged observation.
type DatabaseObjectsView struct {
	// Origin attributes every claim in this section.
	Origin Origin
	// Meta is the section's own freshness.
	Meta SectionMeta
	// Truncated reports that any list was cut at its bound.
	Truncated     bool
	Databases     []DatabaseRowView
	Roles         []DatabaseRoleRowView
	Publications  []PublicationRowView
	Subscriptions []SubscriptionRowView
}

// buildDatabaseObjectsView converts the declarative snapshot into
// bounded display rows.
func buildDatabaseObjectsView(snap observe.DatabaseObjectsSnapshot, now time.Time) *DatabaseObjectsView {
	view := &DatabaseObjectsView{
		Origin:    OriginOperator,
		Meta:      buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Truncated: snap.Truncated,
	}
	for _, d := range snap.Databases {
		view.Databases = append(view.Databases, DatabaseRowView{
			Name: d.Name, Database: orUnknown(d.Database), Owner: orUnknown(d.Owner),
			Encoding: orUnknown(d.Encoding), Ensure: orUnknown(d.Ensure),
			Declared: buildDeclaredView(d.Declared),
		})
	}
	for _, r := range snap.Roles {
		view.Roles = append(view.Roles, DatabaseRoleRowView{
			Name: r.Name, Role: orUnknown(r.Role),
			Attributes:      roleAttributes(r),
			ConnectionLimit: connectionLimit(r.ConnectionLimit),
			InRoles:         joinOrNone(r.InRoles),
			Password:        rolePassword(r.HasPasswordSecret),
			ValidUntil:      validUntil(r.ValidUntil),
			Declared:        buildDeclaredView(r.Declared),
		})
	}
	for _, p := range snap.Publications {
		target := "selected objects"
		if p.AllTables {
			target = "all tables"
		}
		view.Publications = append(view.Publications, PublicationRowView{
			Name: p.Name, Publication: orUnknown(p.Publication), Database: orUnknown(p.Database),
			Target: target, Declared: buildDeclaredView(p.Declared),
		})
	}
	for _, sub := range snap.Subscriptions {
		view.Subscriptions = append(view.Subscriptions, SubscriptionRowView{
			Name: sub.Name, Subscription: orUnknown(sub.Subscription), Database: orUnknown(sub.Database),
			Publication: orUnknown(sub.Publication), ExternalCluster: orUnknown(sub.ExternalCluster),
			Declared: buildDeclaredView(sub.Declared),
		})
	}
	return view
}

// roleAttributes lists the declared privilege flags, or "none".
func roleAttributes(r observe.DatabaseRoleFacts) string {
	var set []string
	if r.Superuser {
		set = append(set, "superuser")
	}
	if r.CreateDB {
		set = append(set, "createdb")
	}
	if r.CreateRole {
		set = append(set, "createrole")
	}
	return joinOrNone(set)
}

// connectionLimit renders the declared limit; PostgreSQL treats a
// negative limit as unlimited.
func connectionLimit(limit int64) string {
	if limit < 0 {
		return "unlimited"
	}
	return strconv.FormatInt(limit, 10)
}

// rolePassword states how the role authenticates. It reports only that
// a Secret is referenced — never its name, and never its content.
func rolePassword(hasSecret bool) string {
	if hasSecret {
		return "from a referenced Secret"
	}
	return "not declared here"
}

// validUntil renders the declared password expiry.
func validUntil(t *time.Time) string {
	if t == nil {
		return "no expiry"
	}
	return formatTime(t)
}

// joinOrNone renders a bounded list, or the word none.
func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

// FailoverQuorumView is the failover-quorum panel. Every value is
// operator-reported: the resource is written by the primary's instance
// manager, and the console restates it without checking that
// replication is actually synchronous.
type FailoverQuorumView struct {
	// Origin attributes every claim in this panel.
	Origin Origin
	// Meta is the panel's own freshness.
	Meta SectionMeta
	// Configured reports that the cluster runs a failover quorum at all.
	// False is an observation of absence, not a missing observation.
	Configured bool
	// Method is the reported synchronous-replication method.
	Method string
	// Primary is the instance that last updated the quorum.
	Primary string
	// StandbyNumber is how many synchronous standbys a transaction waits
	// for, in words.
	StandbyNumber string
	// Standbys is the bounded list of potentially synchronous instances.
	Standbys []string
	// Truncated reports that more standbys were reported than shown.
	Truncated bool
}

// buildFailoverQuorumView converts the quorum snapshot into an
// attributed panel.
func buildFailoverQuorumView(snap observe.FailoverQuorumSnapshot, now time.Time) *FailoverQuorumView {
	view := &FailoverQuorumView{
		Origin:     OriginOperator,
		Meta:       buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Configured: snap.Quorum.Present,
	}
	if !snap.Quorum.Present {
		return view
	}
	view.Method = orUnknown(snap.Quorum.Method)
	view.Primary = orUnknown(snap.Quorum.Primary)
	view.StandbyNumber = strconv.Itoa(snap.Quorum.StandbyNumber)
	view.Standbys = append([]string(nil), snap.Quorum.Standbys...)
	view.Truncated = snap.Quorum.StandbysTruncated
	return view
}

// ImageCatalogView is the image-catalog panel: which catalog the cluster
// draws its image from, and what that catalog offers.
//
// The reference and the catalog are separate observations and are
// attributed separately. The reference is a fact about the Cluster; the
// catalog is a fact about another object that may not exist, may not be
// readable, or may be cluster-scoped and therefore outside this
// console's namespaced authority.
type ImageCatalogView struct {
	// Origin attributes the catalog content.
	Origin Origin
	// Meta is the panel's own freshness.
	Meta SectionMeta
	// Referenced reports that the cluster names a catalog at all. False
	// means the image is named directly on the cluster.
	Referenced bool
	// Kind is the referenced kind, ImageCatalog or ClusterImageCatalog.
	Kind string
	// Name is the referenced catalog's name.
	Name string
	// Major is the PostgreSQL major version drawn from the catalog.
	Major string
	// Observable reports that the console was able to look at all. A
	// namespaced catalog is always observable; a cluster-scoped one is
	// only when the deployment opted in and the ClusterRole is bound.
	Observable bool
	// Unobservable explains, when Observable is false, why the content
	// is not claimed. It never says the catalog is absent — that is a
	// different fact, carried by Found.
	Unobservable string
	// Found reports that the referenced catalog was observed.
	Found bool
	// Images is the bounded list the catalog offers, major-ascending.
	Images []CatalogImageRowView
	// Truncated reports that the catalog carried more images than shown.
	Truncated bool
}

// CatalogImageRowView is one image a catalog offers.
type CatalogImageRowView struct {
	// Major is the PostgreSQL major version, as text.
	Major string
	// Image is the image reference.
	Image string
	// Current marks the major the cluster actually draws.
	Current bool
}

// clusterCatalogKind is the kind a Cluster names when it draws its image
// from a cluster-scoped catalog.
const clusterCatalogKind = "ClusterImageCatalog"

// buildImageCatalogView resolves the cluster's catalog reference against
// the observed catalogs. Resolution happens here rather than in the
// source because the reference lives on the Cluster and can change
// without the catalog changing.
func buildImageCatalogView(snap observe.ImageCatalogsSnapshot, ref *observe.ImageCatalogRef, now time.Time) *ImageCatalogView {
	view := &ImageCatalogView{
		Origin: OriginOperator,
		Meta:   buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
	}
	if ref == nil {
		return view
	}
	view.Referenced = true
	view.Kind = orUnknown(ref.Kind)
	view.Name = orUnknown(ref.Name)
	view.Major = strconv.Itoa(ref.Major)

	var catalog observe.ImageCatalogFacts
	var found bool
	if ref.Kind == clusterCatalogKind {
		// Cluster-scoped: outside the namespaced Role, so it is read only
		// when the deployment opted in. Each way the lookup can decline
		// is stated, and none of them claims the catalog is absent unless
		// the API server said so.
		switch snap.ClusterCatalogState {
		case observe.ClusterCatalogPresent:
			view.Observable, catalog, found = true, snap.ClusterCatalog, true
		case observe.ClusterCatalogAbsent:
			view.Observable = true
		case observe.ClusterCatalogDisabled:
			view.Unobservable = "this deployment does not grant the cluster-scoped read"
		default:
			view.Unobservable = "the cluster-scoped catalog could not be read"
		}
	} else {
		view.Observable = true
		catalog, found = snap.Catalog(ref.Name)
	}
	view.Found = found
	if !view.Observable || !found {
		return view
	}
	view.Truncated = catalog.ImagesTruncated
	for _, img := range catalog.Images {
		view.Images = append(view.Images, CatalogImageRowView{
			Major:   strconv.Itoa(img.Major),
			Image:   img.Image,
			Current: img.Major == ref.Major,
		})
	}
	return view
}

// PoolerRowView is one Pooler as displayed.
type PoolerRowView struct {
	// Name is the resource name.
	Name string
	// Type is the endpoint the pooler fronts, in words.
	Type string
	// TypeToken is the same value as the operator reports it — "rw",
	// "ro", "r". The wiring diagram reads this one: it decides which
	// service a pooler is drawn against, and its readers already know
	// the vocabulary the prose above exists to explain.
	TypeToken string
	// PoolMode is the configured PgBouncer pooling mode.
	PoolMode string
	// Instances is the ready-of-desired pod count.
	Instances string
	// Phase is the operator-reported lifecycle phase.
	Phase string
	// PhaseReason is the operator's explanation, empty when none.
	PhaseReason string
	// Image is the resolved pgbouncer image.
	Image string
}

// PoolersView is the connection-pooler section. Every value is
// operator-reported: the console does not connect to PgBouncer and makes
// no claim that a pooler is actually accepting connections beyond what
// the operator says of it.
type PoolersView struct {
	// Origin attributes every claim in this section.
	Origin Origin
	// Meta is the section's own freshness.
	Meta SectionMeta
	// Truncated reports that more poolers matched than the bound.
	Truncated bool
	// Poolers is name-sorted and bounded.
	Poolers []PoolerRowView
}

// poolerEndpoint restates the pooler type in the same plain language the
// wiring diagram uses. An unrecognized value is reported as itself
// rather than guessed at.
func poolerEndpoint(t string) string {
	switch t {
	case "rw":
		return "rw — the write endpoint"
	case "ro":
		return "ro — the read endpoint"
	case "r":
		return "r — every instance"
	case "":
		return unknown
	default:
		return t
	}
}

// buildPoolersView converts the pooler snapshot into bounded display
// rows. Absence of a reported value is rendered unknown, never blank.
func buildPoolersView(snap observe.PoolersSnapshot, now time.Time) *PoolersView {
	view := &PoolersView{
		Origin:    OriginOperator,
		Meta:      buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Truncated: snap.Truncated,
	}
	for _, p := range snap.Poolers {
		view.Poolers = append(view.Poolers, PoolerRowView{
			Name:        p.Name,
			Type:        poolerEndpoint(p.Type),
			TypeToken:   p.Type,
			PoolMode:    orUnknown(p.PoolMode),
			Instances:   formatPoolerInstances(p.ReadyInstances, p.DesiredInstances),
			Phase:       orUnknown(p.Phase),
			PhaseReason: p.PhaseReason,
			Image:       orUnknown(p.Image),
		})
	}
	return view
}

// formatPoolerInstances renders "scheduled/requested ready" for a
// pooler. The scheduled count is a plain integer in the resource, so a
// zero there is a reported zero and not an absent value; the requested
// count is optional and renders unknown when the resource carried none.
func formatPoolerInstances(ready int32, desired *int32) string {
	d := unknown
	if desired != nil {
		d = strconv.FormatInt(int64(*desired), 10)
	}
	return strconv.FormatInt(int64(ready), 10) + "/" + d + " ready"
}

// buildBackupsView converts a catalog snapshot into explicitly attributed
// display values and derives last-completed age from the injected clock.
func buildBackupsView(snap observe.BackupsSnapshot, now time.Time, evidenceURL string) *BackupsView {
	view := &BackupsView{
		Origin:             OriginOperator,
		Meta:               buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		LastCompletedAge:   unknown,
		BackupsTruncated:   snap.BackupsTruncated,
		SchedulesTruncated: snap.SchedulesTruncated,
	}
	if evidenceURL != "" {
		view.EvidenceLink = &Link{Label: "Inspect repository structure in ObjectStoreViewer", URL: evidenceURL}
	}
	var latestCompleted *time.Time
	// The newest attribution wins, on the same recency the rows use:
	// the reported stop time, falling back to creation for a backup the
	// operator has not finished reporting on.
	var latestAttributed *time.Time
	for _, backup := range snap.Backups {
		if backup.SourceInstance != "" {
			when := backup.CreatedAt
			if backup.StoppedAt != nil {
				when = *backup.StoppedAt
			}
			if latestAttributed == nil || when.After(*latestAttributed) {
				t := when
				latestAttributed = &t
				view.BaseSourceInstance = backup.SourceInstance
				// The plugin reports itself as a DNS-style name; the
				// drawing has room for the leading label alone.
				view.BaseVia, _, _ = strings.Cut(backup.PluginName, ".")
			}
		}
	}
	for _, backup := range snap.Backups {
		phase := orUnknown(backup.Phase)
		if backup.Phase == "completed" {
			phase += " — operator-reported claim"
			if backup.StoppedAt != nil && (latestCompleted == nil || backup.StoppedAt.After(*latestCompleted)) {
				t := *backup.StoppedAt
				latestCompleted = &t
			}
		}
		snapshotState := "not applicable"
		if backup.Method == "volumeSnapshot" {
			snapshotState = "unknown (not collected)"
		}
		view.Rows = append(view.Rows, BackupRowView{
			Name: orUnknown(backup.Name), Phase: phase, Method: orUnknown(backup.Method),
			Started: stampOf(backup.StartedAt), Stopped: stampOf(backup.StoppedAt),
			Age: formatTimeAge(backup.StoppedAt, backup.CreatedAt, now), SnapshotState: snapshotState,
			SourceInstance: orUnknown(backup.SourceInstance),
		})
	}
	if latestCompleted != nil {
		view.LastCompletedAge = formatAge(now.Sub(*latestCompleted))
	}
	for _, schedule := range snap.ScheduledBackups {
		suspended := unknown
		if schedule.Suspended != nil {
			suspended = strconv.FormatBool(*schedule.Suspended)
		}
		view.ScheduledRows = append(view.ScheduledRows, ScheduledBackupRowView{
			Name: orUnknown(schedule.Name), Method: orUnknown(schedule.Method),
			Schedule: orUnknown(schedule.Schedule), Suspended: suspended,
			LastSchedule: stampOf(schedule.LastScheduleTime), NextSchedule: stampOf(schedule.NextScheduleTime),
		})
	}
	return view
}

// RepositoryUnavailableView is the configured-but-silent evidence
// state. It exists because "the sidecar is not wired into this
// deployment" and "the sidecar is wired and has not answered" are
// different claims, and rendering the first for the second tells an
// operator to go look at configuration that is already correct. The
// failure kind is what makes the second actionable.
type RepositoryUnavailableView struct {
	// Origin attributes the claim.
	Origin Origin
	// State is the section state token.
	State string
	// Detail names the failure kind the consumer last saw.
	Detail string
}

// buildRepositoryView re-renders the validated evidence projection into
// attributed display values. The sidecar's own staleness and the
// console's contact staleness stay separate lines; the cluster identity
// line compares the evidence's bound UID against the console's observed
// UID only — a stale observation supports historical display but never
// current agreement, and injected configuration is never a substitute.
func buildRepositoryView(status evidence.Status, cluster observe.Snapshot, clusterOK bool, now time.Time) *RepositoryView {
	report := status.Snapshot.Report
	view := &RepositoryView{
		Origin:             OriginRepository,
		Meta:               buildMeta(status.Snapshot.Generation, status.Snapshot.ObservedAt, status.Snapshot.Stale, now),
		ContactFailure:     string(status.Failure),
		Fingerprint:        orUnknown(report.Fingerprint),
		Scope:              orUnknown(report.ScopeKind) + " " + orUnknown(report.ScopeName),
		Repository:         orUnknown(report.Provider) + " " + orUnknown(report.Format),
		ProducerVersion:    orUnknown(report.ProducerVersion),
		ClusterIdentity:    repositoryClusterIdentity(report.ClusterUID, cluster, clusterOK),
		Revision:           strconv.FormatUint(report.Revision, 10),
		EvidenceGeneration: strconv.FormatUint(report.EvidenceGeneration, 10),
		Completeness:       orUnknown(report.Completeness),
		SourceStale:        report.SourceStale,
		Overall:            StateLineView{State: report.Overall.State, Code: report.Overall.Code},
		ScanCompleted:      stampOf(report.CompletedAt),
		LastAttempt:        stampOf(report.LastAttemptAt),
		Inventory:          repositoryInventory(report.Inventory),
		InventoryFailure:   report.Inventory.LastFailureCategory,
		DetailsType:        orUnknown(report.DetailsType),
	}
	for _, capability := range report.Capabilities {
		view.Capabilities = append(view.Capabilities, CapabilityRowView{
			ID:      orUnknown(capability.ID),
			Support: orUnknown(capability.Support),
			State:   StateLineView{State: capability.State, Code: capability.Code}.Display(),
		})
	}
	if report.Barman == nil {
		view.DetailsUnknown = true
		return view
	}
	view.Barman = buildBarmanSummaryView(*report.Barman, now)
	return view
}

// repositoryClusterIdentity states the observed-UID-only comparison.
func repositoryClusterIdentity(evidenceUID string, cluster observe.Snapshot, clusterOK bool) string {
	switch clusterIdentityMatch(evidenceUID, cluster, clusterOK) {
	case identityMatchCurrent:
		return "matches the observed cluster UID (current observation)"
	case identityMatchStale:
		return "matches a stale cluster observation — not current agreement"
	case identityMismatch:
		return "mismatch: evidence is bound to a different cluster incarnation"
	default:
		return "unknown: no observed cluster identity to compare against"
	}
}

// identityMatch is the typed observed-UID comparison outcome.
type identityMatch int

const (
	// identityUnknown means no observed cluster identity exists.
	identityUnknown identityMatch = iota
	// identityMatchCurrent means the evidence binds to the currently
	// observed cluster UID.
	identityMatchCurrent
	// identityMatchStale means the UIDs match but the observation is
	// stale — historical display, never current agreement.
	identityMatchStale
	// identityMismatch means the evidence binds to a different cluster
	// incarnation.
	identityMismatch
)

// clusterIdentityMatch compares the evidence's bound cluster UID with
// the console's own observation. Injected configuration is never a
// substitute for the observed UID.
func clusterIdentityMatch(evidenceUID string, cluster observe.Snapshot, clusterOK bool) identityMatch {
	if !clusterOK || !cluster.Cluster.Present || cluster.Cluster.UID == "" {
		return identityUnknown
	}
	if cluster.Cluster.UID != evidenceUID {
		return identityMismatch
	}
	if cluster.Stale {
		return identityMatchStale
	}
	return identityMatchCurrent
}

// repositoryInventory renders the allowlisted inventory counts.
func repositoryInventory(inventory evidence.InventoryFacts) string {
	if !inventory.Known {
		return unknown
	}
	return fmt.Sprintf("%s objects, %s bytes stored, %s outside the scope",
		formatCount(inventory.ObjectCount), formatCount(inventory.StoredBytes), formatCount(inventory.UnscopedObjectCount))
}

// buildBarmanSummaryView renders the recognized barman-cloud summary.
func buildBarmanSummaryView(barman evidence.BarmanFacts, now time.Time) *BarmanSummaryView {
	view := &BarmanSummaryView{
		Backups:          formatCount(barman.BackupItems) + " (" + formatCount(barman.StructurallyUsableBackups) + " structurally usable)",
		WAL:              StateLineView{State: barman.WAL.State, Code: barman.WAL.Code},
		Timeline:         StateLineView{State: barman.Timeline.State, Code: barman.Timeline.Code},
		Coverage:         StateLineView{State: barman.Coverage.State, Code: barman.Coverage.Code},
		Retention:        StateLineView{State: barman.Retention.Result.State, Code: barman.Retention.Result.Code},
		RetentionBackups: formatCount(barman.Retention.VisibleBackups) + " visible, " + formatCount(barman.Retention.StructurallyUsableBackups) + " structurally usable",
		RetentionOldest:  stampOf(barman.Retention.OldestCompletionAt),
		RetentionNewest:  stampOf(barman.Retention.NewestCompletionAt),
		RetentionMinimum: "not configured",
		LatestArchiveAge: unknown,
		Truncated:        barman.RangesTruncated || barman.DiagnosticsTruncated,
	}
	if barman.BackupStates != nil {
		view.BackupStates = fmt.Sprintf("%d healthy, %d warning, %d unhealthy, %d unknown",
			barman.BackupStates.Healthy, barman.BackupStates.Warning, barman.BackupStates.Unhealthy, barman.BackupStates.Unknown)
	}
	if barman.WALCounts != nil {
		view.WALCounts = fmt.Sprintf("%d segments, %d partial, %d history, %d backup-history, %d unknown, %d duplicate",
			barman.WALCounts.Segment, barman.WALCounts.Partial, barman.WALCounts.History,
			barman.WALCounts.BackupHistory, barman.WALCounts.Unknown, barman.WALCounts.Duplicate)
	}
	if barman.Retention.MinimumConfigured && barman.Retention.MinimumRedundancy != nil {
		view.RetentionMinimum = "minimum redundancy " + strconv.FormatUint(*barman.Retention.MinimumRedundancy, 10)
	}
	if barman.LatestArchiveReceiptAt != nil {
		view.LatestArchiveAge = formatAge(now.Sub(*barman.LatestArchiveReceiptAt))
	}
	return view
}

// formatCount renders a nullable count: nil is unknown, never zero.
func formatCount(value *uint64) string {
	if value == nil {
		return unknown
	}
	return strconv.FormatUint(*value, 10)
}

func buildObjectStoreView(ref observe.ObjectStoreReference) *ObjectStoreView {
	view := &ObjectStoreView{
		Name: orUnknown(ref.Name), ReferenceOrigin: OriginOperator,
		ObservationOrigin: OriginKubernetes,
	}
	switch ref.State {
	case observe.ObjectStorePresent:
		view.ReferenceState = "referenced"
		view.ObservationState = "object metadata observed"
	case observe.ObjectStoreNotReferenced:
		view.ReferenceState = "not referenced by the enabled Barman Cloud plugin"
		view.ObservationState = "not applicable"
	default:
		view.ReferenceState = unknown
		if ref.Name != "" {
			view.ReferenceState = "referenced"
		}
		view.ObservationState = "unknown (permission, CRD, cluster, or object unavailable)"
	}
	return view
}

func formatTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return unknown
	}
	return value.UTC().Format("2006-01-02 15:04:05Z")
}

// Stamp is one absolute moment as the console states it: the UTC text
// the server rendered, and the machine-readable form beside it.
//
// The two are separate because they answer different questions. Text is
// the claim, and it is what a reader with no script — or a printed page
// — sees, in the one spelling two operators in different places can
// compare. ISO exists only so the browser may restate that same instant
// in the reader's own zone; it is never a different fact. ISO is empty
// when there is no time at all, which is what keeps "unknown" from
// being dressed up as a date.
type Stamp struct {
	// Text is the UTC rendering, or "unknown".
	Text string
	// ISO is the RFC3339 form, or empty when there is no time.
	ISO string
}

// stampOf renders one optional instant into both forms.
func stampOf(value *time.Time) Stamp {
	if value == nil || value.IsZero() {
		return Stamp{Text: unknown}
	}
	return Stamp{Text: value.UTC().Format("2006-01-02 15:04:05Z"), ISO: value.UTC().Format(time.RFC3339)}
}

// stampAt renders one non-optional instant into both forms.
func stampAt(value time.Time) Stamp { return stampOf(&value) }

func formatTimeAge(preferred *time.Time, fallback time.Time, now time.Time) string {
	if preferred != nil && !preferred.IsZero() {
		return formatAge(now.Sub(*preferred))
	}
	if !fallback.IsZero() {
		return formatAge(now.Sub(fallback))
	}
	return unknown
}

// buildEventsView converts the event snapshot into display rows. The
// age window is re-applied against the rendering instant, and Pod-kind
// events are admitted only for verified members: without a pods
// snapshot they are withheld rather than guessed.
func buildEventsView(snap observe.EventsSnapshot, pods observe.PodsSnapshot, podsOK bool, window time.Duration, now time.Time) *EventsView {
	view := &EventsView{
		Origin:    OriginKubernetes,
		Meta:      buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Window:    window.String(),
		Truncated: snap.Truncated,
	}
	members := make(map[string]bool, len(pods.Pods))
	if podsOK {
		for _, p := range pods.Pods {
			members[p.Name] = true
		}
	}
	cutoff := now.Add(-window)
	for _, e := range snap.Events {
		if e.LastSeen.Before(cutoff) {
			continue
		}
		if e.Kind == "Pod" {
			if !podsOK {
				view.PodEventsWithheld = true
				continue
			}
			if !members[e.Object] {
				continue
			}
		}
		view.Rows = append(view.Rows, EventRowView{
			Type:    orUnknown(e.Type),
			Reason:  orUnknown(e.Reason),
			Object:  orUnknown(e.Kind) + "/" + orUnknown(e.Object),
			Message: boundMessage(e.Message),
			Count:   strconv.Itoa(e.Count),
			Age:     formatAge(now.Sub(e.LastSeen)),
		})
	}
	return view
}

// buildMeta renders a section's snapshot line.
func buildMeta(generation uint64, observedAt time.Time, stale bool, now time.Time) SectionMeta {
	state := "current"
	if stale {
		state = "stale"
	}
	return SectionMeta{
		State:      state,
		Age:        formatAge(now.Sub(observedAt)),
		Generation: strconv.FormatUint(generation, 10),
	}
}

// buildPodsView converts the pod snapshot into bounded display rows.
// With logs allowed, each member row links its bounded tail; the link
// is an affordance only — the fetch re-verifies membership live.
// buildPodsView converts a pod snapshot into display rows. logPrefix
// routes the per-pod tail link: instance pods and pooler pods are
// rendered by the same view but their tails are different routes,
// verified against different ownership chains, and reading a pooler's
// pgbouncer log through the instance route would be refused.
func buildPodsView(snap observe.PodsSnapshot, now time.Time, allowLogs bool, logPrefix string) *PodsView {
	view := &PodsView{
		Origin:      OriginKubernetes,
		Meta:        buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Truncated:   snap.Truncated,
		LogsEnabled: allowLogs,
	}
	for _, p := range snap.Pods {
		phase := orUnknown(p.Phase)
		if p.Deleting {
			phase += " (deleting)"
		}
		ready := unknown
		if p.Ready != nil {
			ready = strconv.FormatBool(*p.Ready)
		}
		logsURL := ""
		if allowLogs && p.Name != "" {
			logsURL = logPrefix + url.PathEscape(p.Name)
		}
		view.Rows = append(view.Rows, PodRowView{
			Name:     orUnknown(p.Name),
			Role:     orUnknown(p.Role),
			Phase:    phase,
			Ready:    ready,
			Restarts: orUnknownInt(p.Restarts),
			Node:     orUnknown(p.Node),
			Image:    orUnknown(p.Image),
			LogsURL:  logsURL,
		})
	}
	return view
}

// primaryRole is the observed role label value of a primary instance.
const primaryRole = "primary"

// buildDisagreement cross-references the operator's primary claim with
// the observed role labels. Both claims are rendered with their origins
// when they conflict; agreement, or either side being unreported,
// renders nothing.
func buildDisagreement(facts observe.ClusterFacts, podsSnap observe.PodsSnapshot) *DisagreementView {
	if facts.CurrentPrimary == "" {
		return nil
	}
	var observed []string
	for _, p := range podsSnap.Pods {
		if p.Role == primaryRole && !p.Deleting {
			observed = append(observed, p.Name)
		}
	}
	if len(observed) == 1 && observed[0] == facts.CurrentPrimary {
		return nil
	}
	if len(observed) == 0 {
		return &DisagreementView{
			OperatorClaim:  "current primary is " + facts.CurrentPrimary,
			OperatorOrigin: OriginOperator,
			ObservedClaim:  "no pod carries the primary role label",
			ObservedOrigin: OriginKubernetes,
		}
	}
	return &DisagreementView{
		OperatorClaim:  "current primary is " + facts.CurrentPrimary,
		OperatorOrigin: OriginOperator,
		ObservedClaim:  "primary role label on " + strings.Join(observed, ", "),
		ObservedOrigin: OriginKubernetes,
	}
}

// buildClusterView converts facts into bounded display strings.
func buildClusterView(facts observe.ClusterFacts) *ClusterView {
	if !facts.Present {
		return &ClusterView{Origin: OriginOperator, Absent: true}
	}
	view := &ClusterView{
		Origin:          OriginOperator,
		Phase:           orUnknown(facts.Phase),
		PhaseReason:     facts.PhaseReason,
		CurrentPrimary:  orUnknown(facts.CurrentPrimary),
		TargetPrimary:   orUnknown(facts.TargetPrimary),
		Instances:       formatInstances(facts.ReadyInstances, facts.DesiredInstances),
		Timeline:        orUnknownInt(facts.TimelineID),
		Image:           orUnknown(facts.Image),
		PostgresVersion: orUnknownInt(facts.PostgresMajorVersion),
	}
	for _, c := range facts.Conditions {
		view.Conditions = append(view.Conditions, ConditionView{
			Type:    orUnknown(c.Type),
			Status:  orUnknown(c.Status),
			Reason:  orUnknown(c.Reason),
			Message: boundMessage(c.Message),
		})
	}
	return view
}

// buildLinks keeps only configured link-outs.
func buildLinks(links Links) []Link {
	var out []Link
	for _, l := range []Link{
		// The product name alone. These are sidebar entries beside
		// Overview and Cluster, and the template appends "(new tab)" to
		// build each title, so a parenthetical in the label itself lands
		// as "SQL console (pgAdmin) (new tab)". The sibling tools are
		// named the same way everywhere else in the console.
		{Label: "ObjectStoreViewer", URL: links.ObjectStoreViewer},
		{Label: "pgAdmin", URL: links.PgAdmin},
		{Label: "Monitoring", URL: links.Monitoring},
	} {
		if l.URL != "" {
			out = append(out, l)
		}
	}
	return out
}

// orUnknown renders an unreported string fact.
func orUnknown(s string) string {
	if s == "" {
		return unknown
	}
	return s
}

// orUnknownInt renders an unreported numeric fact.
func orUnknownInt(v *int) string {
	if v == nil {
		return unknown
	}
	return strconv.Itoa(*v)
}

// formatInstances renders "ready/desired", tolerating either side being
// unreported.
func formatInstances(ready, desired *int) string {
	if ready == nil && desired == nil {
		return unknown
	}
	r, d := unknown, unknown
	if ready != nil {
		r = strconv.Itoa(*ready)
	}
	if desired != nil {
		d = strconv.Itoa(*desired)
	}
	return r + "/" + d + " ready"
}

// boundMessage truncates a message to the display bound, rune-safe.
func boundMessage(s string) string {
	runes := []rune(s)
	if len(runes) <= maxDisplayMessage {
		return s
	}
	return string(runes[:maxDisplayMessage]) + "…"
}

// formatAge renders a coarse, human age. Negative values — a clock skew
// artifact — render as "0s" rather than a nonsense negative age.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
