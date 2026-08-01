GO ?= go
IMAGE ?= pgconsole:dev
GOVULNCHECK_VERSION ?= v1.6.0
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
DIST_DIR ?= dist
ARTIFACT_DIR ?= artifacts
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build clean dev-up test test-race test-integration test-scale test-container test-multiarch test-e2e test-ui lint golangci-lint vuln check docs package docker-build supply-chain release-check

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/pgconsole ./cmd/pgconsole

clean:
	rm -rf bin dist artifacts release web/build web/.docusaurus web/.cache-loader

dev-up:
	./hack/dev-up.sh

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

ENVTEST_K8S_VERSION ?= 1.34.1
SETUP_ENVTEST_VERSION ?= release-0.24

test-integration:
	KUBEBUILDER_ASSETS="$$($(GO) run sigs.k8s.io/controller-runtime/tools/setup-envtest@$(SETUP_ENVTEST_VERSION) use $(ENVTEST_K8S_VERSION) -p path)" \
		$(GO) test -race -tags=integration ./internal/kube/ -count=1

test-scale:
	$(GO) test -race ./internal/observe -run '^Test(PodStoreBoundsAndFlagsTruncation|EventCollectorBoundsRetentionAndRendering|BackupStoreBoundsAndFlagsTruncation|PoolerStoreBoundsAndFlagsTruncation)$$' -count=1
	$(GO) test -race ./internal/web -run '^TestHandlerIndex(PodUnknownsAndTruncation|EventsTruncationVisible|BackupTruncationVisible)$$' -count=1

test-container: docker-build
	./hack/test-container.sh $(IMAGE)

test-multiarch:
	./hack/test-multiarch.sh

test-e2e: docker-build
	./hack/test-e2e.sh

test-ui:
	./hack/test-ui.sh

lint: golangci-lint
	test -z "$$(gofmt -l $$(find cmd internal -name '*.go' -type f))"
	$(GO) vet ./...
	./hack/check-boilerplate.sh
	./hack/check-readonly.sh
	./hack/check-deps.sh

golangci-lint:
	$(GOLANGCI_LINT) run --timeout 10m ./...

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

check: lint test test-race vuln

docs:
	cd web && npm ci && npm run typecheck && npm run build

package:
	rm -rf $(DIST_DIR)
	mkdir -p $(DIST_DIR)
	for platform in linux/amd64 linux/arm64; do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags='-s -w' \
			-o $(DIST_DIR)/pgconsole-$$os-$$arch ./cmd/pgconsole || exit 1; \
	done
	cd $(DIST_DIR) && sha256sum pgconsole-* > SHA256SUMS
	printf 'version=%s\n' '$(VERSION)' > $(DIST_DIR)/VERSION

docker-build:
	docker build --tag $(IMAGE) .

supply-chain: docker-build
	./hack/generate-supply-chain-artifacts.sh $(IMAGE) $(ARTIFACT_DIR)/release

release-check: check docs test-integration test-scale test-ui test-container test-e2e package test-multiarch supply-chain
