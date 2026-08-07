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

package kube

import (
	"testing"
)

// TestConvertSecretKeepsMetadataOnly proves the security property the
// children observation rests on: a Secret is reduced to its name, type
// and key count, and no key name and no value survives into the facts.
func TestConvertSecretKeepsMetadataOnly(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	controller := true
	content := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "orders-app", "namespace": "payments", "uid": "s1",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
				"name": "orders", "uid": "c1", "controller": controller,
			}},
		},
		"type": "kubernetes.io/basic-auth",
		"data": map[string]any{
			"username": "c3VwZXJzZWNyZXQ=",
			"password": "aHVudGVyMg==",
		},
	}

	facts, member, err := c.convertSecret(content)
	if err != nil {
		t.Fatalf("convertSecret: %v", err)
	}
	if !member {
		t.Fatal("a controller-owned secret was not recognised as the cluster's")
	}
	if facts.Kind != "Secret" || facts.Name != "orders-app" || facts.SecretType != "kubernetes.io/basic-auth" {
		t.Fatalf("facts = %+v, want the metadata identity", facts)
	}
	if facts.Keys == nil || *facts.Keys != 2 {
		t.Fatalf("keys = %v, want 2", facts.Keys)
	}
	// The facts struct carries strings only where metadata belongs;
	// prove none of them took a payload value or a key name.
	for field, value := range map[string]string{
		"Name": facts.Name, "UID": facts.UID, "SecretType": facts.SecretType,
		"MinAvailable": facts.MinAvailable, "MaxUnavailable": facts.MaxUnavailable,
		"RoleRef": facts.RoleRef,
	} {
		for _, leaked := range []string{"username", "password", "c3VwZXJzZWNyZXQ=", "aHVudGVyMg=="} {
			if value == leaked {
				t.Errorf("facts field %s carries secret material %q", field, leaked)
			}
		}
	}
}

// TestConvertSecretRejectsForeignAndUncontrolledOwners proves the
// membership proof: a secret owned by another cluster, or merely
// labelled without a controller reference, is not this cluster's.
func TestConvertSecretRejectsForeignAndUncontrolledOwners(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	controller := true
	foreign := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "other-app", "namespace": "payments", "uid": "s2",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
				"name": "payments-db", "uid": "c2", "controller": controller,
			}},
		},
		"type": "Opaque",
	}
	if _, member, _ := c.convertSecret(foreign); member {
		t.Error("another cluster's secret was claimed")
	}

	unowned := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{
			"name": "minio-creds", "namespace": "payments", "uid": "s3",
			"labels": map[string]any{"cnpg.io/cluster": "orders"},
		},
		"type": "Opaque",
	}
	if _, member, _ := c.convertSecret(unowned); member {
		t.Error("a merely-labelled secret was claimed without an owner reference")
	}
}
