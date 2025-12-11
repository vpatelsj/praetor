# Praetor

Small Go-based device-management simulation with a manager service and multiple polling agents that communicate over HTTP. Use the Docker Compose setup to bring everything up locally.

## Project layout
- `manager/`: HTTP manager that tracks device registration, desired state, heartbeats, and status reports.
- `agent/`: Lightweight agent that registers itself, polls desired state, executes commands, reports status, and sends heartbeats.
- `docker-compose.yml`: Builds and starts the manager plus multiple agents (default `device1`, `device2`, `device3`).

## Running the stack
```sh
docker compose up --build
```
The manager listens on `localhost:8080`. Agents talk to it over the Docker network.

## Manager HTTP API
- `POST /register` — register an agent with `deviceId`, `agentVersion`, and `deviceType`; idempotent and refreshes `lastSeen`.
- `POST /heartbeat` — lightweight heartbeat with `deviceId`; updates `lastSeen` so online/offline can be inferred.
- `GET /desired/<deviceId>` — fetch the current desired state. The payload is global, but the path is per-device for clarity.
- `POST /updateDesired` — update global desired state, e.g. `{ "version": "v2", "command": ["echo", "Hello"] }`.
- `POST /status` — post an execution status with `deviceId`, `version`, `state`, and `message`.
- `GET /devices/registered` — list registered devices with metadata, `registeredAt`/`lastSeen`, and an `online` boolean (online if heartbeat/status within 15s).
- `GET /devices` — list the last status per device along with an `online` indicator derived from the most recent heartbeat/status.

The manager starts with desired state `v1` that echoes `"Hello from Praetor v1!"`.

## Agent behavior
- Environment:
  - `DEVICE_ID` (required) — unique identifier per container.
  - `AGENT_VERSION` (default: `1.0.0`)
  - `DEVICE_TYPE` (default: `simulated`)
- On startup, the agent registers with the manager and begins polling `/desired/<DEVICE_ID>` every 2s.
- Desired state changes trigger command execution (via `exec.CommandContext`) and a POST to `/status` containing the result (`state` + `message`).
- Heartbeats are sent to `/heartbeat` every 5s to keep `lastSeen` fresh, and failed requests/decodes are retried with backoff.

## Updating desired state at runtime
```sh
curl -X POST http://localhost:8080/updateDesired \
  -H "Content-Type: application/json" \
  -d '{"version":"v2","command":["echo","Hello from Praetor v2"]}'
```
Check current desired for a device:
```sh
curl http://localhost:8080/desired/device1
```

## Observing logs
- All services: `docker compose logs -f`
- Manager only: `docker compose logs -f manager`
- Agents only: `docker compose logs -f device1 device2 device3`

## Expected behavior
- Agents poll the manager every 2s, detect desired-version changes, run the command, and POST status back.
- Heartbeats keep devices marked online; if the manager has not heard from a device in 15s, it reports `online: false` in list endpoints.
- Manager logs registrations, desired updates, heartbeats, and status reports.

## Clean up
```sh
docker compose down -v
```
