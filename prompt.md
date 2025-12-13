Cool — HTTP + local Docker Compose agents is a great MVP. The clean pattern here is:

* **Agent pulls desired state** from Device Gateway (poll or long-poll with ETag).
* **Agent pushes observations/heartbeat** back to Device Gateway (POST).
* **Only Device Gateway talks to Kubernetes** (read desired from cache, write `DeviceProcess.status`, emit Events).

Below is a **full Step 3 prompt** rewritten for HTTP + Docker Compose agents.

---

## Full Prompt — Step 3 (HTTP Gateway + Docker Compose Agents)

### Goal

Implement an MVP **device agent** that runs as a local container (docker compose) and talks **only** to a **Device Gateway** over HTTP to:

* Identify itself as device **X**
* Fetch desired `DeviceProcess` state for X
* Report **AgentConnected**, **SpecObserved(hash)**, and periodic **heartbeat**
* Never talk to Kubernetes directly

The **Device Gateway** is responsible for:

* Fetching desired state from Kubernetes (via controller/informer cache or direct client)
* Writing `DeviceProcess.status` updates to Kubernetes
* Emitting Kubernetes Events

This step **does not** run systemd/initd/container workloads yet. Only “observe & report”.

---

## Non-Negotiables

1. Agents do **not** use kubeconfig, service accounts, or Kubernetes APIs.
2. Gateway is the **only writer** of `DeviceProcess.status` and Events.
3. Gateway must **rate limit/coalesce** writes to apiserver (no “write on every heartbeat”).
4. Everything is **idempotent**:

   * repeated heartbeats don’t cause status churn
   * repeated “observed spec hash” doesn’t re-write status

---

## HTTP API Contract (MVP)

### Authentication (MVP-simple)

For docker compose MVP, use a shared key header:

* Agent sends: `X-Device-Token: <token>`
* Gateway maps token → deviceName OR validates token for provided `deviceName`

(You can swap to mTLS later without changing endpoint shapes.)

### 1) Fetch Desired State

Agent polls for desired state with caching.

**GET** `/v1/devices/{deviceName}/desired`

Response `200`:

```json
{
  "deviceName": "tor1-01",
  "heartbeatIntervalSeconds": 15,
  "items": [
    {
      "uid": "…optional…",
      "namespace": "infra-system",
      "name": "switch-agent-tor1-01",
      "generation": 2,
      "spec": { /* DeviceProcessSpec JSON (or normalized subset) */ },
      "specHash": "sha256:abcd…"
    }
  ]
}
```

Caching behavior:

* Gateway sets `ETag: "<desiredStateHash>"`
* Agent uses `If-None-Match: "<desiredStateHash>"`
* If unchanged, gateway returns `304 Not Modified` with empty body.

**MVP polling strategy**

* Normal poll every `heartbeatIntervalSeconds` (or 10s)
* If `304`, just sleep and retry
* Optional: support long-poll with `?waitSeconds=30` (gateway blocks until change or timeout)

### 2) Post Heartbeat + Observations

Agent reports observations back to gateway.

**POST** `/v1/devices/{deviceName}/report`

Request body:

```json
{
  "agentVersion": "0.1.0",
  "timestamp": "2025-12-12T23:55:00Z",
  "heartbeat": true,
  "observations": [
    {
      "namespace": "infra-system",
      "name": "switch-agent-tor1-01",
      "observedSpecHash": "sha256:abcd…"
    }
  ]
}
```

Response `200`:

```json
{
  "ack": true
}
```

### 3) Optional: First Connect Convenience

You can skip this and just use `/report`, but if you like:

**POST** `/v1/devices/{deviceName}/connect`

```json
{ "agentVersion": "0.1.0" }
```

Gateway emits a single Event `AgentConnected` and marks device as connected.

---

## Gateway Responsibilities (Step 3 Scope)

### A) Desired State Source

Gateway must be able to answer: “what `DeviceProcess` objects are targeted to device X?”

Implement either:

* **Simple MVP**: list/watch `DeviceProcess` and filter `spec.deviceRef.name == deviceName` (in-memory informer cache)
* **Better**: require controller to label processes with `apollo.azure.com/deviceName=<deviceName>` and gateway queries cache by label index

### B) Status Updates (Kubernetes write path)

When gateway receives `/report`:

* For each observed DP:

  * Patch `status.phase = "Pending"` (MVP)
  * Upsert condition `AgentConnected=True`
  * Upsert condition `SpecObserved=True` with message including observed hash
  * Set `status.observedSpecHash = <hash>` (or observedGeneration if you choose that model)

**Coalescing requirement**

* Do NOT write status on every heartbeat.
* Maintain `lastSeen[deviceName]` in memory updated on every report.
* Only write to Kubernetes when:

  * AgentConnected changes (False→True or True→False)
  * observedSpecHash changes
  * (optional) once every N minutes as a “sync” write

### C) Disconnect / Staleness Detection

If `now - lastSeen[deviceName] > 3 * heartbeatIntervalSeconds`:

* Patch all DeviceProcesses for device X with `AgentConnected=False`
* Emit a Warning Event (rate limited), e.g. `AgentDisconnected`

### D) Events

Gateway should emit:

* `Normal AgentConnected` once per connection (or first report)
* `Normal SpecObserved` when observedSpecHash changes
* `Warning AgentDisconnected` on staleness

---

## Agent Responsibilities (Step 3 Scope)

### A) Identity

Agent container requires:

* `APOLLO_DEVICE_NAME=tor1-01`
* `APOLLO_GATEWAY_URL=http://device-gateway:8080`
* `APOLLO_DEVICE_TOKEN=…`

If device name missing → exit with clear error.

### B) Main Loop

Per device:

1. `GET /desired` with `If-None-Match` support
2. For each desired item:

   * If `specHash` differs from local `lastObservedSpecHash[name]`:

     * send observation `{ name, namespace, observedSpecHash }`
     * store the hash locally (in memory is fine for MVP)
3. Every interval:

   * `POST /report` with `heartbeat=true` (include observations if any)
4. Reconnect/backoff:

   * exponential backoff + jitter on errors
   * do not hot-loop

### C) No Kubernetes, no workload execution

Step 3 is purely “observe desired, report status”.

---

## Definition of Done (DoD)

1. Apply a `DeviceProcess` in the cluster with `spec.deviceRef.name=tor1-01`
2. Start gateway (pointing to the cluster)
3. Start agent in docker compose with `APOLLO_DEVICE_NAME=tor1-01`
4. Within **≤ 5 seconds**:

   * `kubectl describe deviceprocess …` shows:

     * `phase: Pending`
     * `AgentConnected=True`
     * `SpecObserved=True`
     * `observedSpecHash` populated
   * Events appear from gateway
5. Edit `DeviceProcess.spec` (change args/env):

   * desired ETag changes
   * agent reports new observed hash
   * gateway updates status + emits `SpecObserved`
6. Stop the agent:

   * after staleness window, gateway sets `AgentConnected=False` + emits Warning event

---

## Docker Compose (example)

Provide a minimal compose file that runs N agents:

```yaml
services:
  agent-tor1-01:
    image: apollo/device-agent:dev
    environment:
      - APOLLO_DEVICE_NAME=tor1-01
      - APOLLO_GATEWAY_URL=http://host.docker.internal:8080
      - APOLLO_DEVICE_TOKEN=devtoken-tor1-01
  agent-tor1-02:
    image: apollo/device-agent:dev
    environment:
      - APOLLO_DEVICE_NAME=tor1-02
      - APOLLO_GATEWAY_URL=http://host.docker.internal:8080
      - APOLLO_DEVICE_TOKEN=devtoken-tor1-02
```

(If gateway is also in compose, use `http://device-gateway:8080` instead.)

---

## Output Required

1. Gateway HTTP server (Go preferred) implementing:

   * `GET /v1/devices/{device}/desired` with ETag/304
   * `POST /v1/devices/{device}/report`
   * in-memory `lastSeen` + staleness timer
   * Kubernetes status patch + event emission helpers
   * write coalescing / rate limiting
2. Agent implementation (Go preferred) implementing polling + report loop
3. Minimal RBAC for gateway (not for agent)
4. Test instructions with exact kubectl commands + curl examples

---

If you tell me where you want the gateway to run **right now** (a pod in cluster vs. local process using kubeconfig), I’ll tailor the “How to run” section and the compose networking (`host.docker.internal` vs. port-forward vs. NodePort) so it’s painless.
