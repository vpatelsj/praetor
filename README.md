# Praetor

Praetor is a Go-based device-management simulation with a manager service and polling agents that communicate over HTTP. Docker Compose brings up a local fleet of agents (servers, switches, DPUs, SOCs, BMCs, simulators) against one manager.

## Project layout
- `manager/`: HTTP manager tracking registration, status, rollouts, heartbeats, and selector-based targeting.
- `agent/`: Shared agent logic (`pkg/agent`) plus per-device entrypoints in `cmd/praetor-agent-*`.
- `praectl/`: CLI for creating/listing rollouts and querying devices.
- `docker-compose.yml`: Single compose file that builds the manager and a multi-device fleet of agents.

## Run the stack
```sh
docker compose up --build
```
Manager listens on `localhost:8080`; agents talk over the compose network.

## Manager HTTP API (current)
- `POST /register` — register `{deviceId, deviceType, labels}` (idempotent, refreshes lastSeen).
- `POST /heartbeat` — `{deviceId}` lightweight heartbeat.
- `POST /status` — report execution status `{deviceId, version, state, message}`.
- `GET /api/v1/devices` — list device statuses; filter with `?type=<deviceType>`.
- `GET /api/v1/devicetypes/<deviceType>/devices` — list devices by type.
- Rollouts (per device type):
  - `GET /api/v1/devicetypes/<deviceType>/rollouts` — list rollouts.
  - `GET /api/v1/devicetypes/<deviceType>/rollouts/<name>` — get rollout.
  - `POST /api/v1/devicetypes/<deviceType>/rollouts` — create rollout `{name, version, selector, maxFailures}`.
  - `POST /api/v1/devicetypes/<deviceType>/rollouts/<name>/status` — device updates rollout status `{deviceId, generation, state, message}`.

Legacy endpoints like `/rollout`, `/desired`, and non-typed rollout routes have been removed.

## Device types & labels
Supported types: `switch`, `dpu`, `soc`, `bmc`, `server`, `simulator`.
Agents register with labels `{rack: "demo", role: <per-type default>}` and capabilities per type. Rollout selectors match against these labels; an empty selector targets all devices of that type.

## Agent behavior (per-device binaries)
- Per-device entrypoints: `praetor-agent-switch`, `praetor-agent-dpu`, `praetor-agent-soc`, `praetor-agent-bmc`, `praetor-agent-server`, `praetor-agent-simulator`.
- Flags/env: `--device-id` (or `DEVICE_ID`) and `--manager-address` (default `http://manager:8080`, overridable via `MANAGER_ADDRESS`).
- Registers on startup, sends heartbeats every 5s, polls typed rollouts every 5s using the device type baked into each binary.
- When targeted by a rollout in Running state and matching selector, runs the command (or no-op message), posts rollout status (`Succeeded`/`Failed`), and tracks applied generations locally.

## CLI (praectl)
From `praectl/` you can run directly or build/install.

Create a rollout (per type):
```sh
go run . rollout create switch switch-r3 --version v3.2.0 --selector rack=demo --selector role=switch --max-failures 0.1
```
Create a rollout with an explicit command (space-split; avoid nested quotes):
```sh
go run . rollout create switch upgrade-switch-agent-v3 --version v1.4.3 --selector role=switch --max-failures 0.05 --command "echo applied v1.4.3"
```

List rollouts:
- All types: `go run . rollout list`
- Single type: `go run . rollout list server`

List devices by type:
```sh
go run . get devices --type switch
```

To use a built binary instead of `go run`, `go build -o praectl .` and run `./praectl ...` with `--server` if needed (default `http://localhost:8080`).

## Observing logs
- All services: `docker compose logs -f`
- Manager: `docker compose logs -f manager`
- Agents: `docker compose logs -f server1 server2 switch1 switch2 dpu1 dpu2 soc1 soc2 bmc1 bmc2 sim1 sim2`

## Clean up
```sh
docker compose down -v
```
