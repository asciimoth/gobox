# gobox

`gobox` is a Go interface for pluggable, host-managed subprocess sandboxes. It
lets applications and reusable program drivers share one execution contract
without sharing a sandbox implementation.

The package is dependency-free. It contains:

- owner, sandbox, command, file, socket, status, and typed-launch interfaces;
- portable error values and validation helpers;
- bounded command-output helpers; and
- lifecycle and security requirements for compatible backends.

## Integration model

The embedding application owns a `gobox.Service`. It creates or restores an
`Owner`, binds that owner, and gives only the resulting `OwnerSandboxes` to an
extension. The extension can acquire named sandbox slots and start programs
that protected host policy permits.

```go
func RunHelper(ctx context.Context, sandboxes gobox.OwnerSandboxes) error {
	box, err := sandboxes.Acquire(ctx, "helper", "helper-v1")
	if err != nil {
		return err
	}

	command, err := box.NewCommand("helper", gobox.Launch{
		Inputs: map[string]gobox.Value{
			"endpoint": {
				Kind:     gobox.EndpointValue,
				Endpoint: "127.0.0.1:9000",
			},
		},
	})
	if err != nil {
		return err
	}
	_, err = gobox.Run(ctx, command)
	return err
}
```

The driver does not receive `Service`. It cannot select an executable path,
construct raw arguments, choose a durable owner key, or weaken host policy.
Template and program identifiers are logical names that the host resolves.

## Compatibility rules

A compatible backend must implement the contracts in the package documentation.
Important rules include:

- no fallback to an ordinary host process when sandbox support is unavailable;
- idempotent acquisition for one owner, slot, and template;
- complete command-tree cancellation and bounded cleanup;
- portable path syntax plus native race-resistant filesystem confinement;
- loopback and Unix socket operations in the selected network context;
- stable command exit results and copied, secret-free status data; and
- fail-closed behavior when a selected protection or cleanup proof fails.

See [BACKENDS.md](BACKENDS.md) for concrete Linux and Windows behavior that a
production backend can provide. Those profiles are examples, not requirements
that every implementation must use the same operating-system mechanisms.

## Versioning

Interfaces in this package are shared by applications, drivers, and backends.
An incompatible interface change requires a new major module version. Add
optional backend capabilities as small extension interfaces when possible.
