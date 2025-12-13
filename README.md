Praetor
====================

Control plane for managing processes on devices (network switches, BMCs, DPUs) without kubelets on the devices. A controller fans out `DeviceProcessDeployment` into per-device `DeviceProcess` objects; a gateway mediates all device traffic, and lightweight agents on the devices fetch desired state and report status over HTTP. The gateway can run in- or out-of-cluster, aggregates device status, and shields the apiserver from high fan-out.

Components
----------
- **CRDs** (azure.com/v1alpha1): `NetworkSwitch` (inventory), `DeviceProcess`, `DeviceProcessDeployment` (deployment-like for hardware targets).
- **Controller**: matches devices to deployments, creates one DeviceProcess per eligible device, keeps desired in sync, aggregates rollout status/conditions.
- **Gateway**: intermediary between devices and control plane; serves desired with ETag/304, ingests heartbeats/observations, emits events; deployable inside or outside the cluster.
- **Agent**: tiny binary on the device (or simulator) that executes the commanded process (container/systemd/init) and reports started/healthy state; never talks to the apiserver directly.

Data flow (happy path)
----------------------
1. Controller matches devices to a `DeviceProcessDeployment` (e.g., firmware upgrade) and creates one `DeviceProcess` per device.
2. Agent on each device calls `GET /v1/devices/{device}/desired` on the gateway, receiving desired spec + ETag.
3. Agent executes the commanded process and reports via `POST /v1/devices/{device}/report` with observations (spec hash, processStarted, healthy) and heartbeat.
4. Gateway updates DeviceProcess status/conditions and emits events; controller aggregates to deployment status (desired/current/updated/ready/available).

Conditions and readiness
------------------------
- DeviceProcess conditions: `AgentConnected`, `SpecObserved`, `ProcessStarted`, `Healthy` (plus phase Pending/Running/...)
- Deployment conditions: `Available`, `Progressing`; rollout counts include desired/current/updated/ready/available.

HTTP surface
------------
- `GET /v1/devices/{device}/desired` – desired state, supports `ETag`/`304 Not Modified`.
- `POST /v1/devices/{device}/report` – `{agentVersion, timestamp, heartbeat, observations[]}`; drives status/conditions/events.
- Auth: `X-Device-Token`; supports shared token or HMAC(deviceName) when `--device-token-secret` is set on the gateway.

Operational notes
-----------------
- Gateway decouples device chatter from the apiserver; scale gateway replicas independently for large agent counts.
- Stale detection: `stale-multiplier × heartbeat` (default 3×15s) marks agents disconnected.
- Heavy use of ETag means most desired polls return 304; 200 only when spec changes or ETag is missing.
- Field index on `spec.deviceRef.name` avoids cluster-wide list scans when serving desired.

Binaries
--------
- `bin/apollo-deviceprocess-controller`
- `bin/apollo-deviceprocess-gateway`
- `bin/apollo-deviceprocess-agent`

Development
-----------
```
make fmt vet test build   # gofmt + vet + unit tests + build binaries into bin/
make generate             # deepcopy
make manifests            # CRDs into config/crd/bases
```

Contributing
------------
- fmt/vet/test/build before sending changes.
- Run `make manifests` when API types change to keep CRDs in sync.


