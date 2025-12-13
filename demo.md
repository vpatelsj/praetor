Here’s a **clean, end-to-end demo prompt** you can copy/paste into ChatGPT (or into your own demo runbook doc) that walks through everything you’ve built so far in a way that will look credible to leadership: **Kubernetes-native desired state → gateway fanout → agent observe/heartbeat → gateway writes status/events → stale detection → ETag efficiency**.

---

## End-to-End MVP Demo Prompt (Step 1–3)

You are helping me run a polished MVP demo for our **Apollo DeviceProcess** system (Step 1–3). The goal is to demonstrate that we have a Kubernetes-native control plane for device workloads **without agents talking to the Kubernetes API server**.

### Key Points We Must Demonstrate

1. **CRDs exist** and have good kubectl UX (`DeviceProcess`, `DeviceProcessDeployment`).
2. **Controller** reconciles `DeviceProcessDeployment` → creates per-device `DeviceProcess` objects (DaemonSet semantics).
3. **Device Gateway** is the only component that talks to Kubernetes:

   * reads desired `DeviceProcess` state from cached informers + indexed lookup
   * writes `DeviceProcess.status` and emits Kubernetes Events
4. **Agents** run as docker-compose containers and talk **only** to Device Gateway over HTTP:

   * fetch desired state (`GET /desired`) with **ETag/304**
   * report heartbeat + spec observed (`POST /report`)
5. **Status updates appear within seconds** after a DeviceProcess is applied:

   * `phase=Pending`
   * `AgentConnected=True`
   * `SpecObserved=True`
   * `observedSpecHash` set
6. **Stale detection works**:

   * stop an agent → gateway marks `AgentConnected=False` after stale window
   * emits exactly one warning event (no spam)
7. **Efficiency / scale story**:

   * verify gateway returns mostly **304 Not Modified**
   * verify gateway is **not listing the world** (it uses field index on `spec.deviceRef.name`)
   * verify gateway does not patch status every heartbeat (coalescing/transition-based writes)

### Environment Assumptions

* I have a Kubernetes cluster available and kubeconfig is set.
* The CRDs and controller are already installed (or include commands to install them).
* Device Gateway is running (in-cluster or locally with kubeconfig), and exposes HTTP endpoints:

  * `GET /v1/devices/{deviceName}/desired`
  * `POST /v1/devices/{deviceName}/report`
* Agents run locally via docker compose and connect to gateway using:

  * `APOLLO_DEVICE_NAME`
  * `APOLLO_GATEWAY_URL`
  * `APOLLO_DEVICE_TOKEN` or per-device HMAC auth (whichever is configured)

### Deliverable Output Format

Give me:

1. A **demo script** with headings and exact commands to run (kubectl, docker compose, curl if useful).
2. The **expected output snippets** I should point to for each step (kubectl get/describe, events, gateway logs).
3. A short list of **talk track bullets** for leadership (2–3 lines per step).
4. A “**what can go wrong**” checklist + quick mitigations.

### Demo Flow Requirements (must include)

* Create a fake device inventory object (or assume one exists) for at least 2 devices (e.g., `tor1-01`, `tor1-02`) with labels matching a selector.
* Apply a `DeviceProcessDeployment` matching those devices.
* Show that controller creates 2 `DeviceProcess` objects with stable naming and labels.
* Start 2 local agents (docker compose) and show status updates within seconds.
* Show the gateway returning **304** responses after the first poll (ETag working).
* Update the deployment template (change an arg/env) and show new `observedSpecHash` is reported + updated.
* Stop one agent and show `AgentConnected=False` after stale timeout + warning event.
* Restart the agent and show `AgentConnected=True` flips back even if desired hasn’t changed (heartbeat-only reconnect).

### Constraints

* Don’t mention future steps like artifact download or systemd execution except as “next”.
* Keep the demo focused on the “control plane + status loop” working end-to-end.
* Be explicit with timing expectations (“within 5 seconds”, “after 3× heartbeat interval”, etc.)
* Include a section to show that agents do **not** have Kubernetes credentials and do not talk to the apiserver.

Now produce the complete demo script.

---

If you want, I can also tailor the script to **your exact run mode**:

* gateway **in cluster** (Service + port-forward), or
## End-to-end rebuild + demo (from scratch)

This runbook rebuilds everything: regenerate CRDs, rebuild binaries, rebuild images, load into kind, redeploy controller/gateway, and run agents.

### 0) Clean workspace build artifacts
```
make clean || true
rm -rf bin/
```

### 1) Recreate kind cluster
```
export KIND_CLUSTER=apollo-dev
kind delete cluster --name $KIND_CLUSTER || true
kind create cluster --name $KIND_CLUSTER --image kindest/node:v1.29.4
kubectl cluster-info --context kind-$KIND_CLUSTER
```

### 2) Regenerate code + CRDs + binaries
```
make tools            # install controller-gen, etc.
make generate         # deepcopy/clientsets
make manifests        # refresh config/crd/bases
make build            # rebuild bin/* (controller, gateway, agent)
```

### 3) Build container images
Dockerfiles are expected at repo root. Tag controller with both names to avoid pull mismatch.
```
docker build -t apollo/controller:dev -t apollo-deviceprocess-controller:dev -f Dockerfile.controller .
docker build -t apollo/gateway:dev    -f Dockerfile.gateway .
docker build -t apollo/agent:dev      -f Dockerfile.agent .
```

### 4) Load images into kind
```
kind load docker-image apollo/controller:dev --name $KIND_CLUSTER
kind load docker-image apollo-deviceprocess-controller:dev --name $KIND_CLUSTER
kind load docker-image apollo/gateway:dev --name $KIND_CLUSTER
# agent image not needed in kind if you run agents via local docker compose
```

### 5) Install CRDs and controller in the fresh cluster (namespace: default)
```
make install                               # applies CRDs from config/crd/bases
make deploy IMAGE=apollo/controller:dev NAMESPACE=default
kubectl rollout status deploy/apollo-deviceprocess-controller -n default
```
Note: the default kustomize overlay excludes samples; you will apply the demo CRs manually in later steps.

### 6) Deploy or run gateway (only component talking to API)

**Option A: in-cluster (namespace: default)**
```
kind load docker-image apollo/gateway:dev --name $KIND_CLUSTER

# RBAC for gateway (already in repo)
kubectl apply -f config/rbac/gateway_service_account.yaml \
   -f config/rbac/gateway_role.yaml \
   -f config/rbac/gateway_role_binding.yaml

# Minimal Deployment + Service
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
        - --kubeconfig=
        ports:
        - name: http
          containerPort: 8080
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
```


### 7) Make gateway reachable to local agents (port-forward)
Run this in its own terminal and keep it running. Bind to all interfaces so docker bridge containers can hit the forward. **Use one port everywhere; default to 18080 to avoid conflicts.**
```
FORWARD_PORT=18080
kubectl -n default port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 ${FORWARD_PORT}:8080
export APOLLO_GATEWAY_URL=http://host.docker.internal:${FORWARD_PORT}

# quick sanity check from host (should return 200 or 304 once DeviceProcess exists)
curl -i http://localhost:${FORWARD_PORT}/v1/devices/tor1-01/desired || true
```

### 8) Create device inventory objects (NetworkSwitches)
The controller only targets NetworkSwitch; create the CRD, wait for it to register, then create two devices that match the selector used below.
```
# Install the placeholder CRD (no sample objects) and wait until it is established
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
    shortNames:
    - nsw
  versions:
  - name: v1alpha1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              dummy:
                type: string
    additionalPrinterColumns:
    - name: Role
      type: string
      jsonPath: .metadata.labels.role
EOF

kubectl wait --for=condition=Established crd/networkswitches.azure.com --timeout=60s

# Create the two demo devices with labels site=sfo, role=tor
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
spec:
  dummy: ok
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
spec:
  dummy: ok
EOF
```

### 9) Prepare demo DeviceProcessDeployment
```
cat > /tmp/dpd.yaml <<'EOF'
apiVersion: azure.com/v1alpha1
kind: DeviceProcessDeployment
metadata:
  name: demo-logger
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
sleep 5   # allow controller to fan out
kubectl get deviceprocess
```

### 10) Run local agents via docker compose (no Kubernetes access)
In the same shell where `APOLLO_GATEWAY_URL` is exported from step 7, run compose so it picks up the correct port.
```
cat > docker-compose.yaml <<'EOF'
services:
  agent-tor1-01:
    image: apollo/agent:dev
    environment:
      APOLLO_DEVICE_NAME: tor1-01
      APOLLO_GATEWAY_URL: ${APOLLO_GATEWAY_URL:-http://host.docker.internal:18080}  # must match FORWARD_PORT
    network_mode: bridge
    extra_hosts:
      - "host.docker.internal:host-gateway"   # needed on Linux
  agent-tor1-02:
    image: apollo/agent:dev
    environment:
      APOLLO_DEVICE_NAME: tor1-02
      APOLLO_GATEWAY_URL: ${APOLLO_GATEWAY_URL:-http://host.docker.internal:18080}  # must match FORWARD_PORT
    network_mode: bridge
    extra_hosts:
      - "host.docker.internal:host-gateway"   # needed on Linux
EOF

docker compose up
```

### 11) Verify status and events
```
kubectl get deviceprocess
kubectl describe deviceprocess demo-logger-tor1-01 | sed -n '/Events/,$p'
```

### 12) ETag + stale detection demo
```
# Expect first GET 200 then 304 from gateway logs
docker compose stop agent-tor1-02
sleep 45   # wait past stale timeout (default ~30s) so AgentConnected flips False
kubectl get deviceprocess demo-logger-tor1-02 -o jsonpath='{.status.conditions}'
# (optional) kubectl describe deviceprocess demo-logger-tor1-02 | sed -n '/Events/,$p'
```

### 13) Template change and observed hash
```
kubectl patch deviceprocessdeployment demo-logger --type merge -p '{"spec":{"template":{"spec":{"execution":{"args":["echo","updated"]}}}}}'
# give the agent a few seconds to poll/apply, then check the observed hash
kubectl get deviceprocess demo-logger-tor1-01 -o jsonpath='{.status.observedSpecHash}'
sleep 10
kubectl get deviceprocess demo-logger-tor1-01 -o jsonpath='{.status.observedSpecHash}'
```

### 14) Cleanup
```
docker compose down || true
kind delete cluster --name $KIND_CLUSTER
```
