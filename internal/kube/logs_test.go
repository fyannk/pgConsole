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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/fyannk/pgconsole/internal/redact"
)

// newLogsTestClient builds a Client over fake dynamic and typed
// clients, seeded with the given pods. The typed fake serves the fixed
// body "fake logs" for every log request.
func newLogsTestClient(t *testing.T, pods ...map[string]any) (*Client, *bytes.Buffer) {
	t.Helper()
	objects := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objects = append(objects, &unstructured.Unstructured{Object: p})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{podGVR: "PodList"},
		objects...,
	)
	logs := &bytes.Buffer{}
	return &Client{
		dyn:   dyn,
		typed: k8sfake.NewClientset(),
		opts: Options{
			Namespace: "payments", ClusterName: "orders",
			RequestTimeout: time.Second, LogTailLines: 200, LogTailMaxBytes: 4096,
		},
		logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}, logs
}

func TestTailLogsForVerifiedMember(t *testing.T) {
	t.Parallel()
	c, _ := newLogsTestClient(t, ownedPod("orders-1", "u1", "primary"))
	tail, err := c.TailLogs(context.Background(), "orders-1")
	if err != nil {
		t.Fatalf("TailLogs: %v", err)
	}
	if !strings.Contains(tail.Content, "fake logs") {
		t.Errorf("content = %q", tail.Content)
	}
	if tail.LineLimit != 200 || tail.ByteLimit != 4096 {
		t.Errorf("bounds not carried: %+v", tail)
	}
	if tail.TruncatedByBytes {
		t.Error("small tail flagged as byte-truncated")
	}
}

// TestTailLogsRefusesNonMember proves the live re-verification: a pod
// carrying the selection label but not owned by the cluster is refused
// as not found — indistinguishable from absence — and the exclusion is
// logged with its stable reason.
func TestTailLogsRefusesNonMember(t *testing.T) {
	t.Parallel()
	intruder := ownedPod("orders-api-1", "ux", "primary")
	delete(intruder["metadata"].(map[string]any), "ownerReferences")
	c, logs := newLogsTestClient(t, intruder)
	_, err := c.TailLogs(context.Background(), "orders-api-1")
	if redact.Categorize(err) != redact.CategoryNotFound {
		t.Fatalf("category = %v, want notfound", redact.Categorize(err))
	}
	if !strings.Contains(logs.String(), `"reason":"membership"`) {
		t.Error("membership exclusion not logged")
	}
}

func TestTailLogsAbsentPodIsNotFound(t *testing.T) {
	t.Parallel()
	c, _ := newLogsTestClient(t)
	_, err := c.TailLogs(context.Background(), "orders-9")
	if redact.Categorize(err) != redact.CategoryNotFound {
		t.Fatalf("category = %v, want notfound", redact.Categorize(err))
	}
}

// TestTailLogsRefusalMatchesAbsence proves a caller cannot distinguish
// a refused non-member from a nonexistent pod through the error.
func TestTailLogsRefusalMatchesAbsence(t *testing.T) {
	t.Parallel()
	intruder := ownedPod("orders-api-1", "ux", "primary")
	delete(intruder["metadata"].(map[string]any), "ownerReferences")
	c, _ := newLogsTestClient(t, intruder)
	_, errMember := c.TailLogs(context.Background(), "orders-api-1")
	_, errAbsent := c.TailLogs(context.Background(), "orders-9")
	if errMember.Error() != errAbsent.Error() {
		t.Fatalf("refusal %q differs from absence %q", errMember.Error(), errAbsent.Error())
	}
}
