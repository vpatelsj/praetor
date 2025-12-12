SHELL := /bin/bash

CONTROLLER_GEN := $(shell go env GOPATH)/bin/controller-gen
IMAGE ?= apollo-deviceprocess-controller:dev
KIND_CLUSTER ?= apollo-dev

.PHONY: all fmt vet test generate manifests build tools kind-image kind-load kind-deploy kind-restart

all: fmt vet test build

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test ./...

tools: $(CONTROLLER_GEN)

$(CONTROLLER_GEN):
	cd /home/vapa/apollo/praetor && GOTOOLCHAIN=go1.22.10 go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.14.0

# Generate deepcopy and object code.
generate: tools
	$(CONTROLLER_GEN) object paths=./api/...

# Generate CRDs and RBAC manifests into config/.
manifests: tools
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd \
		paths=./api/... \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac

build:
	GO111MODULE=on go build -o bin/apollo-deviceprocess-controller ./controller
	GO111MODULE=on go build -o bin/apollo-deviceprocess-agent ./agent

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
