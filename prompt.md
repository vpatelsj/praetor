Here’s a Copilot prompt for **Step 1 (API-first CRDs)** that’s tight, explicit, and keeps you from accidentally drifting into Step 2 logic.

---

## Copilot Prompt — Step 1: CRDs (API-first)

Read the repo and implement **Step 1: finalize CRD APIs** for `DeviceProcess` and `DeviceProcessDeployment` under `api/azure.com/v1alpha1`. This step is **API-only**: define types, markers, validations, defaults, printcolumns, and sample YAMLs. **Do not implement any controller reconciliation logic or agent behavior**.

### Goals

1. CRDs have the full spec + status shape we agreed on.
2. `make generate && make manifests` produces clean CRDs.
3. `kubectl get/describe` looks good from printcolumns + docs, even if nothing runs.

---

## Requirements

### 1) DeviceProcess API

Implement these Go types in `api/azure.com/v1alpha1/deviceprocess_types.go` (or equivalent) with kubebuilder markers + validation.

#### Spec fields (required unless stated optional)

* `spec.deviceRef`:

  * `kind` (string, required; enum: `Server`, `NetworkSwitch`, `SOC`, `BMC`)
  * `name` (string, required)
  * optional `namespace` (string, omit if always same namespace)
* `spec.artifact`:

  * `type` (string enum: `oci`, `http`, `file`)
  * `url` (string, required, must be non-empty)
  * optional `checksumSHA256` (string; if provided must be 64 hex chars)
* `spec.execution`:

  * `backend` (string enum: `systemd`, `initd`, `container`)
  * `command` (`[]string`, required, minItems=1)
  * optional `args` (`[]string`)
  * optional `env` (`[]EnvVar` with `name` and `value`)
  * optional `workingDir` (string)
  * optional `user` (string)  (keep optional for now)
* `spec.restartPolicy` (string enum: `Always`, `OnFailure`, `Never`; default `Always`)
* `spec.healthCheck` (optional):

  * support only `exec` for now:

    * `exec.command` (`[]string`, minItems=1)
  * `periodSeconds` (int32 default 30, min 1)
  * `timeoutSeconds` (int32 default 5, min 1)
  * `successThreshold` (int32 default 1, min 1)
  * `failureThreshold` (int32 default 3, min 1)

#### Status fields

* `status.phase` (enum string: `Pending`, `Running`, `Succeeded`, `Failed`, `Unknown`)
* `status.conditions` (`[]metav1.Condition`)
* `status.artifactVersion` (string, optional; typically tag/digest)
* `status.pid` (int64, optional)
* `status.startTime` (`*metav1.Time`, optional)
* `status.lastTransitionTime` (optional if you want, but conditions should carry timestamps)
* `status.restartCount` (int32, optional)
* `status.lastTerminationReason` (string, optional)

#### Kubebuilder markers / UX

* Enable status subresource
* Add printcolumns similar to:

  * DEVICE = `.spec.deviceRef.name`
  * KIND = `.spec.deviceRef.kind`
  * PHASE = `.status.phase`
  * VERSION = `.status.artifactVersion`
  * RESTARTS = `.status.restartCount`
  * AGE = `.metadata.creationTimestamp`

Add godoc comments on fields so `kubectl describe` is understandable.

---

### 2) DeviceProcessDeployment API

Implement in `api/azure.com/v1alpha1/deviceprocessdeployment_types.go` with validation and printcolumns.

#### Spec fields

* `spec.selector` (required): `metav1.LabelSelector`
* `spec.updateStrategy`:

  * `type` enum: `RollingUpdate`, `Recreate` (default `RollingUpdate`)
  * if RollingUpdate:

    * `rollingUpdate.maxUnavailable`:

      * allow either `int` or percentage string (use `intstr.IntOrString`)
      * default `10%`
* `spec.template` (required):

  * `metadata`:

    * `labels` map[string]string (optional but should exist so controller can copy later)
  * `spec` should embed a **DeviceProcessTemplateSpec** (same fields as `DeviceProcessSpec` *except* `deviceRef`)

Important: Do NOT bake in any “inventory CRD” dependencies here; the selector is just labels.

#### Status fields

* `status.observedGeneration` (int64)
* Counters (int32):

  * `desiredNumberScheduled`
  * `currentNumberScheduled`
  * `updatedNumberScheduled`
  * `numberReady`
  * `numberAvailable`
  * `numberUnavailable`
* `status.conditions` (`[]metav1.Condition`), with planned condition types like:

  * `Available`, `Progressing`

#### Printcolumns for `kubectl get deviceprocessdeployment`

* DESIRED, CURRENT, READY, AVAILABLE, UNAVAILABLE, AGE

---

### 3) Defaults + validation

* Add kubebuilder defaults + validation markers where sensible:

  * enums, minItems, minimums, patterns (sha256)
  * defaults for restartPolicy + health thresholds + rollingUpdate maxUnavailable

---

### 4) Samples

Update `config/samples/` to include:

* `azure_v1alpha1_deviceprocess.yaml` showing a realistic systemd example
* `azure_v1alpha1_deviceprocessdeployment.yaml` showing selector + template + rollingUpdate 10%

Samples should match the API and validate under the CRDs.

---

### 5) DoD checks

After changes, ensure:

* `make fmt vet test generate manifests build` all succeed
* `kubectl apply -f config/crd/bases` works
* `kubectl apply -f config/samples` works
* `kubectl get deviceprocess` and `kubectl get deviceprocessdeployment` show the new columns

---

### Output

Provide:

* file list of changed/added files
* full content of the two types files and updated sample YAMLs

Constraints:

* API-only. No controller logic, no agent logic, no new reconcilers.
* Keep types minimal and stable; prefer adding optional fields over complexity.
* Use `metav1.Condition` for conditions (don’t invent a custom condition struct).

---

If you want, I can also give you a tiny “API review checklist” to run after Copilot finishes (things like “avoid pointer slices”, “prefer int32 for counters”, “don’t forget DeepCopy markers”, etc.).
