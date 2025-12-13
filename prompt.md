Here’s a Copilot prompt for **Step 2 (Minimal Controller: Deployment ⇒ Processes)** that’s explicit about boundaries (SSA, no status writes, no rollout logic), and gives you a clean vertical slice starting with **NetworkSwitch** inventory only.

---

## Copilot Prompt — Step 2: Minimal Controller (Deployment ⇒ Processes, DaemonSet semantics)

Implement **Step 2** in this repo: a minimal Kubernetes controller that reconciles `DeviceProcessDeployment` into per-device `DeviceProcess` objects (DaemonSet semantics). This is the **controller only**. Do NOT implement rollout batching, health checks, artifact download, or agent execution. The controller must **never write `DeviceProcess.status`**.

### Scope / Goals

* Watch `DeviceProcessDeployment` resources.
* List device inventory CRDs for **one device kind only to start**: `NetworkSwitch` (assume it exists in-cluster).
* Match devices using `.spec.selector` (LabelSelector).
* For each matched device, **create or patch** a `DeviceProcess` that represents the desired workload on that device.
* Ensure garbage collection / cleanup when selector changes or deployment is deleted.

### Assumptions (for now)

* `NetworkSwitch` CRD exists and is namespaced.
* It has standard `metadata.labels`.
* We can access it via a typed client or (if no Go types exist) via `unstructured.Unstructured` with GVK:

  * Group: `azure.com`
  * Version: `v1alpha1`
  * Kind: `NetworkSwitch`
  * Resource: `networkswitches` (if unsure, discover from RESTMapper dynamically)
* The `DeviceProcessDeployment` is namespaced; create `DeviceProcess` objects in the **same namespace**.

---

## Implementation Requirements

### 1) Controller wiring

* Add controller manager wiring under `controller/main.go` (or new files under `controller/`):

  * register scheme for our CRDs
  * set up controller-runtime manager
  * register a reconciler for `DeviceProcessDeployment`

### 2) Reconciler behavior (core)

For each `DeviceProcessDeployment`:

1. Convert `.spec.selector` into a label selector and **list matching NetworkSwitch objects** in the same namespace.
2. For each matched NetworkSwitch named `<deviceName>`:

   * Compute the `DeviceProcess` name:

     * prefer stable: `<deploymentName>-<deviceName>`
     * if that exceeds DNS1123 label limits, use `<deploymentName>-<hash(deviceName)>` (keep deterministic)
   * Build desired `DeviceProcess` **spec** from deployment `.spec.template.spec` plus `deviceRef`:

     * `spec.deviceRef.kind = "NetworkSwitch"`
     * `spec.deviceRef.name = <deviceName>`
   * Apply labels to DeviceProcess:

     * `app: <deploymentName>`
     * copy a **safe subset** of device labels (or all labels) — but ensure we don’t exceed label size limits; at minimum keep device labels that drive queries (e.g. `role`, `type`, `rack`)
3. Ensure `ownerReferences` is set to the `DeviceProcessDeployment` so deletion GC works.
4. **Delete stale DeviceProcess objects** that belong to this deployment but no longer match the selector:

   * Identify owned DeviceProcesses via:

     * label `app=<deploymentName>` AND ownerRef UID, OR
     * label like `deviceprocessdeployment=<deploymentName>` (add this label)
   * If a previously created DeviceProcess corresponds to a device no longer selected, delete it.

### 3) Server-Side Apply (SSA) for spec ownership

* Use **Server-Side Apply** when creating/updating `DeviceProcess.spec`, so the controller and agent can manage different fields without stomping:

  * Use a consistent field manager name: `"deviceprocess-controller"`
  * Use `ForceOwnership=false` by default (do NOT steal fields from agent)
  * Only apply `metadata.labels`, `metadata.ownerReferences`, and `spec` (not status)
* The reconciler should be idempotent: repeated reconciles produce no changes if nothing changed.

### 4) Never write status

* Do not set `.status` on `DeviceProcess`.
* You MAY update `.status.observedGeneration` on the **DeviceProcessDeployment** later in Step 7; for Step 2, it’s OK to not update deployment status at all.

### 5) Events + logging

* Log key actions:

  * how many devices matched
  * created / updated / deleted counts
* Emit Kubernetes events on the `DeviceProcessDeployment` for:

  * “CreatedDeviceProcess”
  * “DeletedDeviceProcess”
    (Keep event spam bounded: only emit per reconcile summary or only on changes.)

### 6) RBAC

Add the kubebuilder RBAC markers (or explicit RBAC yaml) so the controller can:

* get/list/watch DeviceProcessDeployments
* create/patch/delete/get/list/watch DeviceProcesses
* get/list/watch NetworkSwitch inventory objects
* create events

### 7) Tests (minimum viable)

Add unit tests using envtest or fake client:

* Reconcile creates N DeviceProcesses when N NetworkSwitch match selector.
* Changing selector deletes stale DeviceProcesses.
* Deleting deployment leads to GC via ownerRef (at least verify ownerRef is set correctly; optional: simulate deletion).

---

## Deliverables / Definition of Done

* `kubectl apply -f <deployment sample>` creates N `DeviceProcess` objects for matching NetworkSwitches.
* `kubectl delete deviceprocessdeployment <name>` results in DeviceProcesses being garbage collected (ownerRef).
* Changing selector changes created/deleted DeviceProcesses accordingly.
* Controller uses SSA and never touches DeviceProcess status.

---

## Output requested

After implementation, show:

* list of new/modified files
* the reconciler code in full
* any RBAC markers or YAML
* how to run locally (make targets) and a quick manual demo command sequence

Constraints:

* No rollout batching or update strategy logic yet.
* No querying multiple device kinds yet (NetworkSwitch only).
* Keep naming deterministic, safe (DNS1123), and stable.

---
