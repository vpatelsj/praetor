# Praetor

Praetor is a Go-based device management simulator: a manager HTTP service tracks devices and type-scoped rollouts, while lightweight agents register, heartbeat, and execute rollout commands. Docker Compose spins up a full demo fleet (servers, switches, DPUs, SOCs, BMCs, simulators) against one manager.

## Components
- `manager/`: HTTP manager keeping device registry, last-seen status, and rollout state per device type.
- `agent/`: Shared agent logic with per-device binaries in `cmd/praetor-agent-*` (switch, dpu, soc, bmc, server, simulator).
- `praectl/`: CLI for creating, updating, listing, watching rollouts and querying devices via the manager API.
- `docker-compose.yml`: Demo topology that builds the manager and launches two of each agent type.

Supported device types: `switch`, `dpu`, `soc`, `bmc`, `server`, `simulator`.

## How it works
- Agents start with `--device-id` (or `DEVICE_ID`) and `--manager-address` (default `http://manager:8080`).
- On startup agents register, send heartbeats every 5s, poll `/api/v1/devicetypes/<type>/rollouts` every 5s, and execute matching rollouts in `Running` state.
- Rollout selectors are simple `key=value` label matches (empty selector targets all devices of that type). Successes/failures are recorded per rollout generation and the controller reconciles state to `Running`, `Paused`, or `Succeeded`.

## Quick start (demo stack)
```sh
docker compose up --build
```
Manager listens on `localhost:8080`; agents talk over the compose network.

To shut down and clean volumes:
```sh
 docker compomse down
```

## CLI setup (`praectl`)
From `praectl/` you can run in-place or build a binary:
```sh
cd praectl
go build -o praectl .
./praectl rollout list
```
`--server` defaults to `http://localhost:8080`.

## Use cases with commands

### 1) Bring up the demo fleet
- Start everything: `docker compose up --build -d`
- Tail logs: `docker compose logs -f manager` or `docker compose logs -f server1 switch1 dpu1 soc1 bmc1 sim1`

### 2) Inspect devices
- List devices of a type: `praectl get devices --type switch`
- Describe a single device: `praectl describe device switch switch1`

### 3) Create a rollout
Targets a device type with optional label selectors, max failure ratio, and command (space-split).
```sh
praectl rollout create switch upgrade-switches-v1 \
  --version v1.4.3 \
  --selector role=switch \
  --max-failures 0.1 \
  --command "echo applying v1.4.3"
```

### 4) Update a rollout (bumps generation)
```sh
praectl rollout update switch upgrade-switches-v1 \
  --version v1.5.0 \
  --selector role=switch \
  --max-failures 0.05
```

### 5) List, get, and watch rollouts
- List all types: `praectl rollout list`
- List one type: `praectl rollout list server`
- Get details: `praectl rollout get switch upgrade-switches-v1`
- Watch progress (polling): `go run ./praectl rollout watch switch upgrade-switches-v1`

### 6) Run a standalone agent locally
```sh
go run ./agent/cmd/praetor-agent-switch --device-id=my-switch --manager-address=http://localhost:8080
```
Flags accept env vars `DEVICE_ID` and `MANAGER_ADDRESS`.

### 7) Talk to the HTTP API directly
- Register: `curl -X POST http://localhost:8080/register -H 'Content-Type: application/json' -d '{"deviceId":"dev1","deviceType":"switch","labels":{"role":"switch"}}'`
- Heartbeat: `curl -X POST http://localhost:8080/heartbeat -H 'Content-Type: application/json' -d '{"deviceId":"dev1"}'`
- Report status: `curl -X POST http://localhost:8080/status -H 'Content-Type: application/json' -d '{"deviceId":"dev1","version":"v1","state":"Running","message":"starting"}'`
- List device statuses: `curl http://localhost:8080/api/v1/devices?type=switch`
- List devices of a type: `curl http://localhost:8080/api/v1/devicetypes/switch/devices`
- Rollouts CRUD (per type):
  - Create: `curl -X POST http://localhost:8080/api/v1/devicetypes/switch/rollouts -H 'Content-Type: application/json' -d '{"name":"r1","version":"v1","selector":{"role":"switch"},"maxFailures":0.2}'`
  - Get: `curl http://localhost:8080/api/v1/devicetypes/switch/rollouts/r1`
  - List: `curl http://localhost:8080/api/v1/devicetypes/switch/rollouts`
  - Update: `curl -X PATCH http://localhost:8080/api/v1/devicetypes/switch/rollouts/r1 -H 'Content-Type: application/json' -d '{"version":"v1.1","selector":{"role":"switch"},"maxFailures":0.1}'`
  - Agent status update: `curl -X POST http://localhost:8080/api/v1/devicetypes/switch/rollouts/r1/status -H 'Content-Type: application/json' -d '{"deviceId":"dev1","generation":2,"state":"Succeeded","message":"ok"}'`

## Troubleshooting
- Missing devices: ensure the agent had `--device-id` set and can reach the manager URL.
- Rollout stuck in Planned/Running: check selectors match device labels and that agents can execute the command; `--max-failures` pauses when failure ratio is reached.
- Modify defaults: poll and heartbeat intervals are 5s in agent code (`agent/pkg/agent`).
