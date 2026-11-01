## ADDED Requirements

### Requirement: A story names the pull request that delivered it
A breakdown's deliverable SHALL accept an optional `delivered_by`: a list of one or more pull requests, each `owner/repo#number`. The plan validator SHALL refuse any other form, naming the deliverable and the value. The breakdown schema SHALL declare the field. `delivered_by` SHALL NOT count toward the digest a story's approval covers, so naming the pull request neither returns an approved story to draft nor counts as an edit to a started one.

#### Scenario: Built by hand
- **WHEN** `run-homelab-through-sortie/homelab-image` carries `delivered_by: [ortusnova/homelab#85]`
- **THEN** the plan validates

#### Scenario: Not a pull request
- **WHEN** a deliverable carries `delivered_by: [homelab#85]`
- **THEN** validation fails, naming the deliverable and `homelab#85`

#### Scenario: A store whose schema predates the field
- **WHEN** the workbench opens a project whose store holds the workbench's older breakdown schema
- **THEN** it writes the current schema into the store, and `delivered_by` validates
