## End-to-end rebuild + demo (from scratch)

Executable runbook for the Apollo DeviceProcess MVP demo. The ChatGPT prompt that explains the goals lives in demo-prompt.md.

### 0) Clean workspace build artifacts
- **What:** reset local build outputs to avoid stale binaries or manifests.
- **Expectation:** repo has no leftover bin/ artifacts; `make` rebuilds fresh.
- **Why:** prevents “it worked yesterday” drift by using current code and CRDs.
```
make clean || true
rm -rf bin/
```

### 1) Recreate kind cluster
- **What:** provision a fresh, empty Kubernetes cluster.
- **Expectation:** kind cluster `apollo-dev` is running and kubeconfig points to it.
- **Why:** ensures a predictable control plane with no leftover CRs/controllers.
```
export KIND_CLUSTER=apollo-dev
kind delete cluster --name $KIND_CLUSTER || true
kind create cluster --name $KIND_CLUSTER --image kindest/node:v1.29.4
kubectl cluster-info --context kind-$KIND_CLUSTER
make demo-build && make crd-install && make controller-deploy && make gateway-deploy
```

### 2) Generate, build images, and load into kind (one command)
- **What:** codegen, regenerate CRDs, build binaries, build images, and load controller/gateway images into kind.
- **Expectation:** CRDs refreshed in `config/crd/bases/`, binaries in `bin/`, images in local cache, and controller/gateway images loaded into the kind cluster.
- **Artifacts produced:**
  - `api/**/zz_generated.deepcopy.go` (deepcopy) and client/informer/lister code
  - refreshed CRD YAMLs under `config/crd/bases/`
    - DeviceProcess: `config/crd/bases/azure.com_deviceprocesses.yaml` (Kind `DeviceProcess`, group `azure.com`, v1alpha1)
    - DeviceProcessDeployment: `config/crd/bases/azure.com_deviceprocessdeployments.yaml` (Kind `DeviceProcessDeployment`, group `azure.com`, v1alpha1)
    - NetworkSwitch: `config/crd/bases/azure.com_networkswitches.yaml` (Kind `NetworkSwitch`, group `azure.com`, v1alpha1)
  - updated kustomize overlays under `config/`
  - rebuilt binaries in `bin/`
    - `bin/apollo-deviceprocess-controller`
    - `bin/apollo-deviceprocess-agent`
    - `bin/apollo-deviceprocess-gateway`
  - built images: `apollo/controller:dev`, `apollo-deviceprocess-controller:dev`, `apollo/gateway:dev`, `apollo/agent:dev`
  - images loaded into kind: controller (both tags) and gateway
- **Why:** single command to refresh code, manifests, binaries, images, and preload kind to avoid pull errors.
```
make demo-build
```

### 3) Install CRDs (namespace: default)
- **What:** register DeviceProcess CRDs.
- **Expectation:** CRDs are Established; no controller deployed yet.
- **What `make crd-install` does to kind:** applies the three CRDs from `config/crd/bases/` (DeviceProcess, DeviceProcessDeployment, NetworkSwitch); no RBAC or deployments yet.
- **Why:** installs the API types so later steps (controller/gateway) can create/read DeviceProcess resources and the demo inventory type.
```
make crd-install                           # applies CRDs from config/crd/bases
```

### 4) Deploy controller (namespace: default)
- **What:** deploy the DeviceProcess controller using the default kustomize overlay.
- **Expectation:** controller Deployment becomes Ready.
- **What `make controller-deploy` does:** applies `config/default` overlay (RBAC, manager Deployment, service account) and sets the controller image; rollout checked below.
- **Why:** control-plane piece that fans out per-device DeviceProcess objects.
```
make controller-deploy
```
Note: the default kustomize overlay excludes samples; you will apply the demo CRs manually in later steps.

### 5) Deploy Device Gateway (namespace: default)
- **What:** deploy the HTTP gateway that agents call; it reads desired state and writes status via the API.
- **Expectation:** single-replica Deployment + Service on port 8080 in default.
- **Why:** enforces the “agents do not talk to apiserver” design; gateway is the only API client.


```
make gateway-deploy
```


### 6) Create device inventory objects (NetworkSwitches)
The controller targets NetworkSwitch; create the two devices that match the selector used later. The CRD is already installed by step 3.
- **What:** apply the demo inventory objects in [examples/networkswitches-demo.yaml](examples/networkswitches-demo.yaml) (tor1-01, tor1-02) with matching labels.
- **Expectation:** two NetworkSwitch resources exist with site=sfo, role=tor labels.
- **Why:** DeviceProcessDeployment selector matches these; controller will fan out per device once deployed.
```
kubectl apply -f examples/networkswitches-demo.yaml
```

### 7) Prepare demo DeviceProcessDeployment
- **What:** apply the demo DeviceProcessDeployment in [examples/deviceprocessdeployment-demo.yaml](examples/deviceprocessdeployment-demo.yaml) targeting the two demo devices.
- **Expectation:** controller creates two DeviceProcess objects (one per device) after apply.
- **Why:** shows DaemonSet-like fanout and central desired state.
```
kubectl apply -f examples/deviceprocessdeployment-demo.yaml
sleep 5   # allow controller to fan out
kubectl get deviceprocess
```

This step only proves desired-state plumbing; the container fields are not executed yet. Execution begins once agents poll in step 9 (after port-forwarding).


### 8) Make gateway reachable to local agents (port-forward)
Use two terminals so the blocking port-forward does not hide the next commands. Bind to all interfaces so docker bridge containers can hit the forward. **Use one port everywhere; default to 18080.**
- **What:** expose the in-cluster gateway service to the host for local docker-compose agents.
- **Expectation:** Terminal A runs the forward; Terminal B sets APOLLO_GATEWAY_URL for later steps.
- **Why:** agents run outside the cluster and need a stable URL to the gateway.

Terminal A (blocking):
```
FORWARD_PORT=18080
kubectl -n default port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 ${FORWARD_PORT}:8080
```

Terminal B (reuse for docker compose later):
```
export FORWARD_PORT=18080
export APOLLO_GATEWAY_URL=http://host.docker.internal:${FORWARD_PORT}

# quick sanity check from host (should return 200 or 304 once DeviceProcess exists)
curl -i http://localhost:${FORWARD_PORT}/v1/devices/tor1-01/desired || true
```


### 9) Run local agents via docker compose (no Kubernetes access)
In the same shell where `APOLLO_GATEWAY_URL` is exported from step 8, use the repo compose file so it picks up the correct port. If your gateway expects auth, add `APOLLO_DEVICE_TOKEN` (or remove it if running in insecure mode).
- **What:** start two agent containers that poll the gateway and report status; no kube creds.
- **Expectation:** agents connect over HTTP to the forwarded gateway URL and fetch desired state.
- **Why:** demonstrates edge connectivity and that agents never contact the apiserver directly.
```
docker compose up         # uses ./docker-compose.yaml with agent-tor1-01 and agent-tor1-02
```

### 10) Verify status and events
- **What:** check that DeviceProcess objects reflect agent connection and observed spec.
- **Expectation:** conditions show AgentConnected=True, SpecObserved=True, observedSpecHash set; events exist.
- **Why:** proves the gateway writes status/events back based on agent reports.

we installed two networkswitches (tor1-01, tor1-02)  
We installed a deviceprocessdeployment firmware-upgrade that targets those switches, so the controller should have created two deviceprocesses (firmware-upgrade-tor1-01, firmware-upgrade-tor1-02).

We then started two agents via docker compose, one for each device.

These agents talk to the gateway we port-forwarded in step 8. They poll the gateway, get their desired state, and report status back.

The deviceprocess objects should now show that the agents are connected and have observed the spec. they should also have events logged.

```
kubectl get deviceprocessdeployment
kubectl get deviceprocess
kubectl get networkswitch
kubectl get pods

```

# Conclusion





-----
# Extra Features

### 11) ETag + stale detection demo
Use a new terminal from this step onward; the log follow blocks.
- **What:** prove ETag 200→304 with `curl`, then stop one agent to trigger stale detection.
- **Expectation:** first desired GET is 200 with an `ETag`, second GET with `If-None-Match` returns 304; after ~45s stopped, AgentConnected=False and a warning event appears.
- **Why:** shows efficiency and clean disconnect signaling without event spam.

```
# Show ETag behavior (non-blocking)
curl -i http://localhost:${FORWARD_PORT:-18080}/v1/devices/tor1-01/desired | tee /tmp/desired.txt
etag=$(grep -i ETag /tmp/desired.txt | awk '{print $2}')
curl -i -H "If-None-Match: ${etag}" http://localhost:${FORWARD_PORT:-18080}/v1/devices/tor1-01/desired

# Follow gateway events (blocking; do in this terminal)
kubectl -n default logs -f deploy/apollo-deviceprocess-gateway
```

In another terminal:
```
docker compose stop agent-tor1-02
sleep 45   # wait past stale timeout (default ~30s) so AgentConnected flips False
kubectl get deviceprocess firmware-upgrade-tor1-02 -o jsonpath='{.status.conditions}'
# (optional) kubectl describe deviceprocess firmware-upgrade-tor1-02 | sed -n '/Events/,$p'
```

### 12) Template change and observed hash
- **What:** patch the deployment template to simulate a desired-state change and watch observed hash update.
- **Expectation:** observedSpecHash on DeviceProcess changes after the agent polls the new spec.
- **Why:** demonstrates config rollout signaling from control plane to devices.
```
kubectl patch deviceprocessdeployment firmware-upgrade --type merge \
  -p '{"spec":{"template":{"spec":{"execution":{"args":["echo updated; sleep 3600"]}}}}}'
# give the agent a few seconds to poll/apply, then check the observed hash
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.observedSpecHash}'
sleep 10
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.observedSpecHash}'
```

### 13) Cleanup
- **What:** shut down agents and delete the kind cluster to leave the environment clean.
- **Expectation:** docker compose exits and the kind cluster is removed.
- **Why:** avoids leftover resources and frees ports/VM resources for the next run.
```
export KIND_CLUSTER=apollo-dev
docker compose down || true
kind delete cluster --name $KIND_CLUSTER
```
