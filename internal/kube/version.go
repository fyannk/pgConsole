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
	"context"
	"encoding/json"

	"k8s.io/apimachinery/pkg/version"
)

// maxGitVersion bounds the reported version string, which is server
// free text as far as this console is concerned.
const maxGitVersion = 64

// FetchServerVersion reads the API server's /version endpoint and
// returns its gitVersion. The raw request rather than the discovery
// helper, so the call carries a context and the usual timeout.
//
// /version is a non-resource URL every authenticated principal may
// read; no grant in the example Roles is involved.
func (c *Client) FetchServerVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	raw, err := c.typed.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil {
		return "", categorize("server version", err)
	}
	var info version.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", categorize("server version decode", err)
	}
	gitVersion := info.GitVersion
	if len(gitVersion) > maxGitVersion {
		gitVersion = gitVersion[:maxGitVersion]
	}
	return gitVersion, nil
}
