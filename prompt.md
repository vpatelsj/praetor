Cool — here’s an updated **Copilot prompt** tuned for **plain `go build`** (no goreleaser), with separate controller + agent binaries.

---

### Copilot Prompt — Step 0 (Foundations, go build)

Bootstrap a Kubernetes-native Go project for “Apollo DeviceProcess” with CRDs + controller + agent skeleton. This is **Step 0: foundations only**. Use **plain `go build`** for binaries.

## Goals

* Create a clean repo layout, codegen, CRD generation, and build/test targets.
* Everything compiles and runs (controller prints startup logs; agent prints startup logs).
* No rollout logic, no artifact download, no systemd/initd/container execution yet.

## Repo layout to create

* `api/azure.com/v1alpha1/` : CRD Go types (`DeviceProcess`, `DeviceProcessDeployment`) + deepcopy
* `controller/` : controller manager entrypoint using controller-runtime
* `agent/` : agent entrypoint (no k8s watch yet)
* `pkg/conditions/` : shared helpers for Conditions
* `pkg/version/` : version info (string vars)
* `pkg/log/` : zap logger setup helper
* `config/` : kubebuilder-style manifests (`crd/`, `rbac/`, `manager/`, `default/`, `samples/`)
* `hack/` : scripts if needed

## Tooling / deps

* Use `sigs.k8s.io/controller-runtime` + k8s apimachinery/apimachinery.
* Use `controller-gen` for CRD + RBAC generation.
* Ensure versions are consistent (no mixed major versions).

## API conventions

* CRDs under group/version: `azure.com/v1alpha1`
* Add kubebuilder markers:

  * `+kubebuilder:object:root=true`
  * `+kubebuilder:subresource:status`
  * `+kubebuilder:resource:scope=Namespaced`
  * `+kubebuilder:printcolumn` (placeholders ok)
* Define enums/constants:

  * DeviceProcess phases: `Pending, Running, Succeeded, Failed, Unknown`
  * Condition types: `ArtifactDownloaded, ProcessStarted, Healthy, AgentConnected`
* In `pkg/conditions`, implement helpers:

  * `FindCondition`, `SetCondition`, `MarkTrue`, `MarkFalse`
  * `LastTransitionTime` should only change when Status changes

## Entrypoints

* `controller/main.go`:

  * starts a controller-runtime manager
  * registers scheme for our API types
  * logs startup and exits on signal
  * **no reconcilers required yet** (stub ok)
* `agent/main.go`:

  * simple flags/env parsing (`--device-id`, `--kubeconfig` optional)
  * logs “agent starting” and blocks (or exits cleanly)

## Build & Makefile

Provide Makefile targets using **go build**:

* `make fmt` -> `go fmt ./...`
* `make vet` -> `go vet ./...`
* `make test` -> `go test ./...`
* `make generate` -> run controller-gen for deepcopy + object generation
* `make manifests` -> controller-gen CRD + RBAC output into `config/crd/bases` and `config/rbac`
* `make build` -> builds:

  * `bin/apollo-deviceprocess-controller` from `./controller`
  * `bin/apollo-deviceprocess-agent` from `./agent`

Include a `tools.go` pattern or Makefile download logic so `controller-gen` is available (preferred: `tools.go` + `go install` via Makefile).

## Config

* kustomize structure under `config/` sufficient so `make manifests` produces CRDs.
* Add minimal sample YAMLs under `config/samples/` for both CRDs.

## README

* Provide minimal README with dev loop:

  * `make generate && make manifests && make test && make build`

## Output format

At the end, show:

1. the final file tree
2. full contents of: `go.mod`, `Makefile`, `api/azure.com/v1alpha1/*.go`, `pkg/conditions/conditions.go`, `controller/main.go`, `agent/main.go`, and README.

Constraints: keep it minimal and idiomatic. Don’t implement business logic beyond scaffolding.
