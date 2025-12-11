# Praetor

Small Go-based device-management simulation with a manager and three agents running via Docker Compose.

## Structure
- `praetor/manager`: HTTP manager service
- `praetor/agent`: polling agent and Dockerfile
- `praetor/docker-compose.yml`: brings up manager + three agents

## Run
```sh
docker compose up --build
```

## Development
- Each service is its own Go module (`agent/go.mod`, `manager/go.mod`) tied together with the workspace file `go.work`.
- Run tests per module, e.g.:
  ```sh
  go test ./agent/...
  go test ./manager/...
  ```

## Manager HTTP API
- `POST /register` — agent registration with `deviceId`, `agentVersion`, `deviceType` (idempotent; updates `lastSeen`).
- `GET /desired/<deviceId>` — fetches the current global desired state (per-device path, but single global payload).
- `POST /updateDesired` — update global desired state: `{"version":"vX","command":[...args...]}`.
- `POST /status` — agent posts execution status: `deviceId`, `version`, `state`, `message`.
- `GET /devices/registered` — list registered devices with metadata and timestamps.
- `GET /devices` — list latest statuses posted by agents.

## Agent behavior
- On start, registers via `/register` with its `DEVICE_ID`, `AGENT_VERSION` (default `1.0.0`), and `DEVICE_TYPE` (default `simulated`).
- Polls `/desired/<DEVICE_ID>` every 2s; if `version` changes, executes `command` and posts `/status`.
- Retries registration, desired fetch, and status posts with backoff.

## Update desired state at runtime
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
- Manager prints status reports it receives.

## Clean up
```sh
docker compose down -v
```
