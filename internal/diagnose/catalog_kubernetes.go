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

// kubernetesRules are the claims about the Kubernetes cluster itself.
//
// Empty today: the API server's version is one discovery call away but
// not yet observed into a snapshot, so ComponentKubernetes stays unknown
// and a rule pinned to it could never resolve. As with cnpgRules, the
// first pinned rule should land together with the version source.
// Unpinned Kubernetes rules have a home here too — an EventMatch on a
// kubelet reason, say — when one earns its place by catching something
// the event-backed detectors do not.
func kubernetesRules() []Rule {
	return nil
}
