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

package diagnose

// Every snapshot a check reads carries its own staleness: the collector
// lost contact, and what it holds is the retained last-good observation.
// Stale data is fine to display with its age beside it. It is not fine
// to clear a check with, because "nothing matched in what was last seen"
// is not "nothing is wrong now" — and a match found in it is a claim
// about the past dressed as the present. So a stale source reads as
// unavailable, exactly as an unobserved one does, and the check says
// which of the two it was.
//
// Each source answers the question once, here, so a condition and a
// hand-written detector reading the same snapshot refuse it for the same
// reason. A new condition over one of these sources starts with the
// matching helper; a condition over a new source adds one.

// eventsUnavailable is the reason the event list cannot be read, empty
// when it can.
func eventsUnavailable(in Input) string {
	switch {
	case !in.HasEvents:
		return "events have not been observed yet"
	case in.Events.Stale:
		return "the event list is stale, so current events are unknown"
	}
	return ""
}

// podsUnavailable is the reason the instance pods cannot be read, empty
// when they can.
func podsUnavailable(in Input) string {
	switch {
	case !in.HasPods:
		return "instance pods have not been observed yet"
	case in.Pods.Stale:
		return "the instance pods are stale, so current container states are unknown"
	}
	return ""
}

// poolerPodsUnavailable is the reason the pooler pods cannot be read,
// empty when they can. Absence is not a reason: poolers are optional,
// and a cluster without them has nothing to observe. Only a snapshot
// that exists and has gone stale withholds a result.
func poolerPodsUnavailable(in Input) string {
	if in.HasPoolerPods && in.PoolerPods.Stale {
		return "the pooler pods are stale, so current container states are unknown"
	}
	return ""
}

// clusterUnavailable is the reason the Cluster's status cannot be read,
// empty when it can. Staleness is checked before presence: a stale
// snapshot that says "no Cluster" is not evidence that there is none.
func clusterUnavailable(in Input) string {
	switch {
	case !in.HasCluster:
		return "the Cluster has not been observed yet"
	case in.Cluster.Stale:
		return "the Cluster snapshot is stale, so its current status is unknown"
	case !in.Cluster.Cluster.Present:
		return "the API server reports no Cluster object"
	}
	return ""
}

// poolersUnavailable is the reason the Pooler set cannot be read, empty
// when it can. An empty set is readable: a cluster with no poolers has
// nothing to be short of.
func poolersUnavailable(in Input) string {
	switch {
	case !in.HasPoolers:
		return "poolers have not been observed yet"
	case in.Poolers.Stale:
		return "the poolers are stale, so current instance counts are unknown"
	}
	return ""
}

// quorumUnavailable is the reason the failover quorum cannot be read,
// empty when it can. Absence of the resource is readable — it is the
// operator's way of saying the cluster runs no quorum.
func quorumUnavailable(in Input) string {
	switch {
	case !in.HasFailoverQuorum:
		return "the failover quorum has not been observed yet"
	case in.FailoverQuorum.Stale:
		return "the failover quorum is stale, so the current standby set is unknown"
	}
	return ""
}

// imageCatalogsUnavailable is the reason the image catalogs cannot be
// read, empty when they can.
func imageCatalogsUnavailable(in Input) string {
	switch {
	case !in.HasImageCatalogs:
		return "image catalogs have not been observed yet"
	case in.ImageCatalogs.Stale:
		return "the image catalogs are stale, so their current content is unknown"
	}
	return ""
}

// evidenceUnavailable is the reason the repository-evidence report cannot
// be read, empty when it can. The channel has more ways to be silent
// than a watch does — not configured, never answered, contact lost, no
// scan completed yet, the sidecar's own staleness against the
// repository, a details variant this consumer does not know — and each
// is named, because "could not run" is only honest with its reason.
func evidenceUnavailable(in Input) string {
	if !in.HasEvidence {
		return "the repository-evidence consumer is not configured"
	}
	status := in.Evidence
	if !status.HasReport {
		reason := "the repository-evidence sidecar has not answered yet"
		if status.Failure != "" {
			reason += " (latest poll: " + string(status.Failure) + ")"
		}
		return reason
	}
	switch {
	case status.Snapshot.Stale:
		return "contact with the repository-evidence sidecar is lost, so the retained report is not current"
	case status.Snapshot.Report.Completeness == "no-completed-scan":
		return "the repository-evidence sidecar has not completed a scan"
	case status.Snapshot.Report.SourceStale:
		return "the repository-evidence sidecar reports its own evidence as stale against the repository"
	case status.Snapshot.Report.Barman == nil:
		return "the repository report's details are of a variant this console does not recognise"
	}
	return ""
}

// historyUnavailable is the reason the object timeline cannot be read,
// empty when it can. The timeline has no staleness of its own: it is a
// record of what was observed, not a claim about now, and its bounds
// are stated on every finding counted from it instead.
func historyUnavailable(in Input) string {
	if !in.HasHistory {
		return "the object timeline is not recorded, so nothing can be counted over time"
	}
	return ""
}

// infrastructureUnavailable is the reason the cluster's volumes and
// children cannot be read, empty when they can.
func infrastructureUnavailable(in Input) string {
	switch {
	case !in.HasInfrastructure:
		return "the cluster's volumes have not been observed yet"
	case in.Infrastructure.Stale:
		return "the cluster's volumes are stale, so current phases are unknown"
	}
	return ""
}
