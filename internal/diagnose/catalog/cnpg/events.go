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

// eventRules cover the refusals the operator records as events on the
// Cluster object. Only Cluster and member-pod events are in the
// observed window; what the operator writes on Backup objects surfaces
// through the backup rules instead.
func eventRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			ID:        "cnpg-primary-status-check",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "a Ready primary whose status check the operator cannot pass",
			Summary:   "The primary looks Ready but the operator's own status check on it fails, and failover is deliberately deferred.",
			Detail: "The operator short-circuits reconciliation in this state: it will not " +
				"fail over until Kubernetes itself marks the primary not Ready. A " +
				"cluster can sit here indefinitely looking almost healthy.",
			When:      diagnose.EventMatch{Reasons: []string{"PrimaryStatusCheckFailed"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			// The primary lease is 1.30 machinery; older operators
			// arbitrate the primary through status alone.
			ID:        "cnpg-primary-lease-conflict",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(only130),
			Severity:  diagnose.SeverityCritical,
			Describes: "a primary lease owned by something else",
			Summary:   "The primary lease is controlled by another owner, and the operator refuses to adopt it.",
			Detail: "Usually a leftover lease from a deleted cluster with the same name. " +
				"Reconciliation errors out on every loop until the lease is removed.",
			When:      diagnose.EventMatch{Reasons: []string{"PrimaryLeaseConflict"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-scale-down-refused",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a scale-down the operator reverted",
			Summary:   "The requested instance count conflicts with maxSyncReplicas, and the operator reverted it.",
			Detail: "The operator wrote the old count back into the spec, so the scale-down " +
				"silently never happens until maxSyncReplicas is lowered first.",
			When:      diagnose.EventMatch{Reasons: []string{"NoScaleDown"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-ca-secret-unusable",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "a referenced CA secret that is missing or unparseable",
			Summary:   "A CA secret this cluster references is missing or malformed, and PKI reconciliation is stopped.",
			When:      diagnose.EventMatch{Reasons: []string{"SecretNotFound", "InvalidCASecret"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			ID:        "cnpg-ca-expiring",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a user-supplied CA approaching expiry",
			Summary:   "A user-supplied CA certificate is close to expiring.",
			Detail: "The operator does not rotate user-supplied CAs. When this one lapses, " +
				"TLS between the operator, the instances, and clients breaks with it.",
			When:      diagnose.EventMatch{Reasons: []string{"SecretIsExpiring"}},
			Link:      "/cluster/overview",
			LinkLabel: "Cluster overview",
		},
		{
			// The event first appears in 1.29; a 1.28 operator fails the
			// reconcile without recording it.
			ID:        "cnpg-service-account-missing",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since129),
			Severity:  diagnose.SeverityCritical,
			Describes: "a specified ServiceAccount that does not exist",
			Summary:   "The ServiceAccount named in the spec does not exist, so pods cannot be created.",
			When:      diagnose.EventMatch{Reasons: []string{"ServiceAccountNotFound"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-bootstrap-backup-missing",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "a bootstrap recovery pointing at a Backup that does not exist",
			Summary:   "Bootstrap-from-recovery references a Backup object that is missing, so the primary is never created.",
			When:      diagnose.EventMatch{Reasons: []string{"ErrorNoBackup"}},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
		{
			ID:        "cnpg-manager-upgrade-failed",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "an in-place instance-manager upgrade that failed",
			Summary:   "The in-place instance-manager upgrade failed, so pods are still running the old binary.",
			When:      diagnose.EventMatch{Reasons: []string{"InstanceManagerUpgradeFailed"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "cnpg-retention-failed",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a backup retention policy that failed to prune",
			Summary:   "The backup retention policy failed, so the object store keeps growing.",
			When:      diagnose.EventMatch{Reasons: []string{"RetentionPolicyFailed"}},
			Link:      "/backups",
			LinkLabel: "Backups",
		},
	}
}
