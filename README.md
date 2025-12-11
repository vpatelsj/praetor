# Praetor

Small Go-based device-management simulation with a manager service and multiple polling agents that communicate over HTTP. Use the Docker Compose setup to bring everything up locally.

## Project layout
- `manager/`: HTTP manager that tracks device registration, desired state/rollouts, heartbeats, selector-based targeting, and status reports.
- `agent/`: Lightweight agent that registers itself, polls rollout assignments, executes commands, reports status, and sends heartbeats.
- `docker-compose.yml`: Builds and starts the manager plus multiple agents representing different device types (servers, switches, DPUs, SOCs, BMCs, simulators).

## Running the stack
```sh
docker compose up --build
```
The manager listens on `localhost:8080`. Agents talk to it over the Docker network.

## Manager HTTP API
- `POST /register` — register an agent with `deviceId`, `agentVersion`, `deviceType`, `labels`, and `capabilities`; idempotent and refreshes `lastSeen`.
- `POST /heartbeat` — lightweight heartbeat with `deviceId`; updates `lastSeen` so online/offline can be inferred.
- `GET /rollout/<deviceId>` — fetch the current rollout assignment (generation id, version, command) if the device matches the active selector; returns 204 when not targeted.
- `POST /rollout` — start a rollout targeting devices by labels with payload `{ "version": "v2", "command": ["echo", "Hello"], "matchLabels": {"rack":"demo"}, "maxFailureRatio": 0.25 }`; omitting `command` reuses the previous desired command.
- `POST /rolloutStatus` — update rollout progress for a device with `{ deviceId, generationId, state: "Succeeded"|"Failed", message }`; pauses the generation if the failure ratio exceeds `maxFailureRatio`.
- `GET /desired/<deviceId>` — legacy desired-state endpoint; returns 204 when the device does not match the active selector.
- `POST /status` — post an execution status with `deviceId`, `version`, `state`, and `message`.
- `GET /devices/registered` — list registered devices with metadata, `registeredAt`/`lastSeen`, online indicator, and whether they are selected by the active labels.
- `GET /devices` — list the last status per device along with device metadata, online indicator, and selector match.

The manager starts with desired state `v1` that echoes `"Hello from Praetor v1!"`.

## Agent behavior
- Environment:
  - `DEVICE_ID` (required) — unique identifier per container.
  - `AGENT_VERSION` (default: `1.0.0`)
  - `DEVICE_TYPE` (default: `Simulator`) — determines default labels/capabilities.
- On startup, the agent registers with the manager using labels `{rack:"demo", role:<per-device-type>}` and default capabilities for the device type.
- The agent polls `/rollout/<DEVICE_ID>` every 2s and applies new generations sequentially.
- Desired state changes trigger command execution (via `exec.CommandContext`) and POSTs to `/status` and `/rolloutStatus`.
- Heartbeats are sent to `/heartbeat` every 5s to keep `lastSeen` fresh, and failed requests/decodes are retried with backoff.

## Device types and capabilities
Allowed capabilities vary by device type; invalid combinations are rejected at registration.

- Server: `systemd`, `container`, `raw-binary`
- NetworkSwitch: `systemd`, `raw-binary`
- DPU: `systemd`, `container`, `raw-binary`
- SOC: `systemd`, `raw-binary`
- BMC: `initd`, `raw-binary`
- Simulator: `systemd`, `container`, `raw-binary`, `initd`

Default capabilities sent by the agent are `systemd` + `raw-binary` for most types and `initd` + `raw-binary` for BMCs.

## Updating desired state at runtime
Trigger a rollout targeting a label set:
```sh
curl -X POST http://localhost:8080/rollout \
  -H "Content-Type: application/json" \
  -d '{"version":"v2","command":["echo","Hello from Praetor v2"],"matchLabels":{"rack":"demo"},"maxFailureRatio":0.25}'
```
Check the active assignment for a device:
```sh
curl http://localhost:8080/rollout/device1
```
Report rollout completion manually (if needed):
```sh
curl -X POST http://localhost:8080/rolloutStatus \
  -H "Content-Type: application/json" \
  -d '{"deviceId":"device1","generationId":1,"state":"Succeeded","message":"done"}'
```

## Observing logs
- All services: `docker compose logs -f`
- Manager only: `docker compose logs -f manager`
- Agents only: `docker compose logs -f server1 server2 switch1 switch2 dpu1 dpu2 soc1 soc2 bmc1 bmc2 sim1 sim2`

## Expected behavior
- Agents poll the manager every 2s, detect rollout/version changes, run the command, and POST status and rollout results back.
- Heartbeats keep devices marked online; if the manager has not heard from a device in 15s, it reports `online: false` in list endpoints.
- Manager logs registrations, rollouts, heartbeats, and status/rollout reports.

## Clean up
```sh
docker compose down -v
```
