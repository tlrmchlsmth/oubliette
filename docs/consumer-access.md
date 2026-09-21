# Consumer virtual Kubernetes access

Oubliette exposes lifecycle through MCP and workload operations through the
private virtual Kubernetes API. `oub-connect` is a trusted consumer connector
for the external placement selected by ADR-0007 and ADR-0014. It is a foreground
process on the consumer side of the sandbox boundary. It is **not an agent tool**
and must not run inside an agent sandbox.

## Integration contract

1. Give each independent agent or explicitly shared trust group a distinct
   Kubernetes ServiceAccount identity for lifecycle authentication. A consumer
   sharing one lifecycle identity across unrelated agents also shares ownership
   of their Oubliettes. Do not select identity from model-supplied input.
2. Configure the existing lifecycle MCP endpoint with the caller's audience-bound
   `oubliette-mcp` token through the consumer's secret configuration. Create an
   Oubliette and poll `oubliette_get` until it is ready.
3. Start `oub-connect` in the trusted consumer environment, with explicit host
   credentials, context, caller-token file, and a fresh output directory. The
   connector reauthenticates the caller and independently verifies ownership,
   readiness, observed generation, resource UID, namespace and expiry.
4. After the credential-free readiness notification, the consumer delivers
   `OUTPUT_DIRECTORY/kubeconfig` into its sandbox through its private credential
   channel. Set the sandbox's `KUBECONFIG` to that virtual-only file.
5. The consumer provides private connectivity to the connector's loopback
   endpoint. A sandbox in another network namespace or on another machine needs
   a consumer-owned authenticated relay. Rewriting the kubeconfig endpoint for
   that relay must retain the virtual CA and TLS server name. Never expose an
   unauthenticated public listener or forward the host API.
6. Propagate atomic credential replacements into the sandbox and remove the
   sandbox copy and relay when the connector exits. Watch the output directory;
   mounting or watching one file inode misses replacements. A consumer using
   the Go package can supply its own `Sink` for direct sandbox injection.

The connector never launches an agent command with its host environment. It
does not print kubeconfigs or tokens, and MCP results remain unchanged. The
agent may use ordinary `kubectl`, Helm and Kubernetes client libraries once the
consumer completes delivery and routing.

## Trusted-side invocation

Build locally:

```sh
go build -o ./bin/oub-connect ./cmd/oub-connect
```

The following is a consumer/operator invocation, **not a command to expose to
an agent**. Both the host config and caller token are pre-provisioned private
files. The token file must be a regular file with no group/other permissions.
The output's parent directory must exist and be writable only by the trusted
consumer; the output directory itself must not exist.

```sh
./bin/oub-connect \
  --host-kubeconfig /consumer/private/host.kubeconfig \
  --host-context approved-host \
  --caller-token-file /consumer/private/agent-mcp.token \
  --output-dir /consumer/private/access-session \
  agent-task
```

This creates a `0700` directory and a `0600` kubeconfig. It forwards an ephemeral
port on `127.0.0.1` to the ready vCluster control-plane pod behind its assigned
Service's port 443, with virtual CA verification and TLS server-name validation.
The forwarded destination cannot be supplied by the agent. The connector
refuses synchronized tenant pods as control-plane targets.

The trusted consumer needs a direct private route to the host API. This initial
port-forward transport requires HTTPS with certificate verification, rejects
an explicit kubeconfig proxy, and does not use environment HTTP proxies.

Host permissions belong only to the trusted consumer: create TokenReviews, get
Oubliettes, get the assigned namespace's bootstrap Secret and Service, list its
Pods, and create its Pod port-forward subresource. Operators should scope the
namespaced permissions to the assigned Oubliette; do not grant these permissions
to the caller identity merely so the agent can use MCP. The trusted consumer is
part of the host security boundary. A compromised consumer with broad host
credentials can bypass this connector's checks.

## Credential and lease behavior

The bootstrap kubeconfig stays in process memory. The connector rejects
executable authentication plugins, external authentication providers,
file-referencing credentials, insecure TLS and proxy configuration from it.
It ignores the bootstrap server address, uses the assigned tunnel instead, and
requests ten-minute tokens for the fixed virtual `oubliette-agent`
ServiceAccount. After the initial issuance it drops bootstrap authentication;
subsequent rotation uses the virtual token. The delivered kubeconfig is constructed from scratch with only
the virtual endpoint, CA, TLS name, one context and the new virtual token.

Tokens rotate at half their returned lifetime. Unreasonably long or nearly
expired responses are rejected. The caller token file is reread during every
authorization check, allowing trusted rotation for the same caller identity.
Authorization and lifecycle state are checked at most 15 seconds apart, with
a ten-second request deadline per check. Failure closes access rather than
continuing with cached authorization. Identity changes, replacement resources,
readiness loss, deletion and shortened leases also close access.

The session owns the shared host port-forward connection. A broken pipe from
one completed client request (for example, `kubectl exec`) closes that request
without tearing down unrelated connections. Actual host transport loss still
ends the session. Ordinary host request timeouts are excluded from the tunnel URL.

A connection ends at the expiry observed when it started, even if MCP later
renews the Oubliette. Reconnect after renewal to adopt a later deadline. An
expired lease cancels the tunnel independently of the next polling interval.
The process removes its output on normal exit, cancellation or failure; cleanup
errors produce a failing exit status. A process supervisor must remove stale
files after an uncatchable termination such as SIGKILL, and must also clean up
any consumer-side credential copies and relays.

Closing this tunnel revokes this connection, not every credential a virtual
cluster-admin might mint. A ten-minute token may outlive a short Oubliette lease.
Host-authoritative workload expiry, virtual API reachability and teardown remain
mandatory. In particular, this connector does not resolve the existing case
where evidence export delays controller finalization. It does not claim that
token rotation alone enforces a virtual cluster-admin's lifetime.

## Verification and deployment gate

Portable Go conformance tests cover caller ownership, readiness and generation
checks, same-name replacement, bootstrap credential exclusion, unsafe bootstrap
configuration, private atomic file delivery, rotation, expiry, cancellation,
transport loss and cancellation during connection setup. The token-issuance
test uses a local TLS Kubernetes API fixture. These are not live-cluster proofs.

Before exposing this integration to agents on a host, run an operator-authorized
acceptance session with its actual vCluster chart, host admission and CNI:

- Create through MCP, attach through the consumer, deploy a permitted workload,
  retrieve logs, execute a command and delete it using only the virtual config.
- Verify TLS names and CA verification through the chosen consumer relay,
  credential rotation and termination of established connections at expiry.
- Prove the virtual token cannot authenticate to the host API; a different
  lifecycle caller cannot attach, renew or delete this Oubliette.
- Attempt forbidden pod privileges, scheduling overrides, quota exhaustion,
  cross-Oubliette traffic and raw-IP/DNS egress bypass. Verify host rejection.
- Delete and expire Oubliettes while connected, including a failed evidence
  export; distinguish connector access closure from host workload shutdown.

Conductor/OpenShell-specific credential delivery and private routing are not
automatically installed by this command. Aggregate allocation limits, bounded
total renewal lifetime, workload shutdown independent of evidence finalizers,
and runtime isolation for hostile workloads remain separate host-side work.
Storage and model/image access retain the repository's proposed-ADR status.
