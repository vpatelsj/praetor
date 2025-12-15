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
APOLLO_OCI_PLAIN_HTTP ?= 1
REGISTRY_PORT ?= 5001
APOLLO_OCI_PLAIN_HTTP_HOSTS ?= host.docker.internal:$(REGISTRY_PORT)
PAYLOAD_DIR ?= /tmp/payload
PAYLOAD_TAR ?= /tmp/payload.tar
REGISTRY ?= localhost:$(REGISTRY_PORT)
LOG_FORWARDER_REPO ?= log-forwarder
LOG_FORWARDER_TAG ?= demo
LOG_FORWARDER_REF := $(REGISTRY)/$(LOG_FORWARDER_REPO):$(LOG_FORWARDER_TAG)
LOG_FORWARDER_DIGEST_FILE ?= /tmp/log-forwarder.digest
DEPLOYMENT_FILE ?= examples/deviceprocessdeployment-demo.yaml
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

.PHONY: all fmt vet test generate manifests build tools crd-install controller-deploy kind-image kind-load kind-deploy kind-restart kind-clean clean gateway-deploy demo-build container-build container-demo-build ensure-kind ensure-kind-cluster demo-up install-crs start-device-agents monitor stop-device-agents payload-build-push
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
demo-build: ensure-docker ensure-kind ensure-oras ensure-kind-cluster ensure-buildx tools generate manifests build
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/gateway:dev -f Dockerfile.gateway .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/agent:dev -f Dockerfile.agent .
	kind load docker-image apollo/controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo-deviceprocess-controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo/gateway:dev --name $(KIND_CLUSTER)
	$(MAKE) payload-build-push

# Run generate/manifests/build inside a container (uses Go 1.22) to avoid host Go constraints.
container-build:
	$(DOCKER_GO_RUN) sh -c "apk add --no-cache bash git make && \
	 go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) && \
	 make generate manifests build CONTROLLER_GEN=/go/bin/controller-gen"

# Full demo build using containerized Go for codegen/build, then local docker/kind for images.
container-demo-build: ensure-docker ensure-kind ensure-oras ensure-kind-cluster ensure-buildx container-build
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/gateway:dev -f Dockerfile.gateway .
	$(DOCKER_BUILD_CMD) $(DOCKER_BUILD_OPTS) -t apollo/agent:dev -f Dockerfile.agent .
	kind load docker-image apollo/controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo-deviceprocess-controller:dev --name $(KIND_CLUSTER)
	kind load docker-image apollo/gateway:dev --name $(KIND_CLUSTER)
	$(MAKE) payload-build-push

# Ensure kind is available (default install via Homebrew; override KIND_INSTALL_CMD to customize).
ensure-kind:
	@command -v kind >/dev/null 2>&1 || (echo "kind not found; installing with '$(KIND_INSTALL_CMD)'" && $(KIND_INSTALL_CMD))

# Ensure oras is available (install via Homebrew if missing).
ensure-oras:
	@command -v oras >/dev/null 2>&1 || (echo "oras not found; installing with 'brew install oras'" && brew install oras)

# Ensure docker is available and running.
ensure-docker:
	@command -v docker >/dev/null 2>&1 || (echo "docker not found; please install Docker Desktop" && exit 1)
	@docker info >/dev/null 2>&1 || (echo "docker daemon not running; please start Docker Desktop" && exit 1)

# Build single-layer payload tar, run local registry, push, and record digest.
payload-build-push:
	@echo "Building demo payload tar at $(PAYLOAD_TAR)"
	rm -rf $(PAYLOAD_DIR)
	mkdir -p $(PAYLOAD_DIR)/bin $(PAYLOAD_DIR)/config
	printf '#!/bin/sh\necho hello demo\nsleep 3600\n' > $(PAYLOAD_DIR)/bin/log-forwarder
	chmod +x $(PAYLOAD_DIR)/bin/log-forwarder
	echo 'log: info' > $(PAYLOAD_DIR)/config/log-forwarder.yaml
	tar -C $(dir $(PAYLOAD_DIR)) -cf $(PAYLOAD_TAR) payload
	@echo "Starting local registry on $(REGISTRY_PORT) (apollo-registry)"
	docker rm -f apollo-registry 2>/dev/null || true
	docker run -d --name apollo-registry -p $(REGISTRY_PORT):5000 registry:2
	@echo "Pushing payload to $(LOG_FORWARDER_REF)"
	(cd /tmp && oras push $(LOG_FORWARDER_REF) payload.tar:application/vnd.oci.image.layer.v1.tar)
	@echo "Fetching digest for $(LOG_FORWARDER_REF)"
	D=$$(oras manifest fetch --descriptor $(LOG_FORWARDER_REF) | jq -r '.digest'); \
		echo $$D > $(LOG_FORWARDER_DIGEST_FILE); \
		echo "Pushed digest: $$D (saved to $(LOG_FORWARDER_DIGEST_FILE))";

# Create the kind cluster if it does not exist.
ensure-kind-cluster: ensure-kind
	@if ! kind get clusters | grep -qx $(KIND_CLUSTER); then \
		echo "Creating kind cluster '$(KIND_CLUSTER)' with image $(KIND_IMAGE)"; \
		kind create cluster --name $(KIND_CLUSTER) --image $(KIND_IMAGE); \
	else \
		echo "Using existing kind cluster '$(KIND_CLUSTER)'"; \
	fi

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
	@LOG=/tmp/apollo-gateway-port-forward.log; PIDFILE=/tmp/apollo-gateway-port-forward.pid; \
	  echo "Port-forwarding gateway on 0.0.0.0:$(FORWARD_PORT) -> 8080 (background)"; \
	  if [ -f $$PIDFILE ] && kill -0 "$$(<$$PIDFILE)" 2>/dev/null; then \
	    echo "Stopping existing port-forward pid $$(cat $$PIDFILE)"; \
	    kill "$$(<$$PIDFILE)" || true; \
	    sleep 1; \
	  fi; \
	  nohup kubectl -n $(NAMESPACE) port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 $(FORWARD_PORT):8080 > $$LOG 2>&1 & \
	  echo $$! > $$PIDFILE; \
	  echo "Started port-forward (pid $$!); log: $$LOG; stop with 'kill $$(cat $$PIDFILE)'"; \
	  if [ ! -s $(LOG_FORWARDER_DIGEST_FILE) ]; then \
	    echo "ERROR: digest file $(LOG_FORWARDER_DIGEST_FILE) missing; run 'make demo-build' first" >&2; exit 1; \
	  fi; \
	  DIGEST=$$(cat $(LOG_FORWARDER_DIGEST_FILE)); \
	  echo "Applying DeviceProcessDeployment with digest $$DIGEST"; \
	  sed "s|sha256:REPLACE_ME|$${DIGEST}|" $(DEPLOYMENT_FILE) | sed "s|localhost:$(REGISTRY_PORT)|host.docker.internal:$(REGISTRY_PORT)|" | kubectl apply -f -

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
		sleep 2; \
		docker exec $$dev sh -lc 'set -e; \
		  for i in 1 2 3 4 5; do \
		    systemctl is-system-running --wait --quiet && break || true; \
		    sleep 1; \
		  done; \
		  mkdir -p /etc/systemd/system/apollo-agent.service.d; \
		  printf "[Service]\nEnvironment=APOLLO_OCI_PLAIN_HTTP=$(APOLLO_OCI_PLAIN_HTTP)\nEnvironment=APOLLO_OCI_PLAIN_HTTP_HOSTS=$(APOLLO_OCI_PLAIN_HTTP_HOSTS)\n" > /etc/systemd/system/apollo-agent.service.d/oci.conf; \
		  for i in 1 2 3 4 5; do \
		    if systemctl daemon-reload && systemctl restart apollo-agent.service; then exit 0; fi; \
		    sleep 1; \
		  done; \
		  echo "systemd not ready" >&2; exit 1'; \
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
