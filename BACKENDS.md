# Backend behavior

This document describes common production backend behavior for the `gobox`
contract. It gives driver authors a concrete model and gives backend authors a
reference for lifecycle and security behavior. It does not require one native
implementation.

## Common behavior

A service validates protected policy before it accepts untrusted work. Policy
maps each `TemplateID` to approved owner classes, programs, storage, network
operations, limits, and required protection features. A driver supplies typed
`Launch` inputs. The backend validates them and constructs arguments. It never
accepts an arbitrary executable or raw argument list through this API.

Owner identities are durable. The service stores an unguessable owner key and
an epoch outside extension-controlled metadata. Restored metadata is checked
against that registry. A newer epoch fences old bindings. A slot has its own
generation so an old handle cannot close or mutate its replacement.

`Acquire` returns the current healthy instance when owner, slot, and template
match. It returns `ErrConflict` if the slot uses another template. It replaces
closed or failed instances only after cleanup is proved. Cancellation during
preparation cleans partial resources. A cleanup failure makes the owner
unusable until a trusted recovery process resolves it.

The service applies finite limits to owners, instances, commands, tasks, open
files, sockets, transfer size, temporary storage, output, memory, and CPU when
the selected backend can enforce them. It reports exact results through
`Status.Features` and `Status.Limits`. `StrictSatisfied` means all protections
required by that platform profile are active. `FullBoundary` means no listed
protection is omitted or excluded.

File operations resolve below held root identities. They reject path traversal,
symbolic links, reparse points, unexpected hard links, devices, and identity
changes. Temporary storage is unique to one instance generation. Persistent
storage is stable for one owner and slot. Normal instance close preserves it.
Protected owner-deletion policy can remove it only after instance cleanup.

Command start uses a fixed environment and an explicit inherited-handle set in
protected modes. Start reports setup and exec errors. Wait is repeatable. Kill,
context cancellation, sandbox close, and owner revocation terminate the full
process tree. Cleanup has a finite deadline.

TCP and UDP operations accept numeric loopback endpoints only. Unix socket
paths are confined to approved writable roots. A private-network profile uses
the sandbox network context, not host loopback. Unsupported DNS, raw sockets,
multicast, interface discovery, and native-handle access fail closed.

## Linux profiles

A full Linux profile can use a small trusted launcher and one persistent
per-instance namespace worker. A qualified implementation uses private mount,
network, PID, IPC, UTS, and cgroup namespaces; a private `/proc` and device
view; an exact mount-root view; loopback-only networking; Landlock; seccomp;
identity and capability removal; resource limits; per-instance and per-command
cgroups; and an exec-status handshake.

The full profile exposes temporary and persistent roots at stable child paths.
It bind-mounts approved read-only inputs at policy-defined targets and makes
approved programs available at stable program paths. A quota-limited `tmpfs`
can enforce temporary-storage limits. Cgroup v2 can enforce process, memory,
CPU, and task limits. The backend reports the full profile only after it probes
all required kernel, namespace, cgroup, filesystem, and launcher prerequisites.

An ordinary-user partial profile can apply `no_new_privs`, Landlock,
environment construction, inherited-handle control, writable-root protection,
process-group cleanup, rlimits, and broker-side quotas. It does not claim
private namespaces, an exact filesystem view, private networking, or complete
cgroup limits. In this profile, a read-only target normally must equal its host
source because no private mount view remaps it.

Pathname Unix sockets below writable roots work in full and partial profiles.
The full profile resolves them in its private network namespace. A partial
profile uses the host Unix socket namespace and reports that limitation.

If a host cannot prove that descendants from an interrupted session are dead,
it quarantines the durable owner instead of reusing its persistent state. A
backend must not claim strict mode on an unqualified architecture or kernel.

## Windows profile

A native Windows profile can create a stable, owner-and-slot AppContainer
identity and a restricted Low-integrity primary token. It disables
administrative groups, removes privileges, protects writable-root DACLs and
integrity labels, uses an exact inherited-handle list, creates the child
suspended, applies process mitigations, and assigns nested no-breakaway Job
Objects before the child runs.

Job Objects provide process-tree cleanup and can enforce active-process,
aggregate memory, and CPU limits. Portable terminate and kill operations stop
the complete job. Soft POSIX-style signals can return `ErrUnsupported`.

This native profile can report privilege removal, fixed environment, handle
inheritance control, protected writable storage, process isolation,
process-tree containment, and supported resource limits. It does not claim an
exact filesystem view or private networking when the child still uses the host
object and network namespaces. In that case, `StrictSatisfied` can be true for
the qualified Windows profile while `FullBoundary` is false, and status marks
those features as operating-system exclusions. A template that explicitly
requires an excluded feature fails before launch.

Native path handling rejects relative, device, UNC, alternate-stream, reparse,
and identity-changing paths. Approved executable and root objects stay open
while access rules are installed. The backend revokes temporary grants during
verified cleanup.

Not every Windows release or architecture has equivalent token, Job Object,
filesystem, and mitigation behavior. A backend must qualify each supported
matrix and return `ErrUnavailable` outside it. It must also reject limits that
the selected Job Object and filesystem mechanisms cannot enforce.

## Off and automatic modes

`Off` can be useful for development, but status must mark every protection as
`Disabled`. The child can receive the normal host environment and authority.
Policy that requires a protection must fail in this mode.

`Auto` applies the strongest qualified plan available and reports omissions as
`OmittedAuto`. It must not silently weaken a fixed plan after allocation or
launch starts. A failure while applying a selected restriction fails the
instance in every mode.
