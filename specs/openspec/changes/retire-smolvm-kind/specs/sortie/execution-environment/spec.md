## MODIFIED Requirements

### Requirement: Workflow selects an execution environment kind
Sortie SHALL read `environment.kind` from the workflow front matter. The accepted values are `local` and `remote`. Under `remote`, Sortie hands each run to worker hosts (the Ortus Nova spec store's `agent-platform/worker-dispatch`). When `environment` or `environment.kind` is absent, the kind SHALL be `local`. The retired kind `smolvm` SHALL fail validation with an error that names `environment.kind`, `smolvm` and `remote`. Any other unknown kind SHALL fail validation with an error that names the setting and the value.

At startup, a validation failure SHALL stop Sortie. On reload, Sortie SHALL keep the previous configuration and report the failure through `LastLoadError()`.

#### Scenario: No environment block
- **WHEN** a workflow has no `environment` block
- **THEN** Sortie runs every issue with the `local` kind

#### Scenario: Unknown kind on reload
- **WHEN** a running Sortie reloads a workflow that sets `environment.kind: firecracker`
- **THEN** Sortie keeps running with the previous configuration
- **AND** `LastLoadError()` reports an error that names `environment.kind` and `firecracker`

#### Scenario: The remote kind
- **WHEN** a workflow sets `environment.kind: remote`
- **THEN** validation accepts it

#### Scenario: Retired
- **WHEN** a workflow sets `environment.kind: smolvm`
- **THEN** validation fails with an error that names `environment.kind`, `smolvm` and the `remote` kind

## REMOVED Requirements

### Requirement: smolvm excludes SSH workers
**Reason**: There is no `smolvm` kind; a remote job runs on a worker daemon, not over SSH.
**Migration**: None.
