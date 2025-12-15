SHELL := /bin/bash

CONTROLLER_GEN := $(shell go env GOPATH)/bin/controller-gen
CONTROLLER_GEN_VERSION ?= v0.14.0
IMAGE ?= apollo-deviceprocess-controller:dev
KIND_CLUSTER ?= apollo-dev
KIND_IMAGE ?= kindest/node:v1.29.4
NAMESPACE ?= default
FORWARD_PORT ?= 18080
AGENT_GATEWAY ?= http://host.docker.internal:$(FORWARD_PORT)
AGENT_NAMES ?= device-01 device-02
DOCKER_GO_IMAGE ?= golang:1.22-alpine
DOCKER_GO_RUN = docker run --rm -v $(PWD):/workspace -w /workspace $(DOCKER_GO_IMAGE)
BUILDX_BUILDER ?= apollo-builder
DOCKER_PLATFORM ?= linux/$(shell go env GOARCH)
DOCKER_CACHE_DIR ?= .docker-cache
DOCKER_CACHE_FROM := $(shell test -d $(DOCKER_CACHE_DIR) && echo --cache-from type=local,src=$(DOCKER_CACHE_DIR))
BUILDX_AVAILABLE := $(shell docker buildx version >/dev/null 2>&1 && echo 1 || echo 0)
ifeq ($(BUILDX_AVAILABLE),1)
DOCKER_BUILD_CMD ?= DOCKER_BUILDKIT=1 docker buildx build --builder $(BUILDX_BUILDER)
DOCKER_BUILD_LOAD ?= --load
else
DOCKER_BUILD_CMD ?= DOCKER_BUILDKIT=1 docker build
DOCKER_BUILD_LOAD ?=
endif
DOCKER_CACHE_TO ?=
DOCKER_BUILD_OPTS ?= --platform $(DOCKER_PLATFORM) --build-arg BUILDKIT_INLINE_CACHE=1 $(DOCKER_BUILD_LOAD) $(DOCKER_CACHE_FROM) $(DOCKER_CACHE_TO)
KIND_INSTALL_CMD ?= brew install kind
DEMO_BUILD_TARGET ?= demo-build


VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo v0.0.0)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
LDFLAGS := -X github.com/apollo/praetor/pkg/version.Version=$(VERSION) -X github.com/apollo/praetor/pkg/version.Commit=$(COMMIT)

.PHONY: all fmt vet test generate manifests build tools crd-install controller-deploy kind-image kind-load kind-deploy kind-restart kind-clean clean gateway-deploy demo-build container-build container-demo-build ensure-kind demo-up install-crs start-device-agents monitor stop-device-agents
 .PHONY: ensure-buildx

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
demo-build: ensure-kind ensure-buildx tools generate manifests build
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/gateway:dev -f Dockerfile.gateway .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/agent:dev -f Dockerfile.agent .
	kind load docker-image apollo/controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo-deviceprocess-controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo/gateway:dev --name $(KIND_CLUSTER)

# Run generate/manifests/build inside a container (uses Go 1.22) to avoid host Go constraints.
container-build:
	$(DOCKER_GO_RUN) sh -c "apk add --no-cache bash git make && \
	 go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) && \
	 make generate manifests build CONTROLLER_GEN=/go/bin/controller-gen"

# Full demo build using containerized Go for codegen/build, then local docker/kind for images.
container-demo-build: ensure-kind ensure-buildx container-build
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/gateway:dev -f Dockerfile.gateway .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/agent:dev -f Dockerfile.agent .
	kind load docker-image apollo/controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo-deviceprocess-controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo/gateway:dev --name $(KIND_CLUSTER)

# Ensure kind is available (default install via Homebrew; override KIND_INSTALL_CMD to customize).
ensure-kind:
	@command -v kind >/dev/null 2>&1 || (echo "kind not found; installing with '$(KIND_INSTALL_CMD)'" && $(KIND_INSTALL_CMD))

# Steps 0-2 from demo.md (plus CRD/controller/gateway deploy): clean, stop agents, recreate kind cluster, build/load images, install CRDs, deploy controller and gateway.
demo-up: ensure-kind clean
	$(MAKE) stop-device-agents
	kind delete cluster --name $(KIND_CLUSTER) || true
	kind create cluster --name $(KIND_CLUSTER) --image $(KIND_IMAGE)
	kubectl cluster-info --context kind-$(KIND_CLUSTER)
	$(MAKE) $(DEMO_BUILD_TARGET)
	$(MAKE) crd-install
	$(MAKE) controller-deploy
	$(MAKE) gateway-deploy

# Steps 3-8 from demo.md: install CRDs, deploy controller/gateway, apply demo inventory and deployment, then port-forward gateway (blocking).
install-crs: crd-install controller-deploy gateway-deploy
	kubectl apply -f examples/networkswitches-demo.yaml
	kubectl apply -f examples/deviceprocessdeployment-demo.yaml
	@echo "Port-forwarding gateway on 0.0.0.0:$(FORWARD_PORT) -> 8080 (ctrl-c to stop)"
	kubectl -n $(NAMESPACE) port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 $(FORWARD_PORT):8080

# Quick monitor: list key resources
monitor:
	kubectl get deviceprocessdeployment
	kubectl get deviceprocess
	kubectl get networkswitch
	kubectl get pods

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

# Step 9: start systemd-capable agent containers pointing at the host gateway port-forward.
start-device-agents:
	@echo "Using agent gateway: $(AGENT_GATEWAY)"
	for dev in $(AGENT_NAMES); do \
		docker rm -f $$dev 2>/dev/null || true; \
		docker run -d --name $$dev --hostname $$dev \
		  --privileged --cgroupns=host \
		  --tmpfs /run --tmpfs /run/lock \
		  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
		  -e APOLLO_GATEWAY_URL=$(AGENT_GATEWAY) \
		  -e APOLLO_DEVICE_NAME=$$dev \
		  --add-host host.docker.internal:host-gateway \
		  --stop-signal SIGRTMIN+3 \
		  apollo/agent:dev; \
	done

# Stop agent containers started by start-device-agents.
stop-device-agents:
	for dev in $(AGENT_NAMES); do docker rm -f $$dev 2>/dev/null || true; done

# Remove local build artifacts
clean:
	rm -rf bin

# Clean everything: stop agents, delete kind cluster, remove binaries and images
clean-all:
	@echo "Cleaning everything..."
	$(MAKE) stop-device-agents
	kind delete cluster --name $(KIND_CLUSTER) || true
	rm -rf bin
	docker rmi apollo/agent:dev apollo/gateway:dev apollo/controller:dev apollo-deviceprocess-controller:dev 2>/dev/null || true
	@echo "Cleanup complete"

# Ensure a buildx builder is available and selected.
ensure-buildx:
ifeq ($(BUILDX_AVAILABLE),1)
	@if ! docker buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1; then \
		echo "Creating buildx builder '$(BUILDX_BUILDER)'"; \
		docker buildx create --name $(BUILDX_BUILDER) --use >/dev/null; \
	else \
		docker buildx use $(BUILDX_BUILDER) >/dev/null; \
	fi
	@docker buildx inspect $(BUILDX_BUILDER) --bootstrap >/dev/null
else
	@echo "docker buildx not found; install Docker Buildx plugin or upgrade Docker Desktop." && exit 1
endif
