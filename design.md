## Overview

The Apollo platform manages a diverse set of physical infrastructure including servers, network switches, DPUs (Data Processing Units), SoCs (Systems on Chip), and BMCs (Baseboard Management Controllers). While Kubernetes provides excellent orchestration for containerized workloads on servers through pods and the kubelet, these lower-powered devices present unique challenges that make the standard pod model unsuitable.

**Problem Statement:**
Network switches, DPUs, SoCs, and similar devices are resource-constrained ARM-based systems where every MB of memory, percentage of CPU, and KB of disk space must be carefully managed. These devices either cannot run the full Kubernetes node stack (kubelet, container runtime interface, CNI plugins) or the complexity suppporting them is require significant customizations to standard kubernetes components due to:
- Limited CPU and memory resources (often < 1GB RAM, low-power ARM cores)
- Restricted storage capacity
- Need for tight control over running processes
- Requirements to run processes as systemd units, init.d services, or lightweight containers
- Inability to support pod networking abstractions

Currently, the PilotFish system handles process deployment on both hosts and devices, but it lacks Kubernetes-native integration, making it difficult to leverage existing Kubernetes tooling, RBAC, and operational patterns.

**Solution Goal:**

Design a Kubernetes-native system for deploying and managing processes on resource-constrained devices through:
1. Custom Resources (DeviceProcess, DeviceProcessDeployment) that mirror Kubernetes native concepts
2. A lightweight device agent that watches for scheduled work and manages local process lifecycle
3. A controller that orchestrates deployments, rollouts, and status aggregation
4. Support for systemd units, init.d services, and lightweight containers as execution backends

## Solution

**In Scope:**
- DeviceProcess CRD for declaring a single process instance on a specific device
- DeviceProcessDeployment CRD for managing rollouts across multiple devices
- Lightweight device agent that manages process lifecycle (start, stop, restart, status)
- Controller that reconciles DeviceProcessDeployment into DeviceProcess instances
- Support for systemd, init.d, and container execution backends
- Artifact fetching from OCI registries and HTTP(S) endpoints
- Process health monitoring and status reporting
- Rolling updates with configurable strategies (RollingUpdate, Recreate)
- Integration with existing device CRDs (Server, NetworkSwitch, SOC, BMC, DeviceConnection)
- Replacement of PilotFish for device workload management

**Out of Scope:**
- Full pod compatibility or CRI/CNI support
- Running processes on traditional Kubernetes nodes (use regular pods)
- Dynamic device discovery (relies on existing device inventory CRDs)
- Multi-process orchestration within a single DeviceProcess (use multiple DeviceProcess resources)
- Service mesh integration for device processes

**Design Principles:**
- **Kubernetes-native:** Use CRDs and controllers to leverage existing Kubernetes patterns
- **Lightweight:** Minimize agent resource footprint on constrained devices
- **Declarative:** Desired state declared via CRs, reconciled by controllers and agents
- **Observable:** Rich status, conditions, and events for operational visibility
- **Progressive:** Support gradual rollout with safety checks and rollback capabilities

### Architecture

#### Components

**1. DeviceProcess CRD**
Represents a single process instance scheduled to run on a specific device. Analogous to a Pod but designed for device constraints.

```yaml
apiVersion: azure.com/v1alpha1
kind: DeviceProcess
metadata:
  name: switch-agent-tor1-01
  namespace: infra-system
spec:
  deviceRef:
    kind: NetworkSwitch
    name: tor1-01
  artifact:
    type: oci  # or http, file
    url: ghcr.io/apollo/switch-agent:v1.2.3
    checksumSHA256: abc123...
  execution:
    backend: systemd  # or initd, container
    command: ["/usr/bin/switch-agent"]
    args: ["--config", "/etc/switch-agent/config.yaml"]
    env:
      - name: LOG_LEVEL
        value: info
    workingDir: /opt/switch-agent
  restartPolicy: Always  # or OnFailure, Never
  healthCheck:
    exec:
      command: ["/usr/bin/switch-agent", "health"]
    periodSeconds: 30
    timeoutSeconds: 5
    successThreshold: 1
    failureThreshold: 3
status:
  phase: Running  # Pending, Running, Succeeded, Failed, Unknown
  conditions:
    - type: ArtifactDownloaded
      status: "True"
      lastTransitionTime: "2025-11-17T10:00:00Z"
    - type: ProcessStarted
      status: "True"
      lastTransitionTime: "2025-11-17T10:01:00Z"
    - type: Healthy
      status: "True"
      lastTransitionTime: "2025-11-17T10:02:00Z"
  artifactVersion: v1.2.3
  pid: 12345
  startTime: "2025-11-17T10:01:00Z"
  restartCount: 0
  lastTerminationReason: ""
```

**2. DeviceProcessDeployment CRD**
Manages rollout of DeviceProcess instances across multiple devices. Mirrors Kubernetes DaemonSet semantics - automatically creates a DeviceProcess for every device matching the selector.

```yaml
apiVersion: azure.com/v1alpha1
kind: DeviceProcessDeployment
metadata:
  name: switch-agent
  namespace: infra-system
spec:
  selector:
    matchLabels:
      role: tor
      type: switch
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 10%
  template:
    spec:
      artifact:
        type: oci
        url: ghcr.io/apollo/switch-agent:v1.2.3
        checksumSHA256: abc123...
      execution:
        backend: systemd
        command: ["/usr/bin/switch-agent"]
        args: ["--config", "/etc/switch-agent/config.yaml"]
      restartPolicy: Always
      healthCheck:
        exec:
          command: ["/usr/bin/switch-agent", "health"]
        periodSeconds: 30
status:
  observedGeneration: 2
  desiredNumberScheduled: 100
  currentNumberScheduled: 100
  numberReady: 98
  numberAvailable: 98
  numberUnavailable: 2
  updatedNumberScheduled: 100
  conditions:
    - type: Available
      status: "True"
      reason: MinimumDevicesAvailable
    - type: Progressing
      status: "True"
      reason: RolloutInProgress
```

#### Kubectl Output Examples

**Viewing DeviceProcessDeployment Status:**
```bash
$ kubectl get deviceprocessdeployment -n infra-system
NAME            DESIRED   CURRENT   READY   AVAILABLE   UNAVAILABLE   AGE
switch-agent    100       100       98      98          2             5d
dpu-firmware    50        50        50      50          0             10d
soc-telemetry   200       200       197     197         3             2d
```

**Detailed DeviceProcessDeployment View:**
```bash
$ kubectl describe deviceprocessdeployment switch-agent -n infra-system
Name:         switch-agent
Namespace:    infra-system
Labels:       app=switch-agent
Annotations:  <none>
API Version:  azure.com/v1alpha1
Kind:         DeviceProcessDeployment
Spec:
  Selector:
    Match Labels:
      Role:  tor
      Type:  switch
  Update Strategy:
    Rolling Update:
      Max Unavailable:  10%
    Type:               RollingUpdate
  Template:
    Spec:
      Artifact:
        Checksum SHA256:  abc123def456...
        Type:              oci
        URL:               ghcr.io/apollo/switch-agent:v1.2.3
      Execution:
        Args:
          --config
          /etc/switch-agent/config.yaml
        Backend:       systemd
        Command:
          /usr/bin/switch-agent
      Health Check:
        Exec:
          Command:
            /usr/bin/switch-agent
            health
        Period Seconds:  30
      Restart Policy:    Always
Status:
  Conditions:
    Last Transition Time:  2025-11-17T10:05:00Z
    Message:               98 of 100 devices are available
    Reason:                MinimumDevicesAvailable
    Status:                True
    Type:                  Available
    Last Transition Time:  2025-11-17T10:03:00Z
    Message:               Rollout in progress
    Reason:                RolloutInProgress
    Status:                True
    Type:                  Progressing
  Current Number Scheduled:  100
  Desired Number Scheduled:  100
  Number Available:          98
  Number Ready:              98
  Number Unavailable:        2
  Observed Generation:       2
  Updated Number Scheduled:  100
Events:
  Type    Reason            Age   From                            Message
  ----    ------            ----  ----                            -------
  Normal  RolloutStarted    5m    deviceprocess-controller        Starting rollout of version v1.2.3
  Normal  RolloutProgress   3m    deviceprocess-controller        Updated 50/100 devices
  Normal  RolloutProgress   1m    deviceprocess-controller        Updated 100/100 devices
  Warning DeviceUnhealthy   30s   deviceprocess-controller        Device tor1-42: health check failing
```

**Viewing DeviceProcess Resources:**
```bash
$ kubectl get deviceprocess -n infra-system
NAME                          DEVICE         PHASE     VERSION   RESTARTS   AGE
switch-agent-tor1-01          tor1-01        Running   v1.2.3    0          5d
switch-agent-tor1-02          tor1-02        Running   v1.2.3    0          5d
switch-agent-tor1-03          tor1-03        Running   v1.2.3    1          5d
switch-agent-tor2-01          tor2-01        Running   v1.2.3    0          5d
dpu-firmware-dpu-rack1-01     dpu-rack1-01   Running   v2.1.0    0          10d
soc-telemetry-soc-node-15     soc-node-15    Running   v0.9.2    0          2d
```

**Viewing DeviceProcess by Device Type:**
```bash
$ kubectl get deviceprocess -n infra-system -l type=switch
NAME                          DEVICE         PHASE     VERSION   RESTARTS   AGE
switch-agent-tor1-01          tor1-01        Running   v1.2.3    0          5d
switch-agent-tor1-02          tor1-02        Running   v1.2.3    0          5d
switch-agent-tor1-03          tor1-03        Running   v1.2.3    1          5d
switch-agent-tor2-01          tor2-01        Running   v1.2.3    0          5d
switch-agent-tor2-02          tor2-02        Running   v1.2.3    0          5d
```

**Detailed DeviceProcess View:**
```bash
$ kubectl describe deviceprocess switch-agent-tor1-01 -n infra-system
Name:         switch-agent-tor1-01
Namespace:    infra-system
Labels:       app=switch-agent
              role=tor
              type=switch
Annotations:  <none>
API Version:  azure.com/v1alpha1
Kind:         DeviceProcess
Spec:
  Artifact:
    Checksum SHA256:  abc123def456...
    Type:              oci
    URL:               ghcr.io/apollo/switch-agent:v1.2.3
  Device Ref:
    Kind:  NetworkSwitch
    Name:  tor1-01
  Execution:
    Args:
      --config
      /etc/switch-agent/config.yaml
    Backend:  systemd
    Command:
      /usr/bin/switch-agent
    Env:
      Name:          LOG_LEVEL
      Value:         info
    Working Dir:     /opt/switch-agent
  Health Check:
    Exec:
      Command:
        /usr/bin/switch-agent
        health
    Failure Threshold:  3
    Period Seconds:     30
    Success Threshold:  1
    Timeout Seconds:    5
  Restart Policy:       Always
Status:
  Artifact Version:  v1.2.3
  Conditions:
    Last Transition Time:  2025-11-17T10:00:00Z
    Status:                True
    Type:                  ArtifactDownloaded
    Last Transition Time:  2025-11-17T10:01:00Z
    Status:                True
    Type:                  ProcessStarted
    Last Transition Time:  2025-11-17T10:02:00Z
    Status:                True
    Type:                  Healthy
  Phase:                   Running
  PID:                     12345
  Restart Count:           0
  Start Time:              2025-11-17T10:01:00Z
Events:
  Type    Reason              Age   From           Message
  ----    ------              ----  ----           -------
  Normal  ArtifactDownloaded  5d    device-agent   Successfully downloaded artifact from ghcr.io/apollo/switch-agent:v1.2.3
  Normal  ProcessStarted      5d    device-agent   Started switch-agent process (PID: 12345)
  Normal  HealthCheckPassed   5d    device-agent   Health check passed
```

**Checking Version Consistency Across Devices:**
```bash
$ kubectl get deviceprocess -n infra-system -l app=switch-agent -o custom-columns=NAME:.metadata.name,DEVICE:.spec.deviceRef.name,VERSION:.status.artifactVersion,PHASE:.status.phase
NAME                     DEVICE      VERSION   PHASE
switch-agent-tor1-01     tor1-01     v1.2.3    Running
switch-agent-tor1-02     tor1-02     v1.2.3    Running
switch-agent-tor1-03     tor1-03     v1.2.3    Running
switch-agent-tor1-04     tor1-04     v1.2.2    Running
switch-agent-tor2-01     tor2-01     v1.2.3    Running
```

This output shows that `tor1-04` is still running the old version v1.2.2 during a rolling update.

**3. DeviceProcessDeployment Controller**
A Kubernetes controller running in the control plane that:
- Watches DeviceProcessDeployment resources
- Queries device inventory CRDs (NetworkSwitch, SOC, Server, BMC) based on selectors
- Automatically creates/updates/deletes DeviceProcess resources for all matched devices (DaemonSet semantics)
- Implements update strategies (rolling updates with configurable maxUnavailable)
- Aggregates status from DeviceProcess instances into deployment status
- Emits events for significant lifecycle transitions

**4. Device Agent**
A lightweight agent deployed on each device that:
- Authenticates to Kubernetes API server (service account token or device certificate)
- Watches DeviceProcess resources where `spec.deviceRef` matches the local device
- Downloads artifacts from specified sources (OCI registries, HTTP endpoints)
- Verifies artifact checksums for security
- Manages process lifecycle via configured backend (systemd, init.d, container)
- Monitors process health using configured health checks
- Reports status and conditions back to DeviceProcess resource
- Minimal resource footprint (target: <50MB memory, <5% CPU)

**Execution Backends:**
- **systemd:** Generates systemd unit files, uses `systemctl` to start/stop/restart
- **init.d:** Generates init scripts, uses `/etc/init.d/` conventions
- **container:** Uses lightweight container runtime (podman, docker, containerd) for isolation

#### Workflows

**Deployment Workflow:**
1. User creates/updates DeviceProcessDeployment CR
2. Controller watches DeviceProcessDeployment, queries device inventory
3. Controller automatically creates/updates DeviceProcess CRs for all matched devices
4. Device agent watches for DeviceProcess scheduled to it
5. Agent downloads artifact, verifies checksum
6. Agent configures execution backend (systemd unit, init script, container)
7. Agent starts process and begins health monitoring
8. Agent updates DeviceProcess status with conditions and phase
9. Controller aggregates status into DeviceProcessDeployment

**Update/Rollout Workflow:**
1. User updates DeviceProcessDeployment (e.g., new artifact version)
2. Controller calculates rollout plan based on updateStrategy
3. Controller updates DeviceProcess CRs in batches respecting maxUnavailable
4. Device agents detect changes, download new artifacts
5. Agents perform graceful shutdown of old process
6. Agents start new process version
7. Agents report health status
8. Controller proceeds with next batch or pauses on failures
9. Controller updates deployment status showing progress

**Health Monitoring:**
- Agent executes health check command on configured interval
- Success/failure tracked with thresholds
- Process restarted based on restartPolicy (Always, OnFailure, Never)
- Status conditions updated (Healthy, Unhealthy)
- Events emitted for state transitions

**Failure Handling:**
- Artifact download failures: Retry with exponential backoff, report condition
- Process start failures: Retry based on restartPolicy, update conditions
- Health check failures: Restart after threshold, emit events
- Agent crashes: Process continues running, agent resumes monitoring on restart
- Network partitions: Agent operates on last known state, controller marks stale

#### Scaling and Performance

**Expected Scale:**
- 100,000 devices (switches, DPUs, SoCs)
- 100+ DeviceProcessDeployment resources
- 100,000+ DeviceProcess resources

#### Integration Points

**Device Inventory CRDs:**
- Query Server, NetworkSwitch, SOC, BMC, DeviceConnection resources
- Use labels for selector matching
- Validate device connectivity via DeviceConnection status

**Artifact Storage:**
- Leverage the Apollo Distribution Service for high-scale artifact deployment
- HTTP(S) file servers
- Local file paths (for pre-staged artifacts)

**Observability:**
- Prometheus metrics for controller and agent health
- Kubernetes events for lifecycle transitions
- Status conditions following Kubernetes conventions
- Integration with ADX-Mon infrastructure

### Security/Privacy/Compliance

**Authentication & Authorization:**
- **Controller:** Runs with service account with permissions to read device CRDs, manage DeviceProcess/DeviceProcessDeployment
- **Agent:** Authenticates via Kubernetes bootstrap token model:
  - Device boots with time-bound bootstrap token (short-lived, e.g., 1 hour)
  - Agent exchanges bootstrap token for mutual TLS certificate via Kubernetes Certificate Signing Request (CSR) API
  - Long-lived certificate (e.g., 1 year) used for subsequent API server connections
  - Certificate renewal handled automatically before expiration
  - Device identity embedded in certificate (serial number, MAC address)
- **RBAC:** Separate roles for deployment creation vs. viewing status
- **Device Identity:** Agents identify themselves using device serial numbers or MAC addresses embedded in certificates

**Artifact Security:**
- Mandatory checksum verification (SHA256) for all downloaded artifacts
- Support for signed artifacts (future: Sigstore integration)
- Private registry authentication via image pull secrets
- HTTPS required for HTTP artifact sources

**Network Security:**
- Agents connect to Kubernetes API server over TLS
- Certificate validation for all external connections

**Process Isolation:**
- Systemd security features (CapabilityBoundingSet, PrivateTmp, etc.)
- Container isolation when using container backend
- Process runs as non-root user where possible
- Resource limits (memory, CPU) enforced by execution backend

**Compliance:**
- Audit logging for all DeviceProcess lifecycle operations
- Immutable artifact storage with retention policies
- Status history for forensic analysis
- Compliance with internal security baseline requirements

**Secrets Management:**
- Support Kubernetes Secrets referenced in DeviceProcess spec
- Agent fetches secrets via API with appropriate RBAC

### Dependencies and assumptions

**Dependencies:**
- Existing device inventory CRDs populated and maintained
- Device connectivity to Kubernetes API server (direct or via proxy)
- Artifact storage infrastructure (OCI registry, HTTP servers, Apollo Distribution Service)
- systemd, init.d, or container runtime available on devices

**Assumptions:**
- Devices have sufficient resources to run agent (<50MB RAM, <5% CPU)
- Device clocks are synchronized (NTP) for accurate timestamps
- Devices can authenticate to Kubernetes API server
- Artifact sizes are reasonable (<500MB typical, <2GB max)
- Device inventory CRDs are authoritative source of truth
- Devices have stable identifiers (serial number, MAC address)