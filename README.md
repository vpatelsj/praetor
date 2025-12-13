# Apollo DeviceProcess (Step 1)

API-first CRDs for DeviceProcess and DeviceProcessDeployment (no controller behavior yet).

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
