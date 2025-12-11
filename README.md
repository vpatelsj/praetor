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

## Update desired state at runtime
```sh
curl -X POST http://localhost:8080/desired \
  -H "Content-Type: application/json" \
  -d '{"version":"v2","command":["echo","Hello from Praetor v2"]}'
```
Check current state:
```sh
curl http://localhost:8080/desired
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
