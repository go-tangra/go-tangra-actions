# Testing environment plan — provisioning fresh virtual servers

`go-tangra-actions` is meant to run *post virtual-server install*: install packages,
edit config, manage `systemd` services, run scripted actions. The unit tests
already cover the engine against an in-memory host (`system.Fake`, ~85% coverage).
What they cannot prove is **fidelity** — that a real `apt`/`dnf`/`systemctl`, on a
real init system, across real distros, behaves the way the actions assume, and
that workflows are **idempotent** on a second run.

This document plans an integration/e2e environment for that, tuned to this WSL2
host.

## Why not plain Docker

A vanilla container has no real init, so `service` actions (`systemctl
start/restart/enable`) can't be exercised faithfully, package managers behave
oddly (no running services to restart, no `/run` semantics), and reboots/kernel
modules are impossible. We need **systemd + a real OS userspace**. That points at
system containers (LXD/Incus) or full VMs (KVM), not application containers.

## This host's capabilities (probed)

| Capability | Status | Enables |
|---|---|---|
| systemd as PID1 (`systemd=true`) | ✅ | systemd-based containers; `service` tests |
| `/dev/kvm` + `vmx/svm` (nested virt) | ✅ | full KVM/QEMU VMs |
| cgroup v2 | ✅ | LXD/Incus delegation |
| Incus 7.1 installed + initialized | ✅ (2026-06-11) | Tier 1 live — see "First validation run" |

Both tiers below are available on this machine; Tier 1 (Incus) is set up and
validated.

## Options compared

| Option | Kernel | Boot | Config injection | Snapshots | Multi-distro ease | Best for |
|---|---|---|---|---|---|---|
| **Incus/LXD container** | shared host | ~1s | image + `incus exec` | instant | excellent (image server) | inner loop, CI matrix |
| **Incus `--vm`** | own (real) | ~10–20s | cloud-init | instant | excellent (same CLI/images) | fidelity without hand-rolling QEMU |
| **Firecracker microVM** | own (real) | ~125ms | MMDS / baked rootfs | fast (VM-state snapshot) | moderate (build rootfs+kernel) | real-kernel at near-container speed, big parallel matrices, prod-parity if prod uses FC |
| **QEMU/libvirt + cloud image** | own (real) | ~20–40s | cloud-init | qcow2 overlay | easy (official cloud images) | most faithful full-server boot, release validation |

**Recommendation:** lead with **Incus** as the *unifying* tool — system
containers for the fast matrix, and the **same CLI in `--vm` mode** when you need
a real kernel (reboots, kernel modules, kernel-package upgrades) with cloud-init
and instant snapshots, no hand-rolled QEMU. Reach for **Firecracker** when you
specifically want real-kernel isolation at container-like speed/density (e.g. a
large matrix in parallel, or because production runs Firecracker) and are willing
to maintain a rootfs/kernel pipeline. Hand-rolled QEMU/libvirt is the fallback if
you'd rather not run Incus.

## Tiers

### Tier 1 — Incus (or LXD) system containers — the fast inner loop + CI matrix

System containers boot a full systemd userspace with real package managers, in
~1 second, with near-instant snapshot/restore. Ideal for the **per-commit test
matrix** and rapid iteration.

- Distro matrix (exercises every package backend):
  - `ubuntu/24.04`, `debian/12` → `apt`
  - `rockylinux/9` (or `almalinux/9`) → `dnf`
  - `alpine/3.20` → `apk`
  - `archlinux` → `pacman`
- Snapshots: `incus snapshot create <c> clean` once, `incus snapshot restore <c> clean` between tests (sub-second clean slate).
- Limitation: shared host kernel — can't test kernel modules, reboots, or
  kernel-package upgrades. That's Tier 2's job.

### Tier 2 — real-kernel VMs — fidelity/release

A real VM booting an official **cloud image** is the truest analogue of "a freshly
installed virtual server." Use for release validation and anything containers
can't model (kernel updates, reboots, full boot path).

Two ways to get there here, in increasing setup cost:

- **Incus `--vm` (preferred):** `incus launch images:ubuntu/24.04 t --vm` gives a
  QEMU-backed VM with a real kernel, cloud-init, and the *same* CLI, image server
  and instant snapshots as the containers above. Reuses the whole Tier-1 harness
  unchanged — just add `--vm`.
- **Hand-rolled QEMU/libvirt:** if not using Incus. `cloud-init` seeds the VM;
  qcow2 backing-file overlays or `virsh snapshot` give fast resets. Heavier.

### Tier 3 (optional) — Firecracker microVMs — real kernel at container speed

[Firecracker](https://firecracker-microvm.github.io/) boots a stripped-down KVM
microVM (real kernel, virtio-only, no BIOS/ACPI) in ~125 ms with tiny memory
overhead. It gives **VM isolation and a real kernel at near-container speed**,
which is attractive for a large matrix run in parallel, for CI on nested-virt
hosts, or for **parity if production provisioning targets are Firecracker**.

The cost is a lower-level pipeline (no cloud-init, no image server):

- **Kernel**: supply an uncompressed `vmlinux` (the Firecracker quickstart kernel,
  or build one with virtio + the configs systemd needs).
- **Rootfs**: build a per-distro `ext4` image with **systemd** inside — easiest by
  exporting a systemd-enabled OCI image to ext4
  (`podman create` → `podman export` → `mkfs.ext4`/`mcopy` into an image), or via
  `firecracker-containerd` / `flintlock` which run OCI images as microVMs directly.
- **Config injection**: no cloud-init — either *bake* the `tangra-actions` binary,
  workflow and actions into the rootfs before boot, or pass them via the
  **MMDS** metadata service and a tiny in-guest fetch on boot.
- **Networking**: a host TAP device per microVM (`ip tuntap`), NAT for outbound
  `apt`/`dnf`.
- **Reset**: Firecracker's **snapshot/restore** (pause → snapshot → resume) gives
  an extremely fast clean slate, or just rebuild from the immutable base rootfs.

Tradeoff vs Incus `--vm`: Firecracker is faster/lighter and a closer match to a
Firecracker-based production fleet, but you maintain the kernel + rootfs build and
TAP networking yourself, whereas Incus hands you images, cloud-init, networking
and snapshots out of the box. **Start with Incus; adopt Firecracker only if its
speed/density or prod-parity earns the extra pipeline.**

## Anatomy of one integration test

```
build → provision → deliver → execute → assert → assert-idempotent → reset
```

1. **Build** a static agent binary once:
   `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/tangra-actions ./cmd/tangra-actions`
   (the module has no cgo, so this is a portable static binary). Bundle the
   example workflow + action packages (`examples/`).
2. **Provision** a clean target from the `clean` snapshot.
3. **Deliver**: `incus file push dist/tangra-actions <c>/usr/local/bin/` (+ workflow + `examples/actions`); for KVM, `scp` over the cloud-init SSH key.
4. **Execute** on the target, as root:
   `tangra-actions -json -actions /opt/actions /opt/provision.yaml`
5. **Assert** two independent ways:
   - **Engine view** — parse the `-json` `RunResult`: every job `success`, the
     expected steps report `changed: true`, no `err`.
   - **External probes** — confirm the host actually changed:
     - package: `dpkg -s nginx` / `rpm -q nginx` / `apk info -e nginx`
     - service: `systemctl is-active nginx`, `systemctl is-enabled nginx`
     - file: content hash + `stat -c '%a'` mode
6. **Assert idempotent** — run the workflow **again**; every step that changed
   the host the first time must now report `changed: false` (and still
   `success`). This is the single most valuable provisioning test.
7. **Reset** — restore the snapshot / destroy the VM.

Also run **negative/security** cases in the same harness: a malicious package
name is rejected before exec, a `file` action with `-confine` blocks `..`
traversal, a registered secret is masked in `-json` output.

## First validation run (2026-06-11, Incus Tier 1)

Incus 7.1 stood up on this WSL2 host (`incusbr0` 10.48.250.1/24 with **IPv6
disabled** to dodge the dnsmasq listen-socket failure; `dir` storage pool), an
`images:ubuntu/24.04` system container launched with real systemd
(`systemctl is-system-running` → `running`). The static binary + `examples/`
were pushed in and `provision-web.yaml` run twice. Results:

- **Pass — fidelity:** real `apt` installed nginx `1.24.0-2ubuntu7.11`; external
  probes agreed (`dpkg -s` installed, `systemctl is-enabled/is-active` →
  enabled/active, vhost rendered `server_name test.local`). The fake host never
  exercised any of this.
- **Pass — idempotency for `file`/`service`:** on the second run "Write virtual
  host" reported `changed: false` and "Reload nginx" **skipped** (∅) because its
  `if: steps.vhost.outputs.changed == 'true'` guard went false. The
  change-gated-reload chain firing correctly against real systemd is the headline
  result.
- **Finding F1 — apt cache — FIXED.** First attempt failed with `apt-get exited
  with code 100`: the `package` action issued `apt-get install` with **no
  `apt-get update`** against empty package lists. Fixed by adding an
  `update_cache` input to the `package` action (`apt-get update` / `dnf|yum
  makecache` / `apk update` / `pacman -Sy`); `provision-web.yaml` now sets it.
  Re-validated on a freshly restored `clean` snapshot: install succeeds with
  `cache_updated: true`, no manual refresh. (Also fixed a doc bug here — the
  reset command is `incus snapshot restore`, not `incus restore`, in Incus 7.x.)
- **Finding F3 — hostnamectl needs D-Bus — FIXED.** The `hostname` action's
  first cut chose `hostnamectl` on PATH presence alone; on the **minimal**
  `images:ubuntu/24.04` container it failed with *"Failed to connect to bus"*
  (these LXC images ship no D-Bus/`systemd-hostnamed`; cloud images do). Fixed by
  falling back to the portable `/etc/hostname` + `hostname <binary>` method on any
  hostnamectl failure. Re-validated: hostname set to `web-01` (live + persisted),
  idempotent on re-run. Also exercised the `service` action's new `ignore_missing`
  + list support via the `disable-auto-updates` composite (apt timers disabled;
  `unattended-upgrades`/`zabbix-agent` correctly reported `missing` and skipped).
- **Finding F4 — symlink-write corruption — FIXED (high value).** The first cut
  of the `timezone` action's portable fallback wrote the zone bytes straight to
  `/etc/localtime`. That path is normally a **symlink** into the zoneinfo tree, so
  `os.WriteFile` followed it and overwrote the canonical zone file — setting
  `Europe/Sofia` silently rewrote `/usr/share/zoneinfo/Etc/UTC` (114→2077 bytes),
  corrupting UTC for every process. No unit test caught it (the `Fake` has no
  symlinks); only the real host did. Fixed by `Remove`-ing `/etc/localtime`
  before writing, so a fresh regular file is created and the zoneinfo target is
  left intact. Re-validated: `TZ=Etc/UTC date` → `UTC +0000` (intact) while the
  system zone is `Europe/Sofia`. Lesson for the harness: **the `system.Fake` can't
  model symlink-follow, FS permissions, or init quirks — these need a real target,
  which is exactly Tier 1's job.**
- **Finding F2 — package idempotency — OPEN.** Even on the second run "Install
  nginx" reports success with `Changed=true` (`action/pkg.go` sets it
  unconditionally). The action can't tell "installed" from "already present," so
  package idempotency is currently only assertable by external probe. Next:
  parse manager output to set `Changed` accurately.

## What to add to the codebase to support this

1. **`-json` output on the CLI** (small, high value). Today the CLI prints a human
   tree; tests need machine-readable results. Emit the `RunResult` (jobs → steps
   → outcome/conclusion/changed/outputs/err) as JSON so assertions are precise.
   *Note:* `StepReport` has no `Changed` field yet — add it (the action `Result`
   already carries `Changed`; thread it through `runStep`/`runComposite`) so
   idempotency can be asserted from JSON.
2. **A realistic `examples/provision.yaml`** — a representative post-install
   workflow (create a user, install packages, write+secure an sshd drop-in,
   enable+start a service, a JS action for a templated step) that the matrix runs.
   It **must start with a package-index refresh** (`apt-get update` /
   `dnf makecache` / `apk update` / `pacman -Sy`) — see finding F1 below: a
   freshly installed server has empty/stale package lists, so an install without
   a refresh fails hard.
3. **Package action: cache refresh + idempotent reporting** (findings F1, F2).
   - *F1 — refresh — DONE.* Added an `update_cache` input to the `package`
     action; it runs the manager's index refresh before the install/upgrade and
     publishes `cache_updated`. `provision-web.yaml` uses it.
   - *F2 — idempotency — TODO:* `Package.Run` unconditionally sets `Changed=true`.
     It cannot tell "installed it" from "already present," so package-level
     idempotency is unprovable from the engine view. Either parse manager output
     (apt prints `0 newly installed`; rpm/apk/pacman have equivalents) to set
     `Changed` accurately, or accept that package idempotency is asserted only by
     external probe (`dpkg -s` before/after).
4. **A `test/` tree**:
   - `test/testenv/` — a small Go helper exposing a backend-agnostic `Target`
     interface (`Push`, `Exec`, `Snapshot/Restore`, `Destroy`) with an `incus`
     implementation first, and room for `firecracker`/`qemu` behind the same
     interface so the test bodies never change when the backend does.
   - `test/integration_test.go` behind `//go:build integration` so it never runs
     in the normal `go test ./...`; invoked as `go test -tags=integration ./test/...`.
   - `test/scripts/` — `setup-incus.sh`, `setup-kvm.sh`, `run-matrix.sh` for the
     shell-only path / CI.
5. **Makefile targets**: `test-incus` (matrix), `test-kvm` (fidelity),
   `test-integration` (the Go-tagged suite).

## Setup commands

### Incus (Tier 1) — on this Ubuntu 22.04 (jammy) WSL2 host

> **Status:** already done on this host (Incus 7.1, initialized 2026-06-11). The
> daemon is initialized from [`test/scripts/incus-preseed.yaml`](../test/scripts/incus-preseed.yaml)
> (`ipv6.address: none` + `dir` driver). Your user needs the `incus-admin` group
> (`sudo usermod -aG incus-admin $USER`, then restart WSL so it takes effect).
> The steps below are kept for reproducing the install from scratch.

`incus` is **not** in jammy's repos (it ships in Ubuntu from 24.04 / Debian 13).
On 22.04 install it from the official Zabbly repo (or use snap LXD — see below):

```bash
# Incus from the Zabbly stable repo (apt, no snap)
sudo mkdir -p /etc/apt/keyrings
sudo curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
sudo sh -c 'cat > /etc/apt/sources.list.d/zabbly-incus-stable.sources <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: jammy
Components: main
Architectures: amd64
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF'
sudo apt-get update && sudo apt-get install -y incus

sudo incus admin init --minimal          # default bridge + storage
sudo usermod -aG incus-admin "$USER"      # then re-login / newgrp incus-admin

# Fallback: LXD via snap (snapd is active here). Uses the `lxc` CLI instead of
# `incus`; commands are otherwise identical.
#   sudo snap install lxd && sudo lxd init --minimal && sudo usermod -aG lxd "$USER"

# Per-distro base + clean snapshot (once):
incus launch images:ubuntu/24.04 base-ubuntu
incus exec base-ubuntu -- cloud-init status --wait   # if image uses cloud-init
incus snapshot create base-ubuntu clean

# A test cycle:
incus snapshot restore base-ubuntu clean
incus file push dist/tangra-actions base-ubuntu/usr/local/bin/tangra-actions
incus file push -r examples base-ubuntu/opt/
incus exec base-ubuntu -- tangra-actions -json -actions /opt/examples/actions /opt/examples/provision.yaml
```

If nested containers misbehave, set `security.nesting=true` on the container; with
systemd PID1 + cgroup2 here it generally "just works."

### KVM (Tier 2) — full VM with a cloud image

```bash
sudo apt-get install -y qemu-system-x86 libvirt-daemon-system virtinst cloud-image-utils
sudo adduser "$USER" kvm; sudo adduser "$USER" libvirt

# Seed cloud-init (ssh key + hostname), import an Ubuntu cloud image:
wget https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img -O base.img
cloud-localds seed.img user-data.yaml          # user-data injects your SSH key
qemu-img create -f qcow2 -b base.img -F qcow2 vm.qcow2 20G   # overlay = fast reset
virt-install --name t-ubuntu --memory 2048 --vcpus 2 --import \
  --disk vm.qcow2 --disk seed.img,device=cdrom --os-variant ubuntu24.04 \
  --network default --graphics none --noautoconsole
# reset between tests: virsh destroy + recreate the qcow2 overlay (instant).
```

## CI

- **Tier 1 in CI**: a GitHub Actions job that installs Incus on the runner and
  runs the matrix. Containers are fast and CI-friendly; this gives per-PR
  cross-distro coverage.
- **Tier 2/3 in CI**: any real-kernel VM (Incus `--vm`, QEMU, Firecracker) needs a
  nested-virt-capable runner (most hosted runners aren't). Keep these
  **local/nightly/release** or on a bare-metal/self-hosted runner. Firecracker is
  the lightest of the three if a capable runner is available.

## WSL2 gotchas

- Nested virt requires `[wsl2] nestedVirtualization=true` in `%UserProfile%\.wslconfig`
  (default on Windows 11). `/dev/kvm` being present here confirms it's on.
- systemd must be enabled in `/etc/wsl.conf` (`[boot] systemd=true`) — it is.
- `incus admin init` may fail creating the bridge with
  `dnsmasq: failed to create listening socket … Address already in use` for an
  `fd42:…` (IPv6) address — WSL2 rejects the IPv6 listening socket. **Disable IPv6
  on the bridge**: init with a preseed setting `ipv6.address: none` (or
  `incus network create incusbr0 ipv6.address=none`). IPv4 is all containers need
  for `apt`/`dnf`.
- After Windows sleep the WSL clock can skew, breaking TLS/`apt`; `sudo hwclock -s`
  or restarting WSL fixes it.
- Networking is NAT; outbound (apt mirrors) works. Inbound from Windows isn't
  needed for these tests.

## Phasing

1. Add `-json` + `StepReport.Changed`, write `examples/provision.yaml`. *(in-repo, no infra)*
2. Tier-1 `testenv` (Incus) + one Ubuntu integration test (launch → run → assert → idempotent).
3. Expand to the full distro matrix; add negative/security cases.
4. Real-kernel fidelity via Incus `--vm` (reuses the harness) for release validation.
5. Wire Tier 1 into CI.
6. *(optional)* Firecracker backend behind the same `Target` interface, if speed/density or prod-parity warrants the rootfs/kernel pipeline.
```
