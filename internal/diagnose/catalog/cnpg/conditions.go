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

package cnpg

import "github.com/fyannk/pgConsole/internal/diagnose"

// conditionRules cover the conditions the operator and the instance
// managers write on the Cluster. These carry failure text the phase
// does not: ContinuousArchiving=False quotes the archiver's actual
// error, and LastBackupSucceeded=False quotes the backup's.
func conditionRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			// The flagship rule of this package. Archiving failure does
			// not make the cluster unready — it reports healthy while WAL
			// accumulates, until the volume fills and the operator stops
			// PostgreSQL (the cnpg-wal-disk-space-phase rule).
			ID:        "cnpg-wal-archiving-failing",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "the instance manager reporting continuous archiving as failing",
			Summary:   "WAL archiving is failing, so WAL is accumulating and no new recovery points are being made.",
			Detail: "The cluster keeps reporting healthy while this holds: archiving does " +
				"not affect readiness. Left alone it fills the WAL volume and the " +
				"operator then keeps PostgreSQL down for lack of disk. The quoted " +
				"message is the archiver's own error — typically credentials, the " +
				"destination, or the plugin sidecar.",
			When: diagnose.ClusterCondition{Type: "ContinuousArchiving", Status: "False"},
			NextSteps: "Read the quoted archiver error: wrong credentials and a bad " +
				"destination are fixed in the object-store configuration, a " +
				"non-empty destination needs a different serverName or an emptied " +
				"folder, and a missing plugin sidecar needs a pod rollout. Archiving " +
				"must succeed before the WAL volume fills; once it does, the " +
				"operator archives the backlog on its own.",
			// The condition is the cluster's; its causes are read from one
			// instance's log, so the relation is cluster-wide: whichever
			// instance archived, the refusal it logged is the reason.
			ConsequenceOf: []diagnose.Relation{
				{Cause: "wal-archive-not-empty"}, {Cause: "object-store-denied"},
				{Cause: "object-store-forbidden"}, {Cause: "object-store-unreachable"},
				{Cause: "backup-destination-conflict"}, {Cause: "cnpg-wal-archive-plugin-missing"},
			},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-last-backup-failed",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "the last backup having failed",
			Summary:   "The most recent backup failed, and the quoted message says why.",
			Detail: "Matched by reason, because this condition also goes False when a " +
				"backup merely starts; only reason LastBackupFailed is a failure.",
			When: diagnose.ClusterCondition{
				Type: "LastBackupSucceeded", Status: "False", Reason: "LastBackupFailed"},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-system-id-mismatch",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "instances reporting different PostgreSQL system identifiers",
			Summary:   "The instances do not belong to the same PostgreSQL cluster.",
			Detail: "Two system identifiers means one instance was initialised elsewhere — " +
				"a volume from another cluster reattached, or a restore gone wrong. " +
				"Replication between them can never converge; the odd instance's data " +
				"volume has to go so it can be re-cloned.",
			When: diagnose.ClusterCondition{
				Type: "ConsistentSystemID", Status: "False", Reason: "Mismatch"},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-hibernation-blocked",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a requested hibernation waiting on cluster health",
			Summary:   "Hibernation was requested but will not start while the cluster is unhealthy.",
			Detail: "The operator only hibernates a healthy cluster, so this waits on " +
				"whatever else is wrong — fix that first and hibernation proceeds.",
			When: diagnose.ClusterCondition{
				Type: "cnpg.io/hibernation", Status: "False", Reason: "WaitingForHealthy"},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-demotion-fencing",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "every instance fenced for a replica-cluster transition",
			Summary:   "All instances are fenced for a demotion to replica cluster: PostgreSQL is not serving anywhere.",
			Detail: "The transition to a replica cluster fences the whole cluster while it " +
				"runs. If this persists, the demotion is wedged and the fence with it — " +
				"a full outage until the transition completes or is unwound.",
			When:      diagnose.ClusterCondition{Type: "ReplicaClusterFencing", Status: "True"},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
	}
}
