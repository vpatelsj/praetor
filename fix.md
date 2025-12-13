

## 🚨 P0: Your condition “changed” detection is wrong (breaks stale/disconnect + reconnect-with-empty-obs)

In `gateway/server.go`:


```go
func setAgentConnected(status *...DeviceProcessStatus, connected bool, reason, message string) bool {
    before := conditions.FindCondition(status.Conditions, ConditionAgentConnected)
    conditions.SetCondition(&status.Conditions, metav1.Condition{...})
    after := conditions.FindCondition(status.Conditions, ConditionAgentConnected)

    return before == nil || after == nil || before.Status != after.Status ...
}
```

Problem: `FindCondition` returns a **pointer to the element inside the slice**. Then `SetCondition` overwrites that same element in-place. So **`before` gets mutated** and ends up equal to `after`.

### What breaks in practice

* `markDeviceDisconnected()` relies on `changed := setAgentConnected(..., false, ...)` and then:

  ```go
  if !changed { continue }
  patch...
  ```

  After the first time AgentConnected exists, `changed` will almost always be **false** even when you flip True→False — so **you won’t patch status** and the device never becomes disconnected.

* `markDeviceConnected()` has the same issue for False→True (reconnect) when there are **no observations** (e.g., agent restarts, desired 304, sends heartbeat only). Your `handleReport()` only calls `markDeviceConnected()` when `isStale`, but `markDeviceConnected()` may skip patch due to the same bug → device stays disconnected.

### Fix (simple and correct)

Capture a **copy** of the “before” condition (not a pointer into the slice) or have `SetCondition` return a boolean.

Example fix inside `setAgentConnected`:

```go
func setAgentConnected(status *apiv1alpha1.DeviceProcessStatus, connected bool, reason, message string) bool {
    desired := metav1.ConditionFalse
    if connected { desired = metav1.ConditionTrue }

    var beforeCopy *metav1.Condition
    if c := conditions.FindCondition(status.Conditions, apiv1alpha1.ConditionAgentConnected); c != nil {
        tmp := *c
        beforeCopy = &tmp
    }

    conditions.SetCondition(&status.Conditions, metav1.Condition{
        Type:    string(apiv1alpha1.ConditionAgentConnected),
        Status:  desired,
        Reason:  reason,
        Message: message,
    })

    after := conditions.FindCondition(status.Conditions, apiv1alpha1.ConditionAgentConnected)
    if beforeCopy == nil || after == nil {
        return true
    }
    return beforeCopy.Status != after.Status || beforeCopy.Reason != after.Reason || beforeCopy.Message != after.Message
}
```

Once you fix this, your stale loop + reconnect semantics will actually work.

---

## ✅ What *is* in good shape now

These look good and are “Step 3-grade”:

* Agents **do not** touch Kubernetes; only HTTP to gateway ✅
* Gateway indexes `spec.deviceRef.name` and lists with `MatchingFields` ✅
* `handleReport()` only calls `markDeviceConnected()` on **stale→active transition** (nice scaling win) ✅
* Staleness spam is fixed (stable disconnected message) ✅
* Desired polling doesn’t keep devices alive unless there was a recent report (`recordDesiredHeartbeatIfEligible`) ✅
* HMAC per-device token path exists (when secret configured), shared token fallback for dev ✅
* Agent has separate tickers: desired every 5s, heartbeat every N seconds ✅
* ETag preservation on 304 ✅

---

## Other concerns 

1. **`markDeviceConnected()` calling `recordReport()` again**
   You already call `recordReport()` in `handleReport()`. Double-write is harmless, just noise.

2. **`conditionsEqual()` is order-sensitive**
   If condition ordering ever changes (later code paths), you can get extra patches. Not urgent for Step 3, but you may want to compare by Type map instead of slice index.

3. **Stale loop still lists every stale device every tick**
   Even though it won’t patch repeatedly anymore, it will still `List()` each tick for stale devices. For Step 3 it’s fine; for scale you’ll want a “alreadyDisconnected” memory bit to avoid repeated lists.

---

