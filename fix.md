I went through your Step 1 zip. You’re **90%+ aligned with the plan**: the API shapes are there, validations/defaults mostly look right, CRDs generate cleanly, and the sample YAMLs are realistic.

Here’s what I’d be **critical about**—stuff that will either bite you later or is missing from the Step 1 “API-first” DoD.

## 1) Biggest API issue: `spec` is optional right now (should not be)

Both CRs define:

* `DeviceProcess.Spec   json:"spec,omitempty"`
* `DeviceProcessDeployment.Spec json:"spec,omitempty"`

That makes `.spec` *not required* in the CRD schema. You can create objects with no spec and the API server will accept them.

**Fix:** remove `omitempty` on Spec fields (and keep Status as `omitempty` if you want):

```go
Spec   DeviceProcessSpec   `json:"spec"`
Status DeviceProcessStatus `json:"status,omitempty"`
```

Same for Deployment. This is an easy win and prevents junk objects from existing.

## 2) Missing printcolumn: `UPDATED` for Deployment

Your Deployment has `updatedNumberScheduled` in status, but `kubectl get deviceprocessdeployment` printcolumns don’t show it.

Your earlier UX examples had UPDATED, and it’s genuinely useful.

**Fix:** add:

```go
//+kubebuilder:printcolumn:name="UPDATED",type=integer,JSONPath=`.status.updatedNumberScheduled`
```

…so your `kubectl get` matches the “DaemonSet-style” mental model.

## 3) Your ConditionType exists, but you didn’t define the actual condition constants

`api/.../conditions_types.go` only defines:

```go
type ConditionType string
```

…but no constants like `ArtifactDownloaded`, `ProcessStarted`, etc.

In Step 1, you want these **typed + centralized** so controller/agent don’t invent slightly different strings later.

**Fix:** define condition consts in the API package, e.g.:

```go
const (
  ConditionAgentConnected   ConditionType = "AgentConnected"
  ConditionArtifactDownloaded ConditionType = "ArtifactDownloaded"
  ConditionProcessStarted    ConditionType = "ProcessStarted"
  ConditionHealthy           ConditionType = "Healthy"

  ConditionAvailable    ConditionType = "Available"
  ConditionProgressing  ConditionType = "Progressing"
)
```

If you don’t plan to use `ConditionType`, delete it and use raw strings everywhere—but don’t leave it half-done.

## 4) Deployment strategy defaults are slightly “footgunny”

You default `updateStrategy.type=RollingUpdate`, but `rollingUpdate` can be nil. That means later controller code has to treat nil as “use defaults”.

That’s fine, but it’s easy to mess up and accidentally interpret nil as “0 maxUnavailable”.

**Two improvements (pick one):**

* Add a **CEL XValidation**: if type is RollingUpdate, rollingUpdate must be set.
* Or default the rollingUpdate struct itself (harder with kubebuilder defaults, but possible).

At minimum: add a comment + handle nil explicitly in controller later.

## 5) Template metadata is too minimal (future API churn risk)

`DeviceProcessTemplateMetadata` only supports `labels`. You will almost certainly want `annotations` soon (version pins, rollout markers, owner metadata, etc.).

Adding it now is safe (backward compatible), adding it later is also compatible, but it’s the kind of thing people want immediately once rollouts exist.

**Recommend add now:**

```go
Annotations map[string]string `json:"annotations,omitempty"`
```

## 6) Status field types: pointers where you probably don’t want nullability

You made:

* `PID *int64`
* `RestartCount *int32`

This causes CRD schema to allow null and makes client UX slightly uglier. In most k8s APIs, these are non-pointer with `omitempty` and default to 0 when absent.

**Recommendation:** make them non-pointer unless you truly need “unset vs 0” semantics.

## 7) Minor: Scheme registration location/style

You register both `DeviceProcess*` and `DeviceProcessDeployment*` in `deviceprocess_types.go`’s `init()`.

It works, but it’s surprising and easy to break during refactors. Prefer:

* each types file registers its own types, or
* a dedicated `register.go`.

Not blocking, just cleanup.

## 8) Not Step 1, but still a real footgun: `config/default` still applies samples

`config/default/kustomization.yaml` includes `../samples`.

That means “install” also creates example CRs, which is usually not what you want.

**Recommendation:** remove samples from default; keep samples for manual apply.

## 9) README still says Step 0

Not important technically, but it *will* confuse reviewers. Update to reflect Step 1 (API-first CRDs present, no behavior yet).

---

# Net: Are you missing anything from Step 1 plan?

You delivered the main contract and samples. The meaningful “missing” bits are:

* **Spec should be required** (remove `omitempty`) ✅ important
* **Deployment UPDATED printcolumn** ✅ important
* **Condition type constants** ✅ important for avoiding string drift
* Optional but smart: annotations in template metadata, tighten updateStrategy validation, de-null status fields

If you want, I can give you a **Copilot prompt to implement the fixes above as a single “Step1 polish” PR** (very small diff, high impact).
