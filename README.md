# Apollo DeviceProcess

Kubernetes-native control plane for device workloads with an HTTP gateway and lightweight agents that never touch the apiserver. Controller fan-outs `DeviceProcessDeployment` into per-device `DeviceProcess` objects; gateway serves desired state with ETags and writes status/events; agents poll/report over HTTP.

## What’s here

- CRDs: [api/azure.com/v1alpha1](api/azure.com/v1alpha1) with generated manifests in [config/crd/bases](config/crd/bases).
- Controller: fans out deployments into per-device `DeviceProcess` objects and keeps desired in sync [controller](controller).
- Gateway: HTTP surface for devices; reads desired via cached informers, returns `ETag`/`304`, records heartbeats, updates status, emits events [gateway](gateway) (entrypoint [cmd/gateway](cmd/gateway)).
- Agent: polls `/desired`, reports observations/heartbeat to `/report`, runs via docker compose or directly [agent](agent).
- Sample manifests: [config/samples](config/samples) for quick smoke tests.

## Dev loop

```bash
make fmt vet test build   # gofmt + vet + unit tests + build binaries into bin/
# When APIs change
make generate             # deepcopy
make manifests            # CRDs into config/crd/bases
```

## Binaries produced

- bin/apollo-deviceprocess-controller
- bin/apollo-deviceprocess-gateway
- bin/apollo-deviceprocess-agent

## Demo

1) Create a fresh kind cluster

```bash
export KIND_CLUSTER=apollo-dev
kind delete cluster --name $KIND_CLUSTER || true
kind create cluster --name $KIND_CLUSTER --image kindest/node:v1.29.4
```

2) Build code, CRDs, and images

```bash
make tools generate manifests build
docker build -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
docker build -t apollo/gateway:dev    -f Dockerfile.gateway .
docker build -t apollo/agent:dev      -f Dockerfile.agent .
```

3) Load images into kind and deploy controller

```bash
kind load docker-image apollo/controller:dev --name $KIND_CLUSTER
kind load docker-image apollo-deviceprocess-controller:dev --name $KIND_CLUSTER
kind load docker-image apollo/gateway:dev --name $KIND_CLUSTER

make crd-install                             # CRDs
make controller-deploy IMAGE=apollo/controller:dev NAMESPACE=default
kubectl -n default rollout status deploy/apollo-deviceprocess-controller
```

4) Deploy gateway (in cluster) and port-forward for agents

```bash
kubectl apply -f config/rbac/gateway_service_account.yaml \
	-f config/rbac/gateway_role.yaml \
	-f config/rbac/gateway_role_binding.yaml

cat <<'EOF' | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
	name: apollo-deviceprocess-gateway
	namespace: default
spec:
	replicas: 1
	selector:
		matchLabels:
			app: apollo-deviceprocess-gateway
	template:
		metadata:
			labels:
				app: apollo-deviceprocess-gateway
		spec:
			serviceAccountName: apollo-deviceprocess-gateway
			containers:
			- name: gateway
				image: apollo/gateway:dev
				imagePullPolicy: IfNotPresent
				args:
				- --addr=:8080
				- --default-heartbeat-seconds=15
				- --stale-multiplier=3
---
apiVersion: v1
kind: Service
metadata:
	name: apollo-deviceprocess-gateway
	namespace: default
spec:
	selector:
		app: apollo-deviceprocess-gateway
	ports:
	- name: http
		port: 8080
		targetPort: 8080
EOF

kubectl -n default rollout status deploy/apollo-deviceprocess-gateway

FORWARD_PORT=18080
kubectl -n default port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 ${FORWARD_PORT}:8080 &
export APOLLO_GATEWAY_URL=http://host.docker.internal:${FORWARD_PORT}
```

5) Create the NetworkSwitch CRD and two demo devices (`tor1-01`, `tor1-02`)

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
	name: networkswitches.azure.com
spec:
	group: azure.com
	scope: Namespaced
	names:
		plural: networkswitches
		singular: networkswitch
		kind: NetworkSwitch
	versions:
	- name: v1alpha1
		served: true
		storage: true
		schema:
			openAPIV3Schema:
				type: object
EOF

kubectl wait --for=condition=Established crd/networkswitches.azure.com --timeout=60s

cat <<'EOF' | kubectl apply -f -
apiVersion: azure.com/v1alpha1
kind: NetworkSwitch
metadata:
	name: tor1-01
	namespace: default
	labels:
		site: sfo
		role: tor
		rack: r1
spec: { }
---
apiVersion: azure.com/v1alpha1
kind: NetworkSwitch
metadata:
	name: tor1-02
	namespace: default
	labels:
		site: sfo
		role: tor
		rack: r1
spec: { }
EOF
```

6) Apply the demo `DeviceProcessDeployment` targeting both switches

```bash
cat > /tmp/dpd.yaml <<'EOF'
apiVersion: azure.com/v1alpha1
kind: DeviceProcessDeployment
metadata:
	name: firmware-upgrade
spec:
	selector:
		matchLabels:
			site: sfo
			role: tor
	template:
		metadata:
			labels:
				app: logger
		spec:
			artifact:
				type: oci
				url: alpine:3.19
			execution:
				backend: container
				command: ["/bin/sh", "-c"]
				args: ["echo hello from ${APOLLO_DEVICE_NAME}; sleep 3600"]
				env:
				- name: APOLLO_DEVICE_NAME
					value: ${APOLLO_DEVICE_NAME}
EOF

kubectl apply -f /tmp/dpd.yaml
sleep 5
kubectl get deviceprocess
```

7) Run two local agents via docker compose (no kubeconfig on devices)

```bash
cat > docker-compose.yaml <<'EOF'
services:
	agent-tor1-01:
		image: apollo/agent:dev
		environment:
			APOLLO_DEVICE_NAME: tor1-01
			APOLLO_GATEWAY_URL: ${APOLLO_GATEWAY_URL:-http://host.docker.internal:18080}
		network_mode: bridge
		extra_hosts:
			- "host.docker.internal:host-gateway"
	agent-tor1-02:
		image: apollo/agent:dev
		environment:
			APOLLO_DEVICE_NAME: tor1-02
			APOLLO_GATEWAY_URL: ${APOLLO_GATEWAY_URL:-http://host.docker.internal:18080}
		network_mode: bridge
		extra_hosts:
			- "host.docker.internal:host-gateway"
EOF

docker compose up
```

8) Verify status and ETag/stale behavior

```bash
kubectl get deviceprocess
kubectl describe deviceprocess firmware-upgrade-tor1-01 | sed -n '/Events/,$p'

# Stop one agent to see stale flip and single warning event
docker compose stop agent-tor1-02
sleep 45
kubectl get deviceprocess firmware-upgrade-tor1-02 -o jsonpath='{.status.conditions}'
```

9) Optional: template change → new observed hash

```bash
kubectl patch deviceprocessdeployment firmware-upgrade --type merge -p '{"spec":{"template":{"spec":{"execution":{"args":["echo","updated"]}}}}}'
sleep 10
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.observedSpecHash}'
```

10) Cleanup

```bash

kind delete cluster --name $KIND_CLUSTER
```
## Agents via docker compose

- Minimal example: [examples/docker-compose.yaml](examples/docker-compose.yaml).
- Required env vars: `APOLLO_DEVICE_NAME`, `APOLLO_GATEWAY_URL`, and either `APOLLO_DEVICE_TOKEN` (shared dev token) or `APOLLO_DEVICE_TOKEN_SECRET` (HMAC per device, recommended).
- Agents keep ETag cache and only refetch full desired when the gateway changes it.

## HTTP surface (for debugging)

- `GET /v1/devices/{device}/desired` – returns desired spec; includes `ETag`; returns `304 Not Modified` when unchanged.
- `POST /v1/devices/{device}/report` – body: `{agentVersion, timestamp, heartbeat, observations[]}`; records heartbeat, sets status conditions, emits events.
- Auth header: `X-Device-Token`. If `--device-token-secret` is set, tokens are HMAC(deviceName) using that secret; otherwise `--device-token` is a shared dev token.

## Notes and troubleshooting

- Field index on `spec.deviceRef.name` prevents whole-cluster list scans.
- Stale detection: `stale-multiplier × heartbeat` (default 3×15s) marks agents disconnected.
- Expect many `304` responses on `/desired`; a stream of `200` means the agent lost its ETag.
- Gateway logs: `kubectl -n default logs deploy/apollo-deviceprocess-gateway -f`
- Controller logs: `kubectl -n default logs deploy/apollo-deviceprocess-controller -f`

## Contributing

- Go 1.22+
- Docker + kubectl + kind for the workflow above
- fmt/vet/test/build before sending changes
