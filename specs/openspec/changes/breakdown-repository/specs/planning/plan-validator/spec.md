## MODIFIED Requirements

### Requirement: The validated graph is the exchange format
When validation passes, the validator SHALL output the combined graph as JSON in a versioned graph format. The graph SHALL hold the schema version, a digest of the source files, and each node's plan key, kind, title, body, and approval state. Approval SHALL come from the approvals the caller passes, as plan key and digest; without them every story reads as draft. Each story SHALL also carry its review gate. In a version 2 graph, each story SHALL also carry its resolved repository, the deliverable's own or else its breakdown's, and a story that names none SHALL carry no repository. It SHALL also hold the parent edges and the blocking edges, with the dependents of a superseded story pointing at its replacement. The JSON Schema for this graph format SHALL be published with the validator. When validation fails, the validator SHALL output no graph.

#### Scenario: Valid store
- **WHEN** the store's files pass validation
- **THEN** the validator outputs a graph document that matches the graph schema

#### Scenario: Run by the planning agent
- **WHEN** the planning agent runs `planlib validate` with no approvals
- **THEN** every story reads as draft, and validation still checks every file

#### Scenario: A story in the breakdown's repository
- **WHEN** a breakdown names `repository: sortie` and its deliverable `daemon` names `repository: agentic-orchestration`
- **THEN** the graph's node for `daemon` carries `repository: agentic-orchestration`, and every other story of that breakdown carries `repository: sortie`

#### Scenario: A story that names no repository
- **WHEN** neither a deliverable nor its breakdown names a repository
- **THEN** the deliverable's graph node carries no `repository`
