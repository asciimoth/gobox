// Package gobox defines capabilities for host-managed child-program sandboxes.
// It is a contract between an application, sandbox backends, and reusable
// program drivers. It does not select a backend or define host policy.
//
// # Trust boundary
//
// The host owns Service, protected policy, owner identities, templates, and
// program definitions. A driver receives only OwnerSandboxes. It can select an
// approved template and program, but it cannot select native executables,
// weaken protections, choose an owner key, or request ordinary host execution.
// A missing service must return ErrUnavailable. It must not use host execution
// as a fallback.
//
// # Ownership and instances
//
// Owner keys are durable, unguessable identities. Names are labels. A rename
// changes Name and NameRevision only. BindOwner must authorize restored owner
// metadata against host-owned durable state. Imported metadata is not authority.
// Hosts fence stale epochs and generations when they replace a logical owner or
// sandbox instance.
//
// Acquire is idempotent for one owner, slot, and template. A different template
// in the same slot requires Remove first. A failed instance cannot satisfy a
// later Acquire. Remove, Sandbox.Close, and owner release stop all instance
// resources. They remove temporary storage only after verified cleanup and
// preserve persistent storage according to protected host policy. If cleanup
// cannot prove that the process tree is dead, the host must prevent unsafe
// identity and persistent-state reuse.
//
// # Commands
//
// Command.Start is single-use. It returns after setup and program execution
// succeed. It does not prove application readiness. Its context controls the
// complete command lifetime. Cancellation and Kill stop the complete command
// tree but do not destroy a reusable sandbox. Wait has a stable, repeatable
// result and returns ExitError for an unsuccessful exit. Unsupported soft
// signals return ErrUnsupported.
//
// Each standard pipe can be requested once, before Start. Unrequested stdin is
// closed. Unrequested output is discarded or sent to a bounded host log sink.
// Requested output uses bounded buffers and normal backpressure. Callers must
// drain both requested output streams. Wait and cleanup must bound pipe shutdown
// when a descendant keeps a pipe open.
//
// # Files and sockets
//
// Path values are portable relative paths, not host paths. ValidatePath checks
// syntax only. A backend must confine every operation with held directory
// identities and reject symbolic links, reparse points, hard-link aliases, and
// path replacement races. Root paths can be read but cannot be removed or
// renamed. File metadata must not expose native handles or ownership data.
// ChildPath and ProgramPath describe the child view only. They do not grant host
// filesystem access.
//
// Internet socket methods accept only supported numeric loopback endpoints.
// Backends must validate every packet destination. Unix sockets use Path and
// confined resolution. Operations use the selected sandbox network context. A
// backend that reports PrivateNetwork must not silently connect to host
// loopback. Returned objects must not expose native handles.
//
// # Status and failure
//
// Status is a copy of the fixed protection plan and current lifecycle state.
// It contains no protected paths, native handles, or secrets. Native errors are
// bounded and sanitized. A failed selected restriction or an uncertain mutation
// fails the operation in all modes. A backend must not retry with a weaker plan.
package gobox
