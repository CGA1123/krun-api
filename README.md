# krun-api

An API server for managing lightweight Linux microVMs on macOS using
[libkrun](https://github.com/containers/libkrun). Each VM runs in an isolated
child process, so a guest shutdown never tears down the host.

## Prerequisites

- **Go 1.24+**
- **libkrun** — install from the [slp/krun](https://github.com/slp/homebrew-krun) tap:
  ```bash
  brew tap slp/krun
  brew install libkrunfw libkrun
  ```
- **Docker or Podman** — used by `krun-run` to build images and extract rootfs layers

## Building

```bash
make
```

This builds `krun-api`, `krun-vmm` (codesigned for Hypervisor.framework),
`krun-run`, and `vminit` (cross-compiled for `linux/arm64`).

## Quick start

Start the API server:

```bash
DYLD_LIBRARY_PATH="$(brew --prefix)/lib" ./krun-api \
  --libkrun-path "$(brew --prefix)/lib/libkrun.dylib" \
  --vmm-path ./krun-vmm \
  --listen :8080
```

`DYLD_LIBRARY_PATH` is needed so that `libkrun.dylib` can find `libkrunfw.dylib`
at runtime.

Launch a VM from a Dockerfile (in another terminal):

```bash
./krun-run \
  --api http://localhost:8080 \
  --init ./vminit \
  -n my-vm \
  path/to/build-context/
```

See `./krun-api --help` and `./krun-run --help` for all available flags.

## API

All endpoints are under `/v1/machines`.

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/machines` | Create a new machine |
| `GET` | `/v1/machines` | List all machines |
| `GET` | `/v1/machines/{id}` | Get a machine by ID |
| `DELETE` | `/v1/machines/{id}` | Delete a stopped machine |
| `POST` | `/v1/machines/{id}/start` | Start a machine |
| `POST` | `/v1/machines/{id}/stop` | Stop a running machine |
| `GET` | `/v1/machines/{id}/logs` | Stream console output |

## Architecture

```
krun-api (parent process)             krun-vmm (child process, one per VM)
─────────────────────────             ──────────────────────────────────
REST API (chi router)                 Reads JSON config from stdin
Manager: create/start/stop/delete     Loads libkrun via purego
Virtual networking (gvisor-tap-vsock) Configures VM (vCPUs, memory, rootfs, ...)
Console log management                Calls StartEnter() — blocks until VM exits

Shutdown flow:
  manager.Stop()
    → closes virtual network
    → connects to vsock shutdown socket
    → vminit (PID 1): sends SIGTERM to child, sync() + reboot(POWER_OFF)
    → libkrun _exit() kills only the child process
    → parent detects child exit, cleans up
```

Each VM gets its own userspace network stack (gvisor-tap-vsock) connected via a
unixgram socket — no TUN/TAP devices or root privileges required.
