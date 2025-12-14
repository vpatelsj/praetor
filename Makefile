SHELL := /bin/bash

CONTROLLER_GEN := $(shell go env GOPATH)/bin/controller-gen
CONTROLLER_GEN_VERSION ?= v0.14.0
IMAGE ?= apollo-deviceprocess-controller:dev
KIND_CLUSTER ?= apollo-dev
NAMESPACE ?= default
DOCKER_BRIDGE_IP ?= $(shell docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null)
AGENT_GATEWAY ?= http://$(DOCKER_BRIDGE_IP):18080
AGENT_NAMES ?= device-01 device-02

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -X github.com/apollo/praetor/pkg/version.Version=$(VERSION) -X github.com/apollo/praetor/pkg/version.Commit=$(COMMIT)

.PHONY: all fmt vet test generate manifests build tools crd-install controller-deploy kind-image kind-load kind-deploy kind-restart kind-clean clean gateway-deploy demo-build agents-up agents-down

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

# Generate, build, image, and load into kind (controller, gateway, agent images)
demo-build: tools generate manifests build
	docker build -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
	docker build -t apollo/gateway:dev -f Dockerfile.gateway .
	docker build -t apollo/agent:dev -f Dockerfile.agent .
	kind load docker-image apollo/controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo-deviceprocess-controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo/gateway:dev --name $(KIND_CLUSTER)

# Apply CRDs
crd-install: manifests
	kubectl apply -f config/crd/bases

# Deploy controller using kustomize and override image/namespace
controller-deploy: crd-install
	kubectl apply -k config/default
	kubectl -n $(NAMESPACE) set image deploy/apollo-deviceprocess-controller manager=$(IMAGE)
	kubectl -n $(NAMESPACE) rollout status deploy/apollo-deviceprocess-controller

# Deploy gateway RBAC, deployment, and service
gateway-deploy:
	kubectl apply -f config/gateway/rbac.yaml
	kubectl apply -f config/gateway/deployment.yaml

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

# Start systemd-capable agent containers pointing at the host gateway port-forward.
agents-up:
	@[ -n "$(DOCKER_BRIDGE_IP)" ] || (echo "docker bridge IP not found; ensure docker is running" && exit 1)
	@echo "Using agent gateway: $(AGENT_GATEWAY)"
	for dev in $(AGENT_NAMES); do \
		docker rm -f $$dev 2>/dev/null || true; \
		docker run -d --name $$dev --hostname $$dev \
		  --privileged --cgroupns=host \
		  --tmpfs /run --tmpfs /run/lock \
		  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		  -e APOLLO_GATEWAY_URL=$(AGENT_GATEWAY) \
		  -e APOLLO_DEVICE_NAME=$$dev \
		  apollo/agent:dev; \
	done

# Stop agent containers started by agents-up.
agents-down:
	for dev in $(AGENT_NAMES); do docker rm -f $$dev 2>/dev/null || true; done

# Remove local build artifacts
clean:
	rm -rf bin
