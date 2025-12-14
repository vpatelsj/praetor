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

### 1) Create or reset kind cluster
- **What:** recreate the local kind cluster used for the demo.
- **Expectation:** a fresh kind cluster named apollo-dev is running.
- **Why:** ensures a clean control-plane target for the controller and gateway.
```
export KIND_CLUSTER=apollo-dev
kind delete cluster --name $KIND_CLUSTER || true
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
The controller targets NetworkSwitch; create a single device to match our one WSL-based agent. The CRD is already installed by step 3.
- **What:** apply a single NetworkSwitch named tor1-01 with the labels the demo deployment expects. (We no longer create tor1-02 because we only run one agent.)
- **Expectation:** one NetworkSwitch resource exists with site=sfo, role=tor labels.
- **Why:** DeviceProcessDeployment selector matches this device; controller will fan out one DeviceProcess.
```
kubectl apply -f - <<'EOF'
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
EOF
```

### 7) Prepare demo DeviceProcessDeployment
- **What:** apply the demo DeviceProcessDeployment in [examples/deviceprocessdeployment-demo.yaml](examples/deviceprocessdeployment-demo.yaml) targeting the single tor1-01 device (selector still matches site=sfo, role=tor, so only tor1-01 exists). The template uses the `systemd` backend (supported by the WSL agent path).
- **Expectation:** controller creates one DeviceProcess object (firmware-upgrade-tor1-01) after apply.
- **Why:** shows DaemonSet-like fanout and central desired state, reduced to one device for the WSL agent path.
```
kubectl apply -f examples/deviceprocessdeployment-demo.yaml
sleep 5   # allow controller to fan out
kubectl get deviceprocess
```

This step only proves desired-state plumbing; the container fields are not executed yet. Execution begins once agents poll in step 9 (after port-forwarding).


### 8) Make gateway reachable to local agents (port-forward)
Use two terminals so the blocking port-forward does not hide the next commands. Bind to all interfaces so the WSL host (and anything else) can hit the forward. **Use one port everywhere; default to 18080.**
- **What:** expose the in-cluster gateway service to the host for local WSL agents.
- **Expectation:** Terminal A runs the forward; Terminal B sets APOLLO_GATEWAY_URL for later steps.
- **Why:** agents run outside the cluster and need a stable URL to the gateway.

Terminal A (blocking):
```
FORWARD_PORT=18080
kubectl -n default port-forward deploy/apollo-deviceprocess-gateway --address 0.0.0.0 ${FORWARD_PORT}:8080
```

Terminal B (reuse for the WSL agent later):
```
export FORWARD_PORT=18080
export APOLLO_GATEWAY_URL=http://localhost:${FORWARD_PORT}   # WSL agent will call back to this

# quick sanity check from host (should return 200 or 304 once DeviceProcess exists)
curl -i http://localhost:${FORWARD_PORT}/v1/devices/tor1-01/desired || true
```

### Optional: run containerized agents via docker compose (no Kubernetes access)
Skip this if you are following the WSL systemd path. In the same shell where `APOLLO_GATEWAY_URL` is exported from step 8, use the repo compose file so it picks up the correct port. If your gateway expects auth, add `APOLLO_DEVICE_TOKEN` (or remove it if running in insecure mode).
- **What:** start agent containers that poll the gateway and report status; no kube creds.
- **Expectation:** agents connect over HTTP to the forwarded gateway URL and fetch desired state. For the single-agent path, you can comment out the second container in the compose file.
- **Why:** demonstrates edge connectivity and that agents never contact the apiserver directly.
```
docker compose up         # optional fallback; WSL path runs the binary directly
```


### 9) Run the agent locally on WSL (systemd-enabled, single device)
WSL now ships with systemd, so we can run the agent directly without QEMU. We only run one agent (tor1-01) to match the single NetworkSwitch created in step 6.
- **What:** run the Linux binary on your WSL distro as root so it can manage systemd units.
- **Expectation:** the agent connects to `APOLLO_GATEWAY_URL` (localhost:18080 from step 8), fetches desired state for tor1-01, and manages the systemd unit.
- **Why:** simpler than QEMU and avoids duplicate agents.

Prep (still in WSL; keep the port-forward from step 8 running):
```
make demo-build   # ensures ./bin/apollo-deviceprocess-agent exists
```

Run the agent (WSL shell):
```
sudo env \
  APOLLO_DEVICE_NAME=tor1-01 \
  APOLLO_GATEWAY_URL=${APOLLO_GATEWAY_URL:-http://localhost:18080} \
  ./bin/apollo-deviceprocess-agent
```

If you want it as a service, create a simple unit:
```
cat | sudo tee /etc/systemd/system/apollo-agent.service >/dev/null <<'EOF'
[Unit]
Description=Apollo DeviceProcess Agent (WSL)
After=network-online.target

[Service]
ExecStart=/home/$USER/dev/praetor/bin/apollo-deviceprocess-agent
Restart=always
Environment=APOLLO_GATEWAY_URL=${APOLLO_GATEWAY_URL:-http://localhost:18080}
Environment=APOLLO_DEVICE_NAME=tor1-01
#Environment=APOLLO_DEVICE_TOKEN=...

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now apollo-agent
sudo systemctl status apollo-agent --no-pager
```

### 10) Verify status and events
- **What:** check that DeviceProcess objects reflect agent connection, observed spec, and now the runtime info (pid/startTime) coming from the systemd backend.
- **Expectation:** conditions show AgentConnected=True, SpecObserved=True, ProcessStarted=True; observedSpecHash is set; `status.pid` and `status.startTime` are populated when systemd is available; events exist.
- **Why:** proves the gateway writes status/events back based on agent reports and the systemd runtime data.

We installed one networkswitch (tor1-01).  
We installed a deviceprocessdeployment firmware-upgrade that targets that switch, so the controller should have created one deviceprocess (firmware-upgrade-tor1-01).

We then started the agent inside WSL with systemd enabled. It talks to the gateway we port-forwarded in step 8. It polls the gateway, gets its desired state, and reports status back.

The deviceprocess object should now show that the agent is connected and has observed the spec. It should also have events logged.

```
kubectl get deviceprocessdeployment
kubectl get deviceprocess
kubectl get networkswitch
kubectl get pods

# check pid/startTime fields on one of the deviceprocesses
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.pid}{"\\n"}'
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.startTime}{"\\n"}'

```

# Conclusion

# Optional: run the agent directly on another systemd host

- **Why:** quick path if you already have a systemd-capable host/VM outside WSL.
- **Prep:** build the agent binary (`make demo-build`) and ensure the port-forward from step 8 is running.

```
# run the agent locally as root so it can manage systemd; reuse the forwarded gateway port
sudo env APOLLO_DEVICE_NAME=tor1-01 APOLLO_GATEWAY_URL=${APOLLO_GATEWAY_URL:-http://localhost:18080} \
  ./bin/apollo-deviceprocess-agent

# in another terminal, inspect the systemd artifacts written by the agent
sudo systemctl status "apollo-default-firmware-upgrade-tor1-01.service"
sudo systemctl cat "apollo-default-firmware-upgrade-tor1-01.service"
sudo cat /etc/apollo/env/apollo-default-firmware-upgrade-tor1-01.env

# confirm runtime data flows back into status
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.pid}{"\\n"}'
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.startTime}{"\\n"}'

# optional cleanup for this device
sudo systemctl stop "apollo-default-firmware-upgrade-tor1-01.service" || true
sudo systemctl disable "apollo-default-firmware-upgrade-tor1-01.service" || true
sudo rm -f /etc/systemd/system/apollo-default-firmware-upgrade-tor1-01.service /etc/apollo/env/apollo-default-firmware-upgrade-tor1-01.env
sudo systemctl daemon-reload
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
curl -i http://localhost:${FORWARD_PORT:-18080}/v1/devices/tor1-01/desired | tee /tmp/desired.txt
etag=$(grep -i ETag /tmp/desired.txt | awk '{print $2}')
curl -i -H "If-None-Match: ${etag}" http://localhost:${FORWARD_PORT:-18080}/v1/devices/tor1-01/desired

# Follow gateway events (blocking; do in this terminal)
kubectl -n default logs -f deploy/apollo-deviceprocess-gateway
```

In another terminal (WSL), stop the agent service to simulate staleness:
```
sudo systemctl stop apollo-agent
sleep 45   # wait past stale timeout (default ~30s) so AgentConnected flips False
kubectl get deviceprocess firmware-upgrade-tor1-01 -o jsonpath='{.status.conditions}'
# (optional) kubectl describe deviceprocess firmware-upgrade-tor1-01 | sed -n '/Events/,$p'
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
