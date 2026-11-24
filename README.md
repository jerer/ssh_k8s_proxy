# kubessh

`kubessh` gives you a normal OpenSSH connection into a Kubernetes container
without `sshd` in the image and without shelling out to `kubectl` or `ssh`.

It is inspired by [loft-sh/devpod](https://github.com/loft-sh/devpod): the same
binary is a local SSH `ProxyCommand` and a tiny SSH server that runs inside the
container. The proxy attaches to a `pods/exec` stream, and your ssh client
speaks SSH directly to the in-container agent. No external binaries are
executed by the proxy; the Kubernetes API and SSH protocol are implemented with
Go packages only.

```
ssh client  <--stdio-->  kubessh proxy  <--pods/exec-->  kubessh agent  -->  shell
```

## Features

- Pure Go, `CGO_ENABLED=0` friendly, statically linked binaries.
- Works as an SSH `ProxyCommand`.
- Two transports:
  - `exec` (default): runs `kubessh agent --stdio` inside the container via the
    Kubernetes exec subresource.
  - `portforward`: port-forwards to a long-running `kubessh agent --listen`
    service in the container.
- Optional agent upload (like DevPod's agent injection), with a SHA-256
  content check so unchanged binaries are not overwritten.
- Optional macOS build that embeds a gzip-compressed linux/amd64 agent, so the
  Mac client needs no separate agent file.
- SSH sessions with `exec`, interactive `shell`, `env`, PTY, window resize, and
  signals.
- Public key auth (`authorized_keys`), password auth, or no auth when
  Kubernetes RBAC is the access boundary.
- Persistent host key per pod (`/tmp/kubessh/host_ed25519`) to avoid host key
  changes between connections.
- WebSocket exec with automatic SPDY fallback.

## Build

```sh
make build          # bin/kubessh
make linux-amd64    # bin/kubessh-linux-amd64
make linux-arm64
```

Equivalent direct build:

```sh
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git describe --tags --always)" \
  -o bin/kubessh .
```

`main.version` only controls `kubessh version` output. Upload decisions use
SHA-256 content hashes, not the version string.

### macOS binary with embedded Linux agent

If your pods are always linux/amd64, build macOS binaries that carry the agent
inside them:

```sh
make darwin-embedded
# bin/kubessh-darwin-amd64
# bin/kubessh-darwin-arm64
```

Each macOS binary contains a gzip-compressed linux/amd64 agent. No
`--agent-binary` is needed:

```sshconfig
Host mypod
  HostName mypod
  User root
  ProxyCommand /path/to/kubessh-darwin-arm64 proxy --pod mypod --container app
  StrictHostKeyChecking accept-new
```

In auto mode the proxy compares the remote agent's SHA-256 with the embedded
agent, extracts it to a temporary file only when an upload is needed, and uses
the normal verified upload path. `--agent-binary` still overrides the embedded
agent. Linux/arm64 pods are not supported by this embedded build; supply an
explicit arm64 agent with `--agent-binary` instead.

### Windows client with embedded Linux agent

Windows is supported as a client/proxy; the agent itself runs inside the Linux
container. Build the Windows client with the same embedded linux/amd64 agent:

```sh
make windows-embedded
# bin/kubessh-windows-amd64.exe
# bin/kubessh-windows-arm64.exe
```

Example OpenSSH for Windows config:

```sshconfig
Host mypod
  HostName mypod
  User root
  ProxyCommand C:\tools\kubessh-windows-amd64.exe proxy --pod %h --container app
  StrictHostKeyChecking accept-new
```

The embedded agent is used automatically when no `--agent-binary` is given.
`kubessh agent` is not available on Windows; agent mode is Linux/macOS only.

## CI and releases

- `.gitlab-ci.yml` runs tests and builds macOS x64/arm64 and Windows x64/arm64
  embedded clients plus the Linux x64 client on every pipeline.
- `.github/workflows/release.yml` builds the same clients when a GitHub release
  is published and attaches them plus `SHA256SUMS` to the release.

The Linux client is intentionally built without an embedded agent. By default
it downloads the matching linux/amd64 agent inside the pod from the GitHub
release; `--agent-binary` and `inject` remain available.

## Quick start (ProxyCommand + auto upload)

Build a Linux agent binary for the container architecture, then point your SSH
config at the proxy:

```sshconfig
Host mypod
  HostName mypod
  User root
  ProxyCommand kubessh proxy --pod mypod --container app \
    --agent-binary /path/to/kubessh-linux-amd64
  StrictHostKeyChecking accept-new
  UserKnownHostsFile ~/.ssh/known_hosts
```

```sh
ssh mypod
ssh mypod 'ls -la /'
```

Auto mode works in this order:

1. If a local agent source exists (embedded agent or `--agent-binary`), compare
   its SHA-256 with the remote agent. Otherwise compare the local client
   version with the remote agent version.
2. If they match, connect immediately.
3. If they do not match, try downloading the matching GitHub release asset
   **inside the pod**, with a 60-second default timeout.
4. If the download fails and a local/embedded agent exists, fall back to
   uploading that agent over the Kubernetes API.

Use `--upload-mode=always` to always upload, `--upload-mode=download` to force
in-pod download without fallback, and `--upload-mode=never` to skip checks and
installs entirely.

The pod needs `curl` or `wget` and network access to GitHub for downloads.
Checksums are verified when a local or embedded agent supplies the expected
SHA-256.

The first connection can spend up to the download timeout plus a client-side
upload, which is tens of seconds on slow clusters. If your ssh config sets a
short `ConnectTimeout`, the client may disconnect before the SSH banner is
sent. Remove/raise `ConnectTimeout` for that first connection, run
`kubessh inject` once beforehand, or use `--upload-mode=never` after installing
the agent.

## Install the agent once

```sh
kubessh inject --pod mypod --container app \
  --src ./kubessh-linux-amd64 --dest /tmp/kubessh/kubessh
```

Then use the proxy without `--agent-binary`:

```sshconfig
Host mypod
  HostName mypod
  ProxyCommand kubessh proxy --pod mypod --container app --upload-mode never
```

The upload path requires a POSIX shell plus `cat`, `chmod`, `mv`, `rm`, and
`dirname` in the container. Uploaded data is written to a temporary path,
verified with SHA-256, and only then renamed over the destination, so an
interrupted transfer does not break an existing agent. Verification uses
`sha256sum` when available, otherwise the uploaded binary's `hash` subcommand.
An interrupted transfer may leave a partial `<dest>.upload-*` file next to the
destination; it is never used and can be removed safely.

For distroless/scratch containers, bake the agent into the image and use
service mode instead.

## In-pod agent download

In auto mode, any client tries to download the agent **inside the pod** when
the installed agent does not match (SHA-256 when a local/embedded agent exists,
otherwise version). If the download fails and a local/embedded agent exists,
the proxy falls back to uploading it. The Linux client is built without an
embedded agent, so download is its normal install path.

```sshconfig
Host mypod
  HostName mypod
  User root
  ProxyCommand kubessh proxy --pod %h --container app
```

Defaults:

- Asset: `kubessh-linux-amd64`
- Download URL: `https://github.com/jerer/ssh_k8s_proxy/releases/download/<version>/...`
  for tagged clients, or `https://github.com/jerer/ssh_k8s_proxy/releases/latest/download/...`
  for `dev` builds
- Timeout: 60 seconds
- Downloader: `curl`, falling back to `wget`
- Checksum: verified when the expected SHA-256 is known (embedded agent or
  `--agent-binary`); otherwise GitHub HTTPS is the only integrity check

Override for forks, private mirrors, or air-gapped setups:

```sh
kubessh proxy --pod mypod --container app \
  --upload-mode download \
  --agent-arch amd64 \
  --agent-download-url 'https://github.com/myorg/myrepo/releases/download/{version}/kubessh-linux-{arch}' \
  --download-timeout 60
```

Template placeholders are `{version}`, `{arch}`, and `{os}`. A URL without
placeholders is used literally, which is useful for `file://` or an internal
mirror. If the pod cannot download the agent and there is no local/embedded
agent, use `--agent-binary` or run `kubessh inject` once.

## Service mode (agent listening in the container)

Run the agent as a service inside the container:

```sh
kubessh agent --listen 0.0.0.0:2222 \
  --authorized-keys /etc/kubessh/authorized_keys
```

Listening without credentials is refused unless `--allow-no-auth` is set.

Then proxy through the Kubernetes port-forward subresource:

```sshconfig
Host mypod-service
  HostName mypod
  User root
  ProxyCommand kubessh proxy --pod mypod --via portforward --port 2222 \
    --upload-mode never
```

Port-forward mode does not need a shell or an exec-capable image; it only needs
`pods/portforward` permission and the agent listening in the pod.

## Authentication

The agent supports three modes:

1. **Authorized keys** (recommended):
   - inline: `--authorized-key 'ssh-ed25519 AAAA...'` (repeatable)
   - file: `--authorized-keys /path/to/authorized_keys` (repeatable)
   - env: `KUBESSH_AUTHORIZED_KEYS` (inline keys) or
     `KUBESSH_AUTHORIZED_KEYS_FILES` (comma/colon separated paths)
   - defaults if nothing is configured: `~/.ssh/authorized_keys`,
     `/etc/kubessh/authorized_keys`, `/root/.ssh/authorized_keys`
2. **Password**: `--password` or `KUBESSH_PASSWORD`.
3. **No auth**: if no keys and no password are configured, the agent accepts
   `none` authentication. This is the convenient `ProxyCommand` default,
   because anyone who can open the exec stream already has command execution
   through Kubernetes RBAC. Do not use this for network-exposed agents.

In `--stdio` mode the agent accepts `none` auth by default (the exec stream is
the access boundary). In `--listen` mode it refuses to start without keys or a
password; pass `--allow-no-auth` only if you intentionally want an
unauthenticated network service.

## Host keys

The agent persists an ed25519 host key at `/tmp/kubessh/host_ed25519` by
default. Inside a pod this survives for the pod lifetime, so the key is stable
across connections. Use `--host-key` (agent) or `--agent-host-key` (proxy) to
put it on a persistent volume or another writable path. If the path cannot be
written, an ephemeral key is generated and a warning is logged.

With `StrictHostKeyChecking=accept-new` you get TOFU behavior; if you prefer no
tracking, use `StrictHostKeyChecking=no` and `UserKnownHostsFile=/dev/null`.

## Kubernetes permissions

For `--via exec`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kubessh
  namespace: default
rules:
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get"]
```

For `--via portforward`, replace `pods/exec` with `pods/portforward`.
`pods/exec` is a highly privileged permission: it is effectively root/container
user access. Treat it like SSH access itself.

Kubeconfig loading uses the normal client-go rules: `--kubeconfig`,
`KUBESSH_KUBECONFIG`, `KUBECONFIG`, `~/.kube/config`, or in-cluster credentials.
`--context` and `KUBESSH_CONTEXT` select a context. Namespace defaults to the
kubeconfig namespace, then `default`.

If your kubeconfig uses an exec-based auth provider (for example a cloud CLI),
client-go may run that provider; `kubessh` itself never invokes `kubectl`.

## Commands

```
kubessh agent [--stdio | --listen ADDR] [auth flags]
kubessh proxy --pod POD [--container NAME] [kube flags] [--via exec|portforward]
kubessh inject --pod POD --src FILE [--dest PATH] [kube flags]
kubessh version
kubessh hash
```

`kubessh hash` prints the SHA-256 digest of the running executable; the proxy
uses the same command inside the container for upload decisions.

Useful proxy flags:

| Flag | Meaning |
| --- | --- |
| `--pod`, `--container` | Target pod/container; `KUBESSH_POD`, `KUBESSH_CONTAINER` |
| `--namespace`, `--context`, `--kubeconfig` | Kubernetes access |
| `--remote` | Agent path inside the container (default `/tmp/kubessh/kubessh`) |
| `--agent-binary` | Local binary for client-side upload |
| `--upload-mode` | `auto`, `always`, `download`, `never` |
| `--agent-arch` | Linux agent architecture for downloads (default `amd64`) |
| `--agent-download-url` | Download URL template (`{version}`, `{arch}`, `{os}`) |
| `--download-timeout` | Download timeout in seconds (default `60`) |
| `--via` | `exec` or `portforward` |
| `--port` | Remote port for `portforward` (default `2222`) |
| `--exec-protocol` | `auto`, `websocket`, `spdy` |
| `--agent-host-key` | Host key path passed to the agent |
| `--agent-shell` | Shell passed to the agent |

## How it works

`kubessh proxy` opens a Kubernetes `pods/exec` stream whose command is
`kubessh agent --stdio`. The exec stream is a raw bidirectional pipe. The proxy
connects local stdin/stdout to it. The local OpenSSH process then performs its
normal SSH handshake with `kubessh agent`, which implements just enough of the
SSH server side to run local commands in the container and allocate PTYs with
`github.com/creack/pty`.

The only `os/exec` calls in the project are inside the agent, where they are
required to run the requested shell command and its PTY. The proxy uses the
Kubernetes API clients directly.

## Limitations

- The SSH username is informational; commands run as the container's user. User
  switching is intentionally not implemented.
- SFTP, agent forwarding, and SSH `direct-tcpip` forwarding (`ssh -L`/`-R`)
  are not implemented; use Kubernetes port-forward for other ports.
- Exec mode needs a shell and coreutils inside the container.
- Windows is supported only as a client/proxy; agent mode (PTY and Unix
  signal handling) is Linux/macOS only. Point it at a linux/amd64 pod.
