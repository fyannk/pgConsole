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

// Command evidence-fingerprint prints the repository destination
// fingerprint for an S3 backup destination.
//
// It exists for local development. In a real deployment pgtoolbox
// computes this value and sets REPOSITORY_EXPECTED_FINGERPRINT from it;
// the console then refuses any evidence response that does not carry
// the same one, which is what stops a sidecar pointed at the wrong
// repository from being rendered as this cluster's.
//
// The computation is not reimplemented here. It comes from the shared
// api module — the same code ObjectStoreViewer runs when it stamps a
// response — because a fingerprint the two sides compute differently
// would be worse than no fingerprint at all: it would fail closed
// forever with nothing to point at.
package main

import (
	"flag"
	"fmt"
	"os"

	evidenceapi "github.com/fyannk/pgObjectStoreViewer/api/evidence/v1alpha1"
)

func main() {
	var input evidenceapi.S3FingerprintInput
	flag.StringVar(&input.Endpoint, "endpoint", "", "credential-free S3 endpoint origin, empty for the AWS default")
	flag.StringVar(&input.Region, "region", "", "S3 region, exactly as the viewer is configured with it")
	flag.StringVar(&input.Bucket, "bucket", "", "destination bucket")
	flag.StringVar(&input.Prefix, "prefix", "", "destination prefix inside the bucket")
	flag.StringVar(&input.ScopeName, "server", "", "the Barman server name")
	flag.Parse()

	input.Format = "barman-cloud"
	input.ScopeKind = "barman-server"

	fingerprint, err := evidenceapi.FingerprintS3(input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evidence-fingerprint:", err)
		os.Exit(1)
	}
	fmt.Println(fingerprint)
}
