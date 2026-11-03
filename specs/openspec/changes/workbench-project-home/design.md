## Context

The workbench joins three sources into one graph: the spec store (planlib's
validated plan), the tracker (through Sortie's plan graph) and the live
agents (Sortie's state). The graph view shows epics, then one epic's
stories, laid out left to right by dependency depth. It is where people plan,
with the planning agent beside it.

What the graph cannot show is the project's state as a whole. The first real
project, Ortus Nova's homelab plan in the `ortus-nova-specs` store, made the
gap concrete: half of its milestone M1 was done by hand and in hardware
checklists, and the graph showed all of it as `0 done`.

Who uses the page: several people work on one project, planners, approvers
and operators. Roles are not scoped further for now: every signed-in person
sees the same home, and its actions keep the route checks they already have
(`planning/workbench-identity`).

How they use it: as a quick check before an action or two, and as an
always-on screen. Sit-down planning stays on the graph view.

## Goals / Non-Goals

**Goals:**

- Say truthfully how far the project is from its goal, counting work done
  outside Sortie.
- Answer at a glance: will the agents stay busy if everyone walks away for
  eight hours?
- Show all work that is not ready to execute, why, and how urgent it is, so
  people plan ahead of need.
- Hand any item over to the graph view in one click, ready to plan.

**Non-Goals:**

- Wall-clock estimates. Telemetry goes to Langfuse, and a separate agent will
  correlate past specs with how long they took. The home leaves room for
  those estimates (D8) but computes none.
- Editing milestones or the plan from the home. The planning agent edits the
  store; the home reads it.
- A second planning surface, a kanban board, per-person dashboards or new
  roles.

## Decisions

### D1. Milestones live in their own file in the spec store

`openspec/milestones.yaml`, beside `plan.yaml`:

```yaml
schema_version: 1
milestones:
  - id: m1-vm-complete
    title: "M1: vm-complete"
    depends_on: [m0-foundations]
    includes:
      - change: add-artifact-trust-and-updates
      - change: p3-network-boundaries
        tasks: agent
      - story: agent-platform-workers/worker-daemon
    gate: homelab-m1-gate/m1-gate
  - id: m2-metal-lan
    title: "M2: metal on the LAN"
    depends_on: [m1-vm-complete]
    goal: true
    includes:
      - change: p3-network-boundaries
        tasks: metal
```

- A milestone sits above epics but collects stories from many of them, so
  membership is a list of whole changes or single stories, not a field on a
  change or a story.
- It is not a field in `breakdown.yaml`, because a story's approval covers its
  whole breakdown entry: moving a story between milestones would return it to
  draft.
- `tasks` picks which of a change's `tasks.md` items count toward this
  milestone (`agent`, `metal`, `operator` or `all`, default `all`), so one
  change can count its vm tasks in M1 and its hardware tasks in M2.
- `gate` names the story whose completion proves the milestone done. It is
  optional: without one, a milestone is done when all it includes is done.
- At most one milestone is the `goal`: the line the home measures against.
  Without one, the last milestone in dependency order is the goal.
- A change entry brings the change's stories with `tasks: agent` or `all`
  (stories are agent work); with `metal` or `operator` it brings only those
  tasks, so a change's hardware tasks can count in a later milestone without
  its stories.
- `done: true` declares a milestone finished outside the tracker. Work done
  by hand before the tracker knew of it (the homelab plan's M0, whose stories
  were never synced) can otherwise never read as done, and the home would ask
  for its stories to be approved, which would let agents redo them. Its
  counts still show what the tracker and the checklists record.
- The file is optional. Without it, the home shows the project as one
  implicit milestone holding every planned change.

*Alternative rejected:* deriving milestones from the graph's depth columns.
The columns follow dependencies, not outcomes, and cannot name a goal.

*Alternative rejected:* parsing a project's roadmap prose. It ties the
workbench to one project's document and breaks on any edit.

### D2. Task kinds are a trailing tag

A `tasks.md` item may end with `[metal]` or `[operator]`:

```markdown
- [ ] 6.2 Apply the Talos host rules on the metal nodes. [metal]
- [ ] 3.7 Enroll the YubiKey and declare p1 a mgmt peer. [operator]
- [x] 6.1 Add Talos host rules to the vm machine files.
```

Untagged items are agent work. The tag is plain text, so OpenSpec and
people read it as part of the line. A breakdown lists only work an agent can
do, so tagged tasks are the record of everything else.

*Alternative rejected:* a section heading per kind. Existing checklists mix
kinds within one numbered group, and regrouping them renumbers tasks that
other documents cite.

### D3. Progress counts stories and tasks, side by side

A milestone's progress has up to three parts, each `done / total`:

- **Stories:** the stories it includes. A story is done when its tracker issue
  is in the tracker's done state.
- **Agent tasks** and **person tasks** (metal and operator): the `tasks.md`
  items it counts, ticked or not.

The parts are shown separately, never summed: a story and a checkbox are not
the same size of work, and a sum would hide which kind is left.

Progress is not blocker satisfaction. Sortie lets a story's dependents start
only after a merge it latched or a confirmed no-change claim
(`sortie/blocker-satisfaction`), and that rule stays. The home reports what is
done, including work done by hand, and the graph keeps showing what agents may
start.

*Alternative rejected:* counting only Sortie-latched stories. That is the
current behavior, and it reported a finished change as `0/5 done`.

### D4. The critical path is computed per milestone

For the next unfinished milestone (the first in dependency order whose
gate, or whose contents, are not done), the critical path is the longest
chain of unfinished stories in it that ends at its gate, measured in
stories. Without a gate, the chain ends at any of its last stories. Ties
break by plan order. Until estimates exist (D8), a story counts as one.

### D5. Execution health is a verdict with its reasons

The first focal point answers: if everyone walks away, will no agent time be
wasted?

| Verdict | When |
| --- | --- |
| **Stuck** | Sortie is unreachable, the last sync failed, a run is held (waiting on a permission, a budget, or a person), or a slot is free while nothing is ready and released stories wait on reviews, merges, deploys, questions or held budgets |
| **Done** | Nothing planned is left to run |
| **Idle now** | An agent slot is free and no released story is waiting for one |
| **Will idle** | Every slot is busy, but fewer released stories wait than there are slots |
| **Healthy** | Every slot is busy and at least as many released stories wait as there are slots |

The first verdict that applies wins, in this order. Its reasons name the
held runs, the failed sync, what the stories wait on and who acts on each,
or how many slots are free against how much is ready. **Runway** is the number of released, unstarted stories, shown against
the slot count. It is measured in stories until estimates turn it into hours
(D8). Agent slots and held runs come from Sortie's state.

### D6. "Not ready to execute" lists every blocked unit of work, ranked

The second focal point is the planning frontier. It lists every story in an
unfinished milestone that cannot run yet, and every included change with no
breakdown. Each item has one reason, the first that applies:

1. **No breakdown:** the change has no `breakdown.yaml`.
2. **No spec:** a `story` (not an enabler) covers no requirement.
3. **Needs re-review:** its approval was withdrawn.
4. **Not approved:** a draft.
5. **Held for approval:** approved, but a blocker is not.
6. **Waits on a person:** a person task (D2) in the same milestone blocks it.
   A person task blocks the stories that the milestone's gate depends on. The
   file does not record finer task-to-story links, so the item names the task.
7. **Waits on work:** an unfinished blocker is planned and approved; nothing
   but time is needed.

Reasons 1 to 6 need a person. Reason 7 is listed but shown quieter.

**Slack** is how much work can still run before an item starts holding
agents up: zero when an agent slot is free now and the item is what stands
between it and work, otherwise the number of stories that will run first on
the item's path. It is counted in stories until estimates exist (D8), then
in hours. **Urgency** orders the items: least slack first, then items on the
critical path, then items that more unfinished stories transitively depend
on, then plan order.

On the home, the items that need a person sit in three columns by what a
person does: **Approve** (a story, or a change whose drafts are reviewed
together), **Write a spec** (no breakdown or no spec), and **Operator tasks**.
Each item carries its rank across all three columns, the top three marked,
and a slack bar: shorter is more urgent, red when blocking now, amber within
four stories, green with room to spare, with the slack also in words. Each
has its action: Approve, Review, Plan in graph, or Open task.

### D7. A timeline across the page

The top of the page is one horizontal timeline from the first milestone to
the last:

```
 M0 ━━━━━━━━━━━━ M1 ━━━━━━━━●━━━━━━━━━ M2 (goal) ─ ─ ─ M3 ─ ─ ─ M4
                      ▲ we are here     ✕ ✕            ✕
```

- Each milestone is a tick, with its title. The goal's tick is heavier and
  labelled as the goal.
- The axis is work size: each milestone's span is proportional to the stories
  and tasks it holds. **We are here** sits at the share of that work that is
  done. A finished stretch is drawn solid, the rest dashed.
- Each **✕** is an upcoming blocker that needs a person (D6 reasons 1 to 6),
  placed at its milestone. Hovering or focusing one names it.
- When estimates exist (D8), the axis can become wall-clock time without
  changing the marks.

The timeline is the at-a-glance view. Under it, in order (`prototype.html`
in this change is the locked composition, with real data from the
`ortus-nova-specs` plan):

1. **Health strip:** the execution verdict with its reasons (D5), agent slots
   in use and free, the runway, and "needs a person" with its count by kind.
   Slots and runway show their recent history.
2. **Critical path:** the chain of stories to the next milestone's gate, each
   marked with what it needs.
3. **Needs a person:** the three ranked columns (D6).

Charts that answer "how healthy?" or "how far?" belong here; a chart that
answers neither does not.

### D8. Room for estimates

Each story may later carry an estimate from the telemetry loop. The home's
data model keeps an optional `estimate` per story, unused now. When present,
the critical path, the runway and the timeline's axis use it instead of
counting stories. Nothing in this change computes one.

### D9. Live, and quiet when nothing changes

The home updates over the project's existing event stream, as the graph
does. A story whose state changed since the last update is marked for a few
seconds, so a screen across the room shows motion where work moves. Motion
honors reduced-motion settings. No information is carried by color alone.

### D10. Deep links into the graph

Every story, change and milestone on the home is a link. A story opens the
graph at its epic with the story selected and attached to the planning chat;
a change opens its epic; a milestone opens the project level with its
changes marked. A project's address opens the home; the graph has its own
address, and both are one click from each other.

## Risks / Trade-offs

- **Two progress measures (D3).** Showing stories and tasks apart is more to
  read than one percentage. It is the honest version: they are different
  units, and the remaining kind matters.
- **Task tags are a convention.** An untagged hardware task counts as agent
  work until someone tags it. The home shows the counts by kind, so a wrong
  tag is visible.
- **Person tasks block coarsely (D6.6).** A person task is attributed to the
  milestone's gate, not to specific stories. Finer links can come later as an
  optional field in `milestones.yaml`.
- **Progress is not satisfaction (D3).** The home can show a story done that
  the graph still treats as an unsatisfied blocker. Each view states its rule.

## Open Questions

- Pull requests waiting for review: Sortie knows a story's pull request and
  its state. Listing them on the home (as "waits on a person") needs that
  state in Sortie's plan graph; it may land in this change or after it.
