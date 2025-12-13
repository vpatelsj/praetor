SHELL := /bin/bash

CONTROLLER_GEN := $(shell go env GOPATH)/bin/controller-gen
CONTROLLER_GEN_VERSION ?= v0.14.0
IMAGE ?= apollo-deviceprocess-controller:dev
KIND_CLUSTER ?= apollo-dev
NAMESPACE ?= default

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -X github.com/apollo/praetor/pkg/version.Version=$(VERSION) -X github.com/apollo/praetor/pkg/version.Commit=$(COMMIT)

.PHONY: all fmt vet test generate manifests build tools install deploy kind-image kind-load kind-deploy kind-restart kind-clean clean

all: fmt vet test build

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

tools: $(CONTROLLER_GEN)

$(CONTROLLER_GEN):
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

# Generate deepcopy and object code.
generate: tools
	$(CONTROLLER_GEN) object paths=./api/...

# Generate CRDs into config/.
manifests: tools
	$(CONTROLLER_GEN) crd \
		paths=./api/... \
		output:crd:artifacts:config=config/crd/bases

build:
	go build -ldflags "$(LDFLAGS)" -o bin/apollo-deviceprocess-controller ./controller
	go build -ldflags "$(LDFLAGS)" -o bin/apollo-deviceprocess-agent ./agent
	go build -ldflags "$(LDFLAGS)" -o bin/apollo-deviceprocess-gateway ./cmd/gateway

# Apply CRDs
install: manifests
	kubectl apply -f config/crd/bases

# Deploy controller using kustomize and override image/namespace
deploy: install
	kubectl apply -k config/default
	kubectl -n $(NAMESPACE) set image deploy/apollo-deviceprocess-controller manager=$(IMAGE)
	kubectl -n $(NAMESPACE) rollout status deploy/apollo-deviceprocess-controller

# Build controller container image (local)
kind-image:
	docker build -f Dockerfile.controller -t $(IMAGE) .

# Load image into kind cluster
kind-load: kind-image
	kind load docker-image $(IMAGE) --name $(KIND_CLUSTER)

# Deploy controller to kind using current manifests and override image
kind-deploy: kind-load
	kubectl -n default delete deploy apollo-deviceprocess-controller --ignore-not-found
	kubectl apply -k config/default
	kubectl -n default set image deploy/apollo-deviceprocess-controller manager=$(IMAGE)
	kubectl -n default rollout status deploy/apollo-deviceprocess-controller

# Delete and recreate deployment using the current image (same as kind-deploy)
kind-restart: kind-deploy

# Delete all deployed resources from the default kustomize overlay
kind-clean:
	kubectl delete -k config/default --ignore-not-found

# Remove local build artifacts
clean:
	rm -rf bin
