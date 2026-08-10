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

import (
	"fmt"
	"strconv"
	"strings"
)

// Component names one versioned part of the stack a catalog rule can pin
// itself to. A rule's claim — "this log line means this thing" — is only
// ever true of particular releases, so the pin is part of the claim, not
// metadata on it.
type Component string

const (
	// ComponentCNPG is the CloudNativePG operator.
	ComponentCNPG Component = "CloudNativePG"
	// ComponentPostgreSQL is the database itself. Only the major version
	// is operator-reported, so pins against it compare majors.
	ComponentPostgreSQL Component = "PostgreSQL"
	// ComponentBarman is the Barman Cloud plugin, observed through the
	// sidecar it ships into each instance pod.
	ComponentBarman Component = "Barman Cloud plugin"
	// ComponentKubernetes is the cluster the console runs against.
	ComponentKubernetes Component = "Kubernetes"
)

// ComponentVersion is one observed version fact together with its
// provenance, so a finding gated on it can quote where the number came
// from rather than asserting it.
type ComponentVersion struct {
	// Version is the observed version as dotted numerals, such as "17"
	// or "0.6.0".
	Version string
	// Origin, Object and Detail carry the provenance in Evidence form.
	Origin string
	Object string
	Detail string
}

// evidence quotes the version fact beneath a finding that rests on it.
func (v ComponentVersion) evidence() Evidence {
	return Evidence{Origin: v.Origin, Object: v.Object, Detail: v.Detail}
}

// VersionFacts is every component version the console has observed. A
// component absent from the map is unknown, and unknown is a first-class
// answer: a rule pinned to it reports that it could not be evaluated,
// never that it is clear and never that it matched.
type VersionFacts map[Component]ComponentVersion

// versionFacts derives the observed versions from the published
// snapshots. It is a pure function of the input, like everything else in
// this package: versions are observed, never configured, because an
// injected version the cluster does not actually run would silently gate
// every pinned rule wrong.
//
// Two components are derivable today. The CloudNativePG operator runs
// outside this console's namespaced authority and Kubernetes' own server
// version is not in any snapshot, so both stay unknown until a source
// observes them — which the framework treats as "could not evaluate",
// not as an error.
func versionFacts(in Input) VersionFacts {
	facts := VersionFacts{}

	if in.HasCluster && in.Cluster.Cluster.Present && in.Cluster.Cluster.PostgresMajorVersion != nil {
		major := *in.Cluster.Cluster.PostgresMajorVersion
		facts[ComponentPostgreSQL] = ComponentVersion{
			Version: strconv.Itoa(major),
			Origin:  "operator-reported",
			Object:  "Cluster",
			Detail:  fmt.Sprintf("status reports PostgreSQL major version %d", major),
		}
	}

	if in.HasPods {
		if pod, container, version, ok := barmanSidecarVersion(in); ok {
			facts[ComponentBarman] = ComponentVersion{
				Version: version,
				Origin:  "console-parsed from Kubernetes-observed image",
				Object:  fmt.Sprintf("Pod/%s container %s", pod, container.Name),
				Detail:  fmt.Sprintf("sidecar image %q", container.Image),
			}
		}
	}

	return facts
}

// barmanSidecarContainer is the container name the Barman Cloud plugin
// ships its sidecar under.
const barmanSidecarContainer = "plugin-barman-cloud"

// barmanSidecarVersion finds the plugin sidecar on any instance pod and
// parses its image tag. The first pod carrying one wins: the operator
// rolls the sidecar with the cluster, so a mixed set is transient and
// any member is as good an answer as another.
func barmanSidecarVersion(in Input) (pod string, container ContainerRef, version string, ok bool) {
	for _, p := range in.Pods.Pods {
		for _, c := range p.Containers {
			if c.Name != barmanSidecarContainer {
				continue
			}
			if v, parsed := imageTagVersion(c.Image); parsed {
				return p.Name, ContainerRef{Name: c.Name, Image: c.Image}, v, true
			}
		}
	}
	return "", ContainerRef{}, "", false
}

// ContainerRef names a container and its image for provenance.
type ContainerRef struct {
	Name  string
	Image string
}

// imageTagVersion extracts a dotted numeric version from an image tag:
// "ghcr.io/x/y:v0.6.0" yields "0.6.0", a digest-only or tagless image
// yields nothing. Only the leading numeric part of the tag counts —
// "17.5-bookworm" yields "17.5" — because everything after it names a
// build variant, not a version.
func imageTagVersion(image string) (string, bool) {
	// A digest pins content, not a version.
	if at := strings.IndexByte(image, '@'); at >= 0 {
		image = image[:at]
	}
	colon := strings.LastIndexByte(image, ':')
	if colon < 0 || strings.ContainsRune(image[colon:], '/') {
		return "", false
	}
	tag := strings.TrimPrefix(image[colon+1:], "v")
	if fields, ok := parseVersion(tag); ok {
		return joinVersion(fields), true
	}
	return "", false
}

// parseVersion reads the leading dotted numerals of a version string:
// "1.26.1" is [1 26 1], "17.5-bookworm" is [17 5], "latest" is nothing.
func parseVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(s, "v")
	var fields []int
	for {
		digits := 0
		for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return nil, false
		}
		n, err := strconv.Atoi(s[:digits])
		if err != nil {
			return nil, false
		}
		fields = append(fields, n)
		s = s[digits:]
		if !strings.HasPrefix(s, ".") {
			return fields, true
		}
		s = s[1:]
	}
}

// joinVersion renders parsed fields back to dotted numerals.
func joinVersion(fields []int) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = strconv.Itoa(f)
	}
	return strings.Join(parts, ".")
}

// compareVersions orders two parsed versions numerically, treating a
// missing field as zero so "1.26" and "1.26.0" are equal.
func compareVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// satisfies reports whether a version meets a constraint.
//
// A constraint is space-separated clauses that must all hold, each an
// operator and a version: ">=1.24", "<1.26", "=17", "!=1.25.0". The
// empty constraint holds for every version. A malformed clause or an
// unparseable version fails closed — the rule does not apply — because a
// version-pinned claim evaluated against a version it cannot read would
// be asserting an applicability nobody established.
func satisfies(version, constraint string) bool {
	v, ok := parseVersion(version)
	if !ok {
		return false
	}
	for _, clause := range strings.Fields(constraint) {
		op := "="
		rest := clause
		for _, candidate := range [...]string{">=", "<=", "!=", ">", "<", "="} {
			if strings.HasPrefix(clause, candidate) {
				op, rest = candidate, clause[len(candidate):]
				break
			}
		}
		bound, ok := parseVersion(rest)
		if !ok {
			return false
		}
		cmp := compareVersions(v, bound)
		holds := false
		switch op {
		case ">=":
			holds = cmp >= 0
		case "<=":
			holds = cmp <= 0
		case ">":
			holds = cmp > 0
		case "<":
			holds = cmp < 0
		case "=":
			holds = cmp == 0
		case "!=":
			holds = cmp != 0
		}
		if !holds {
			return false
		}
	}
	return true
}
