


# Quick start (with real OCI artifact demo)

This walkthrough builds a single-layer payload tar, pushes it to a local/plain-HTTP registry, references it by **digest**, and runs the agents against that artifact.
`make demo-build` now also builds/pushes the payload to `localhost:5000/log-forwarder:demo` and writes the digest to `/tmp/log-forwarder.digest`. `make install-crs` uses that digest and rewrites the registry host to `host.docker.internal` so agents can pull.


```
make clean-all
make demo-up
make install-crs  
make start-device-agents

watch make monitor
```


## Detailed Steps
### 0) Clean workspace build artifacts
- **What:** reset local build outputs to avoid stale binaries or manifests.
- **Expectation:** repo has no leftover bin/ artifacts; `make` rebuilds fresh.
- **Why:** prevents “it worked yesterday” drift by using current code and CRDs.
```
make clean || true
rm -rf bin/
```

### 1) Create or reset kind cluster
- **What:** recreate the local kind cluster used for the demo.
- **Expectation:** a fresh kind cluster named apollo-dev is running.
- **Why:** ensures a clean control-plane target for the controller and gateway.
```
export KIND_CLUSTER=apollo-dev
kind delete cluster --name $KIND_CLUSTER || true
make stop-device-agents
kind create cluster --name $KIND_CLUSTER --image kindest/node:v1.29.4
kubectl cluster-info --context kind-$KIND_CLUSTER
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
The controller targets NetworkSwitch; create one per containerized agent you plan to run. The CRD is already installed by step 3.
- **What:** apply the demo NetworkSwitches (device-01, device-02) with the labels the demo deployment expects. Add more devices by editing `examples/networkswitches-demo.yaml` if you scale higher.
- **Expectation:** resources exist with site=sfo, role=tor labels.
- **Why:** DeviceProcessDeployment selector matches these devices; controller will fan out one DeviceProcess per device.
```
kubectl apply -f examples/networkswitches-demo.yaml
```

### 7) Prepare demo DeviceProcessDeployment
- **What:** apply the demo DeviceProcessDeployment in [examples/deviceprocessdeployment-demo.yaml](examples/deviceprocessdeployment-demo.yaml) targeting all devices with site=sfo, role=tor. The template uses the `systemd` backend (supported by the container systemd path and WSL).
- **Expectation:** controller creates one DeviceProcess per device (e.g., firmware-upgrade-device-01, firmware-upgrade-device-02).
- **Why:** shows DaemonSet-like fanout and central desired state.
```
kubectl apply -f examples/deviceprocessdeployment-demo.yaml
sleep 5   # allow controller to fan out
kubectl get deviceprocess
```

This step only proves desired-state plumbing; the container fields are not executed yet. Execution begins once agents poll in step 9 (after port-forwarding).


### 8) Make gateway reachable to local agents (port-forward)
Use two terminals so the blocking port-forward does not hide the next commands. Bind to all interfaces so containers/WSL can hit the forward. **Use one port everywhere; default to 18080.**
- **What:** expose the in-cluster gateway service to the host for local containerized or WSL agents.
- **Expectation:** Terminal A runs the forward; Terminal B sets APOLLO_GATEWAY_URL for later steps.
- **Why:** agents run outside the cluster and need a stable URL to the gateway.

```
FORWARD_PORT=18080
kubectl -n default port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 ${FORWARD_PORT}:8080
```


### 9) Run systemd-capable agents in containers (WSL-friendly)
Run one container per device with systemd PID1. Use the provided make target to start them with the Docker bridge gateway IP (works on WSL when host.docker.internal does not resolve).
- **What:** start two sample agents (device-01, device-02); scale by editing `AGENT_NAMES` in the Makefile or overriding on the command line.
- **Expectation:** each container hostname/device name matches a NetworkSwitch from step 6 and becomes Ready.
- **Why:** demonstrates multi-device fanout without extra VMs.


Start containers (uses Docker bridge IP automatically):
```
make start-device-agents      # uses AGENT_NAMES=device-01 device-02 and AGENT_GATEWAY=http://<docker-bridge-ip>:18080
```

Stop them when done:
```
make stop-device-agents
```

Check inside one container if needed:
```
docker exec -it device-01 bash -lc "systemctl status apollo-agent --no-pager"
```


### 10) Verify status and events
- **What:** check that DeviceProcess objects reflect agent connection, observed spec, and now the runtime info (pid/startTime) coming from the systemd backend.
- **Expectation:** conditions show AgentConnected=True, SpecObserved=True, ProcessStarted=True; observedSpecHash is set; `status.pid` and `status.startTime` are populated when systemd is available; events exist.
- **Why:** proves the gateway writes status/events back based on agent reports and the systemd runtime data.

We installed two networkswitches (device-01, device-02).
We installed a deviceprocessdeployment firmware-upgrade that targets those switches, so the controller should have created matching deviceprocesses (firmware-upgrade-device-01, firmware-upgrade-device-02).

We then started the agents inside containers with systemd PID1. They talk to the gateway we port-forwarded in step 8. They poll the gateway, get their desired state, and report status back.

The deviceprocess objects should now show that the agents are connected and have observed the spec. They should also have events logged.

```
kubectl get deviceprocessdeployment
kubectl get deviceprocess
kubectl get networkswitch
kubectl get pods

# check pid/startTime fields on one of the deviceprocesses
kubectl get deviceprocess firmware-upgrade-device-01 -o jsonpath='{.status.pid}{"\\n"}'
kubectl get deviceprocess firmware-upgrade-device-01 -o jsonpath='{.status.startTime}{"\\n"}'

```


# Optional: run the agent directly on another systemd host

- **Why:** quick path if you already have a systemd-capable host/VM outside WSL.
- **Prep:** build the agent binary (`make demo-build`) and ensure the port-forward from step 8 is running.

```
# run the agent locally as root so it can manage systemd; reuse the forwarded gateway port
sudo env APOLLO_DEVICE_NAME=device-01 APOLLO_GATEWAY_URL=${APOLLO_GATEWAY_URL:-http://localhost:18080} \
  ./bin/apollo-deviceprocess-agent

# confirm runtime data flows back into status
kubectl get deviceprocess firmware-upgrade-device-01 -o jsonpath='{.status.pid}{"\\n"}'
kubectl get deviceprocess firmware-upgrade-device-01 -o jsonpath='{.status.startTime}{"\\n"}'
```


-----
# Extra Features

### 11) ETag + stale detection demo
Use a new terminal from this step onward; the log follow blocks.
- **What:** prove ETag 200→304 with `curl`, then stop one agent to trigger stale detection.
- **Expectation:** first desired GET is 200 with an `ETag`, second GET with `If-None-Match` returns 304; after ~45s stopped, AgentConnected=False and a warning event appears.
- **Why:** shows efficiency and clean disconnect signaling without event spam.

```
# Show ETag behavior (non-blocking)
curl -i http://localhost:${FORWARD_PORT:-18080}/v1/devices/device-01/desired | tee /tmp/desired.txt
etag=$(grep -i ETag /tmp/desired.txt | awk '{print $2}')
curl -i -H "If-None-Match: ${etag}" http://localhost:${FORWARD_PORT:-18080}/v1/devices/device-01/desired

# Follow gateway events (blocking; do in this terminal)
kubectl -n default logs -f deploy/apollo-deviceprocess-gateway
```

In another terminal, stop one container agent to simulate staleness:
```
docker stop device-02
sleep 45   # wait past stale timeout (default ~30s) so AgentConnected flips False
kubectl get deviceprocess firmware-upgrade-device-02 -o jsonpath='{.status.conditions}'
# (optional) kubectl describe deviceprocess firmware-upgrade-device-02 | sed -n '/Events/,$p'
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
# optional: watch startTime change after restart (systemd backend)
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.startTime}{"\\n"}'
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
