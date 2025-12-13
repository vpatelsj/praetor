
## 🚨 P0 #1: You have an actual bug: double heartbeat accounting in `/desired`

In `handleDesired()` you call:

```go
g.recordDesiredHeartbeatIfEligible(deviceName)
g.recordDesiredHeartbeatIfEligible(deviceName)
```

That’s duplicated. It’s harmless-ish, but it’s a “how did this ship?” signal and makes lastSeen logic harder to reason about. Remove one.

---

## 🚨 P0 #2: Your staleness loop will spam patches + events forever

This one is serious.

In `markDeviceDisconnected()` you do:

```go
changed := setAgentConnected(&proc.Status, false,
  "AgentDisconnected",
  fmt.Sprintf("last seen %s ago", age.Round(time.Second)))
```

Because `age` increases every tick, the **message changes every tick**, and your `setAgentConnected()` returns `changed == true` if the message differs:

```go
return before == nil || ... || before.Status != after.Status || before.Message != after.Message
```

So once a device goes stale, you will:

* Patch every DeviceProcess for that device **every staleness tick**
* Emit a Warning event **every staleness tick**
* Continue until the agent returns

That is exactly the kind of “silent apiserver murder” that shows up as latency spikes and etcd pain later.

### Fix (pick one, but do it now)

**Best MVP fix:** make the “disconnected” message stable so it only patches once per transition:

* message: `"device stale"` or `"no report within stale window"`
* if you want the last age, record it somewhere else (metrics/logs) not in the Condition message

Example:

```go
changed := setAgentConnected(&proc.Status, false,
  "AgentDisconnected",
  "device stale (no recent reports)")
```

**Alternative:** only patch if status transitions `True -> False`, ignore message drift:

* change `setAgentConnected()` “changed” calculation to not include Message, or
* only update Message when status changes

But the simplest safe thing is: **don’t put age in the condition message** (or at least don’t update it every tick).

---

## 🔥 High-impact scaling concern: you still do a per-report List() even when nothing changes

Even though the list is indexed (good), you still call `markDeviceConnected()` on **every** `/report`, which does:

* `List(DeviceProcess where deviceName=X)`
* loop them to see if they were disconnected

This creates load proportional to `reports/sec * avg_processes_per_device`, and it’s on the gateway hot path.

### Better pattern (still Step 3 compatible)

Call `markDeviceConnected()` only on a transition:

* if this device was considered stale/disconnected OR never seen before
* otherwise: just update `lastSeen` in memory and return

You already have the state to do this:

* lastSeen + heartbeat hints + staleMultiplier
* just compute “is currently stale?” before calling `markDeviceConnected()`

This will cut your gateway CPU a lot when you scale up.

---

## Security/identity concern: token is not device-bound

`authorize(r, _ string)` checks only a **shared token**. That means any agent with the token can:

* POST `/v1/devices/tor9-99/report` and flip that device’s processes to `AgentConnected=True` (because `markDeviceConnected()` doesn’t verify who “owns” the deviceName)

For docker-compose MVP this might be acceptable, but if you’re closing Step 3 “for real”, this is still a correctness hole.

### Minimal improvement (still simple)

Make the token derived from deviceName:

* token = `HMAC(secret, deviceName)` (agent computes it)
* gateway recomputes and compares

Or simplest dev config:

* env var / file with `deviceName -> token` mapping

---

## “Step 3 DoD” concern: timing can be > “within seconds”

Right now the agent does:

1. GET desired
2. POST report
3. `time.Sleep(a.heartbeat)` (default 15s)

So if someone applies a new DeviceProcess right after the agent goes to sleep, you can miss the “within seconds” expectation.

### Quick MVP fix

Use separate timers:

* poll desired every **~2–5s** (or long-poll)
* report heartbeat every **15s**
* report immediately when desired changes

This makes the UX match your DoD without hammering K8s (gateway caching/ETag handles it).

---

## Smaller issues / polish (worth fixing but not blockers)

* Agent uses `time.Sleep()` (not ctx-aware) → on shutdown it won’t exit until sleep ends. Use `select { case <-time.After(...); case <-ctx.Done(): }`.
* Add jitter to backoff to avoid thundering herd.
* In gateway `respondErr()`, you log via `log.FromContext(ctx)` but you don’t attach a logger to the request context; use `g.log` consistently for meaningful structured logs.

---
