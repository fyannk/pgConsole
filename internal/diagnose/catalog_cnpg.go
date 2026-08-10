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

import "time"

// cnpgRules are the claims about the CloudNativePG operator: the states
// in which the operator is stuck, waiting, or refusing, and why. Every
// string in this file and in catalog_cnpg_logs.go was read out of the
// operator's own source at release-1.30, which is why every rule is
// pinned to that release: the claim "this phase means this" is a claim
// about a version, and the pin states which one was actually verified.
// Verifying a newer release widens the pin; it does not remove it.
//
// The operator version itself is observed, not assumed — parsed from
// the bootstrap-controller init container the operator injects into
// every instance pod. On any other release these checks answer "does
// not apply", never a false clear and never a wrong finding.
func cnpgRules() []Rule {
	var rules []Rule
	rules = append(rules, cnpgPhaseRules()...)
	rules = append(rules, cnpgConditionRules()...)
	rules = append(rules, cnpgEventRules()...)
	rules = append(rules, cnpgMetricRules()...)
	rules = append(rules, cnpgBackupRules()...)
	rules = append(rules, cnpgLogRules()...)
	return rules
}

// cnpg130 pins a rule to the operator release its strings were read
// from.
var cnpg130 = []Requirement{{Component: ComponentCNPG, Constraint: ">=1.30 <1.31"}}

// cnpgPhaseRules cover the phases in which the operator has stopped
// reconciling and said so. The phase strings are the operator's own
// constants; the phase reason — quoted in the evidence — carries the
// specific cause, which for several of these phases is the only place
// that cause is written at all.
func cnpgPhaseRules() []Rule {
	return []Rule{
		{
			ID:        "cnpg-unrecoverable",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "the operator declaring the cluster unrecoverable",
			Summary:   "The operator has declared the cluster unrecoverable: it will not repair this on its own.",
			Detail: "This phase has four distinct causes, and the quoted reason says which: " +
				"every instance's PVCs are gone (the data is lost and only a restore from " +
				"backup recreates the cluster); a bootstrap or replica-creation job " +
				"exhausted its retries (the job's own logs carry the error); no pod is " +
				"active at all; or a replica-promotion token does not match this cluster.",
			When:      ClusterPhase{AnyOf: []string{"Cluster is unrecoverable and needs manual intervention"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-invalid-definition",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a cluster definition the operator's validation rejected",
			Summary:   "The cluster definition is invalid, so the operator is not reconciling it.",
			Detail: "The quoted reason is the validation message. Nothing changes until the " +
				"spec is corrected.",
			When:      ClusterPhase{AnyOf: []string{"Invalid cluster definition"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-cannot-create-objects",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "the operator failing to create the cluster's auxiliary objects",
			Summary:   "The operator cannot create objects this cluster needs.",
			Detail: "The quoted reason carries the API server's refusal — typically operator " +
				"RBAC, a namespace quota, or a conflicting pre-existing object.",
			When:      ClusterPhase{AnyOf: []string{"Unable to create required cluster objects"}},
			Link:      "/objects",
			LinkLabel: "Objects",
		},
		{
			ID:        "cnpg-unknown-plugin",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a required CNPG-I plugin the operator cannot find",
			Summary:   "A plugin this cluster requires is not loaded, and reconciliation is fully stopped.",
			Detail: "The operator checks plugins before anything else, so while this phase " +
				"holds nothing runs: no failover, no rollout, no backup. The plugin name " +
				"in the quoted reason comes from spec.plugins or an external cluster's " +
				"plugin stanza; a plugin absent from the operator's registration is " +
				"usually not installed, not exposed as a service, or misspelled.",
			When: ClusterPhase{AnyOf: []string{
				"Cluster cannot proceed to reconciliation due to an unknown plugin being required"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-plugin-failure",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a CNPG-I plugin interaction failing during reconciliation",
			Summary:   "The operator cannot talk to a plugin this cluster requires, and reconciliation is stopped.",
			When: ClusterPhase{AnyOf: []string{
				"Cluster cannot proceed to reconciliation due to an error while interacting with plugins"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-image-catalog-unusable",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "an image catalog the operator cannot resolve an image from",
			Summary:   "The operator cannot pick a container image from the referenced catalog.",
			Detail: "No image means nothing can roll: pods keep their current image and a " +
				"pending upgrade cannot start. The quoted reason names what is missing — " +
				"usually the catalog itself, or an entry for the requested major version.",
			When:      ClusterPhase{AnyOf: []string{"Cluster has incomplete or invalid image catalog"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-arch-binary-missing",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "an online instance-manager upgrade blocked by a missing architecture binary",
			Summary:   "The operator image carries no instance-manager binary for this node architecture, so the online upgrade cannot run.",
			When: ClusterPhase{AnyOf: []string{
				"Cluster cannot execute instance online upgrade due to missing architecture binary"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-waiting-for-user",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "the operator waiting for a supervised switchover",
			Summary:   "The operator is waiting for a manual switchover and will not proceed on its own.",
			Detail: "primaryUpdateStrategy is supervised and a change is pending on the " +
				"primary — a rollout, or a decrease of a hot-standby-sensitive parameter. " +
				"Nothing is broken and nothing resumes until the switchover is performed " +
				"or the strategy is set to unsupervised.",
			When:      ClusterPhase{AnyOf: []string{"Waiting for user action"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-upgrade-delayed",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityNote,
			Describes: "an upgrade the operator is configured to delay",
			Summary:   "An upgrade is pending but the operator is configured to delay it.",
			When:      ClusterPhase{AnyOf: []string{"Cluster upgrade delayed"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-wal-disk-space-phase",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "the operator refusing to run PostgreSQL for lack of WAL disk space",
			Summary:   "One or more instances have no disk space left for WAL, and PostgreSQL is being kept down.",
			Detail: "This is the end state of a filling WAL volume — most often WAL " +
				"archiving having failed for long enough to fill it. The volume must " +
				"grow, or the archiving failure that filled it must be fixed.",
			When:      ClusterPhase{AnyOf: []string{"Not enough disk space"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-status-unreachable",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "the operator unable to reach any ready instance's status endpoint",
			Summary:   "The operator cannot read status from the ready instances, so it can make no decision at all.",
			Detail: "Failover, rollouts, and every other operator decision need the " +
				"instance managers' status endpoint. The operator's own message points " +
				"at network policy; a stale operator TLS certificate produces the same " +
				"state.",
			When: ClusterPhase{AnyOf: []string{
				"Instance Status Extraction Error: HTTP communication issue"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
	}
}

// cnpgConditionRules cover the conditions the operator and the instance
// managers write on the Cluster. These carry failure text the phase
// does not: ContinuousArchiving=False quotes the archiver's actual
// error, and LastBackupSucceeded=False quotes the backup's.
func cnpgConditionRules() []Rule {
	return []Rule{
		{
			// The flagship rule of this file. Archiving failure does not
			// make the cluster unready — it reports healthy while WAL
			// accumulates, until the volume fills and the operator stops
			// PostgreSQL (the cnpg-wal-disk-space-phase rule above).
			ID:        "cnpg-wal-archiving-failing",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "the instance manager reporting continuous archiving as failing",
			Summary:   "WAL archiving is failing, so WAL is accumulating and no new recovery points are being made.",
			Detail: "The cluster keeps reporting healthy while this holds: archiving does " +
				"not affect readiness. Left alone it fills the WAL volume and the " +
				"operator then keeps PostgreSQL down for lack of disk. The quoted " +
				"message is the archiver's own error — typically credentials, the " +
				"destination, or the plugin sidecar.",
			When:      ClusterCondition{Type: "ContinuousArchiving", Status: "False"},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-last-backup-failed",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "the last backup having failed",
			Summary:   "The most recent backup failed, and the quoted message says why.",
			Detail: "Matched by reason, because this condition also goes False when a " +
				"backup merely starts; only reason LastBackupFailed is a failure.",
			When:      ClusterCondition{Type: "LastBackupSucceeded", Status: "False", Reason: "LastBackupFailed"},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-system-id-mismatch",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "instances reporting different PostgreSQL system identifiers",
			Summary:   "The instances do not belong to the same PostgreSQL cluster.",
			Detail: "Two system identifiers means one instance was initialised elsewhere — " +
				"a volume from another cluster reattached, or a restore gone wrong. " +
				"Replication between them can never converge; the odd instance's data " +
				"volume has to go so it can be re-cloned.",
			When:      ClusterCondition{Type: "ConsistentSystemID", Status: "False", Reason: "Mismatch"},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-hibernation-blocked",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a requested hibernation waiting on cluster health",
			Summary:   "Hibernation was requested but will not start while the cluster is unhealthy.",
			Detail: "The operator only hibernates a healthy cluster, so this waits on " +
				"whatever else is wrong — fix that first and hibernation proceeds.",
			When:      ClusterCondition{Type: "cnpg.io/hibernation", Status: "False", Reason: "WaitingForHealthy"},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-demotion-fencing",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "every instance fenced for a replica-cluster transition",
			Summary:   "All instances are fenced for a demotion to replica cluster: PostgreSQL is not serving anywhere.",
			Detail: "The transition to a replica cluster fences the whole cluster while it " +
				"runs. If this persists, the demotion is wedged and the fence with it — " +
				"a full outage until the transition completes or is unwound.",
			When:      ClusterCondition{Type: "ReplicaClusterFencing", Status: "True"},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
	}
}

// cnpgEventRules cover the refusals the operator records as events on
// the Cluster object. Only Cluster and member-pod events are in the
// observed window; what the operator writes on Backup objects surfaces
// through the backup rules below instead.
func cnpgEventRules() []Rule {
	return []Rule{
		{
			ID:        "cnpg-primary-status-check",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a Ready primary whose status check the operator cannot pass",
			Summary:   "The primary looks Ready but the operator's own status check on it fails, and failover is deliberately deferred.",
			Detail: "The operator short-circuits reconciliation in this state: it will not " +
				"fail over until Kubernetes itself marks the primary not Ready. A " +
				"cluster can sit here indefinitely looking almost healthy.",
			When:      EventMatch{Reasons: []string{"PrimaryStatusCheckFailed"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-primary-lease-conflict",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a primary lease owned by something else",
			Summary:   "The primary lease is controlled by another owner, and the operator refuses to adopt it.",
			Detail: "Usually a leftover lease from a deleted cluster with the same name. " +
				"Reconciliation errors out on every loop until the lease is removed.",
			When:      EventMatch{Reasons: []string{"PrimaryLeaseConflict"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-scale-down-refused",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a scale-down the operator reverted",
			Summary:   "The requested instance count conflicts with maxSyncReplicas, and the operator reverted it.",
			Detail: "The operator wrote the old count back into the spec, so the scale-down " +
				"silently never happens until maxSyncReplicas is lowered first.",
			When:      EventMatch{Reasons: []string{"NoScaleDown"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-ca-secret-unusable",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a referenced CA secret that is missing or unparseable",
			Summary:   "A CA secret this cluster references is missing or malformed, and PKI reconciliation is stopped.",
			When:      EventMatch{Reasons: []string{"SecretNotFound", "InvalidCASecret"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-ca-expiring",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a user-supplied CA approaching expiry",
			Summary:   "A user-supplied CA certificate is close to expiring.",
			Detail: "The operator does not rotate user-supplied CAs. When this one lapses, " +
				"TLS between the operator, the instances, and clients breaks with it.",
			When:      EventMatch{Reasons: []string{"SecretIsExpiring"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-service-account-missing",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a specified ServiceAccount that does not exist",
			Summary:   "The ServiceAccount named in the spec does not exist, so pods cannot be created.",
			When:      EventMatch{Reasons: []string{"ServiceAccountNotFound"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-bootstrap-backup-missing",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a bootstrap recovery pointing at a Backup that does not exist",
			Summary:   "Bootstrap-from-recovery references a Backup object that is missing, so the primary is never created.",
			When:      EventMatch{Reasons: []string{"ErrorNoBackup"}},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-manager-upgrade-failed",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "an in-place instance-manager upgrade that failed",
			Summary:   "The in-place instance-manager upgrade failed, so pods are still running the old binary.",
			When:      EventMatch{Reasons: []string{"InstanceManagerUpgradeFailed"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-retention-failed",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a backup retention policy that failed to prune",
			Summary:   "The backup retention policy failed, so the object store keeps growing.",
			When:      EventMatch{Reasons: []string{"RetentionPolicyFailed"}},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
	}
}

// cnpgMetricRules cover the exporter flags whose non-zero value is a
// standing operator state — states that are otherwise only visible in
// annotations the console does not read.
func cnpgMetricRules() []Rule {
	return []Rule{
		{
			ID:        "cnpg-instance-fenced",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "an instance the operator has fenced",
			Summary:   "An instance is fenced: PostgreSQL is deliberately stopped there and the operator will not restart it.",
			Detail: "Fencing is intentional and meant to be temporary. A fenced replica is " +
				"one fewer member than the cluster's count suggests; a fenced primary " +
				"is an outage.",
			When:      InstantNonZero{Key: "fencing-on"},
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
		{
			ID:        "cnpg-switchover-required",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "an instance reporting that a manual switchover is required",
			Summary:   "An instance reports that a manual switchover is required before pending work resumes.",
			Detail: "The exporter's own word for the supervised-strategy wait, from inside " +
				"the instance — the operator-side view of the same state is the " +
				"waiting-for-user phase check.",
			When:      InstantNonZero{Key: "switchover-required"},
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
	}
}

// cnpgBackupRules read the Backup objects' own reported phases. The
// operator records its backup refusals as events on the Backup objects,
// which are outside the observed event window — but the phase those
// refusals leave behind is on the object, and the object is observed.
func cnpgBackupRules() []Rule {
	return []Rule{
		{
			ID:        "cnpg-backup-failed",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a Backup object reporting the failed phase",
			Summary:   "A backup failed and will not be retried.",
			Detail: "A failed Backup object stays failed: only a new backup — scheduled or " +
				"manual — produces the next recovery point.",
			When: BackupPhase{AnyOf: []string{"failed"}},
		},
		{
			ID:        "cnpg-backup-stuck-pending",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityWarning,
			Describes: "a Backup pending for over half an hour",
			Summary:   "A backup has been pending for over half an hour, which means the operator cannot start it.",
			Detail: "The operator parks a backup in pending and retries every 30 seconds " +
				"while its target pod is missing or not ready, or while the cluster is " +
				"not healthy enough to back up — including being hibernated. It stays " +
				"pending until the blocker clears.",
			When: BackupPhase{AnyOf: []string{"pending"}, MinAge: 30 * time.Minute},
		},
		{
			ID:        "cnpg-backup-wal-archiving",
			Component: ComponentCNPG,
			Requires:  cnpg130,
			Severity:  SeverityCritical,
			Describes: "a Backup blocked by failing WAL archiving",
			Summary:   "A backup is blocked because WAL archiving is not working.",
			Detail: "A base backup is only restorable together with its WAL, so the " +
				"operator refuses to take one while archiving fails. Fixing archiving " +
				"unblocks it.",
			When: BackupPhase{AnyOf: []string{"walArchivingFailing"}},
		},
	}
}
