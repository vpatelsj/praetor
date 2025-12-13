
## Biggest correctness hole: `DoesNotExist` / “absence-based” selectors won’t work

Your mapping function only considers deployments by iterating the **keys present in the switch’s labels**:

```go
for key := range labelsMap {
  List deployments indexed by selectorKeysIndex=key
  if selector.Matches(labelSet) enqueue
}
```

This breaks any selector that depends on a key being **absent**, e.g.:

* `matchExpressions: [{ key: "foo", operator: DoesNotExist }]`

### Why it breaks (real example)

If a deployment selects `foo DoesNotExist`, and a switch has no `foo` label:

* The switch’s labels do **not** include `foo`
* Your loop never considers `foo`
* So that deployment never gets enqueued on switch create/update
* Result: **new switches won’t get a DeviceProcess**, and label removals won’t cause add/remove correctly.

Same issue for cases where *whether the key exists* matters (Exists/DoesNotExist) if the key is missing and you rely on iterating keys.

### What to do about it 

**Disallow DoesNotExist/NotIn for now**
If you want event-driven correctness without periodic scanning, restrict selectors to:

* `matchLabels`
* `matchExpressions` with `In`, `Exists` (maybe)

You can enforce with CRD XValidation (or controller validation) and clearly document it in API comments.

---

## Second correctness concern: “CRD not installed” can cause mass deletions

In `listNetworkSwitches()` you do:

```go
if metameta.IsNoMatchError(err) {
  return nil, nil
}
```

Then reconcile continues with `devices=nil` and `desiredNames` empty, then `cleanupStale()` runs and will delete everything owned by the deployment.

If the CRD is briefly not discoverable (startup ordering, APIServer hiccup, etc.) you could accidentally delete all DeviceProcesses for that deployment.

**Fix:** Treat “NoMatch” as “skip reconcile entirely” (no cleanup):

* Return a special error or a boolean “kindUnavailable”
* Or return `ctrl.Result{RequeueAfter: ...}, nil` immediately before cleanup

---

## Scale/ops concerns (not blockers, but worth tightening)

### 1) Potentially many List calls per switch event

You list deployments **once per label key** on the switch.

If a switch has 20–40 labels, that’s 20–40 API cache list operations per event. With lots of switches flapping labels, this can add up.

**Simple improvement:** In Update, compute the set of **changed keys** (symmetric difference between old/new labels) and only query using those keys. Most updates only change 1–2 keys.

### 2) Duplicate enqueue on update (old + new)

In `UpdateFunc`, you call `requestsForNetworkSwitch` twice and `q.Add` everything from both results. There’s no cross-call dedupe, so duplicates can hit the queue.

Not fatal, but easy to fix by unioning the requests before `q.Add`.

### 3) Predicate includes annotations

Watching annotations changes is usually noise (kubectl apply updates last-applied annotations etc.). Labels are what matters for selection.

I’d drop `annotationsChanged` unless you have a specific reason.

