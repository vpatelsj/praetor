# Apollo DeviceProcess (Step 3)

HTTP gateway + docker-compose-friendly agent that only talks to the gateway (no kubeconfig on devices).

## Dev loop

```bash
make generate
make manifests
make test
make build
```

## Binaries

- `bin/apollo-deviceprocess-controller`
- `bin/apollo-deviceprocess-agent`
- `bin/apollo-deviceprocess-gateway`

## How to run (MVP)

1) Start the gateway (in-cluster or local kubeconfig):

```bash
bin/apollo-deviceprocess-gateway \
	--addr=:8080 \
	--default-heartbeat-seconds=15 \
	--stale-multiplier=3 \
	--device-token=devtoken
```

2) Start an agent (no kubeconfig on the device):

```bash
APOLLO_DEVICE_NAME=tor1-01 \
APOLLO_GATEWAY_URL=http://localhost:8080 \
APOLLO_DEVICE_TOKEN=devtoken \
bin/apollo-deviceprocess-agent
```

3) Apply a DeviceProcess targeting the device:

```bash
kubectl apply -f config/samples/azure_v1alpha1_deviceprocess.yaml
```

4) Verify status (≤ 5s):

```bash
kubectl describe deviceprocess -n infra-system switch-agent-tor1-01 | sed -n '1,80p'
```

You should see `phase: Pending`, `AgentConnected=True`, `SpecObserved=True`, and `observedSpecHash` populated. Stopping the agent should flip `AgentConnected=False` after ~3x heartbeat and emit a Warning event.

## HTTP curl examples

```bash
# Fetch desired with caching
curl -v -H "X-Device-Token: devtoken" http://localhost:8080/v1/devices/tor1-01/desired

# Post heartbeat + observations
curl -v -X POST -H "Content-Type: application/json" -H "X-Device-Token: devtoken" \
	-d '{"agentVersion":"dev","timestamp":"2025-12-12T00:00:00Z","heartbeat":true,"observations":[{"namespace":"infra-system","name":"switch-agent-tor1-01","observedSpecHash":"sha256:abcd"}]}' \
	http://localhost:8080/v1/devices/tor1-01/report
```

## Docker Compose (example)

See [examples/docker-compose.yaml](examples/docker-compose.yaml) for a minimal gateway + two-agent setup. Point the gateway at your cluster via kubeconfig or in-cluster credentials; agents only need the gateway URL/token.
