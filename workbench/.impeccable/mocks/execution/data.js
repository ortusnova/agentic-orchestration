// Prototype data. "now" is the real state read from both Sorties, the
// worker hosts and GitHub at 11:20 UTC on 2026-11-02. "busy" is an example.
// ETA basis: the successful runs in each Sortie's history (p1: 37 runs,
// median 10 min, 75% within 24 min; taichi: 13 runs, median 17 min).
// Tokens: no run on either Sortie has reported usage yet (0 of 250).
window.PROTO = {
  now: {
    workers: [
      {
        name: "p1", role: "control plane and worker", slots: 3,
        host: { cpus: 22, mem: "62 GiB", memFree: "39 GiB" },
        running: [],
        machines: [],
        queue: [
          { id: "ONS-67", title: "repeat-stop", repo: "sortie", step: "Review and merge PR sortie#28", why: "no review agent runs it yet", kind: "unowned", holds: 3 },
          { id: "ONS-47", title: "one-sortie", repo: "agentic-orchestration", step: "Review PR #60, then the cutover", why: "no review agent runs it yet", kind: "unowned", holds: 3 },
          { id: "ONS-74", title: "pr-repair-reactions", repo: "agentic-orchestration", step: "Review and merge PR #64", why: "no review agent runs it yet", kind: "unowned", holds: 0 },
          { id: "ONS-76", title: "deploy-drains-host", repo: "agentic-orchestration", step: "Review and merge PR #62", why: "no review agent runs it yet", kind: "unowned", holds: 0 },
          { id: "ONS-75", title: "stuck-means-waiting", repo: "agentic-orchestration", step: "UI: look at it and merge PR #61", why: "the merge is your approval", kind: "you", holds: 0 },
          { id: "ONS-73", title: "operator-alerts", repo: "agentic-orchestration", step: "UI: look at it and merge PR #63", why: "the merge is your approval", kind: "you", holds: 0 },
          { id: "ONS-68", title: "failure-routing", repo: "sortie", step: "Build", why: "after ONS-67", kind: "blocked", holds: 2 },
          { id: "ONS-20", title: "kit-reads-homelab-egress", repo: "agentic-orchestration", step: "Build", why: "after ONS-19", kind: "blocked", holds: 0 }
        ]
      },
      {
        name: "taichi", role: "worker, nested KVM", slots: 2,
        host: { cpus: 32, mem: "91 GiB", memFree: "63 GiB" },
        running: [],
        machines: [
          { name: "hl-ons-34-proof", what: "ONS-34 image-archive: live proof (vm CI acceptance)", age: "1 h 25", started: "an agent session on p1, not Sortie" },
          { name: "8 stopped Sortie machines", what: "kept from ONS-34, 48, 49, 50, 51, 52, 55, 61", age: "", started: "Sortie" }
        ],
        queue: [
          { id: "ONS-34", title: "image-archive", repo: "homelab", step: "Live proof, then merge PR #103", why: "running outside Sortie, on this host", kind: "elsewhere", holds: 2, critical: true },
          { id: "ONS-52", title: "seed-security-updates", repo: "homelab", step: "Live proof on seed-vm, then merge PR #105", why: "after ONS-34", kind: "blocked", holds: 2 },
          { id: "ONS-61", title: "simplify-proof-fixture", repo: "homelab", step: "Live seed-proof, then merge PR #107", why: "after ONS-34", kind: "blocked", holds: 2 },
          { id: "ONS-48", title: "vm-upstream-mirrors", repo: "homelab", step: "Live k8s-up, then merge PR #106", why: "after ONS-34", kind: "blocked", holds: 1 },
          { id: "ONS-37", title: "canary-rebuild", repo: "homelab", step: "Build", why: "after ONS-34", kind: "blocked", holds: 1, critical: true },
          { id: "ONS-55", title: "proof-cadence", repo: "homelab", step: "Build", why: "your answer: the 3.3 proof cadence", kind: "you", holds: 1 },
          { id: "ONS-49", title: "talos-installer-reproducible", repo: "homelab", step: "Build again", why: "16 of 16 sessions used on a refused push; token fixed", kind: "held", holds: 1 },
          { id: "ONS-50", title: "scan-coverage-and-register", repo: "homelab", step: "Build again", why: "16 of 16 sessions used on a refused push; token fixed", kind: "held", holds: 1 },
          { id: "ONS-51", title: "failure-notifications", repo: "homelab", step: "Build again", why: "16 of 16 sessions used on a refused push; token fixed", kind: "held", holds: 1 }
        ]
      }
    ]
  },
  busy: {
    example: true,
    workers: [
      {
        name: "p1", role: "control plane and worker", slots: 3,
        host: { cpus: 22, mem: "62 GiB", memFree: "21 GiB" },
        running: [
          { id: "ONS-68", title: "failure-routing", repo: "sortie", phase: "Build", state: "working", turn: 4, elapsed: 23, tokens: "1.8 M", cost: "$0.42", eta: "about 5 min", etaBasis: "median of 37 runs", last: "edited internal/orchestrator/route.go", lastAgo: "20 s" },
          { id: "ONS-74", title: "pr-repair-reactions", repo: "agentic-orchestration", phase: "Review", state: "working", turn: 2, elapsed: 6, tokens: "410 k", cost: "$0.09", eta: "about 10 min", etaBasis: "median of 37 runs", last: "ran make -C dev/stage0 test", lastAgo: "1 min" },
          { id: "ONS-69", title: "workbench-platform-faults", repo: "agentic-orchestration", phase: "Build", state: "starting", attempt: 2, elapsed: 1, tokens: null, eta: null, lastError: "session start: response_timeout", last: "starting the machine", lastAgo: "40 s" }
        ],
        machines: [],
        queue: [
          { id: "ONS-70", title: "health-checks", repo: "sortie", step: "Build", why: "takes the next free slot", kind: "ready", holds: 0 },
          { id: "ONS-76", title: "deploy-drains-host", repo: "agentic-orchestration", step: "Fix CI on PR #62", why: "takes the next free slot", kind: "ready", holds: 0 },
          { id: "ONS-72", title: "unfinished-work-handoff", repo: "sortie", step: "Build", why: "after ONS-68", kind: "blocked", holds: 0 }
        ]
      },
      {
        name: "taichi", role: "worker, nested KVM", slots: 2,
        host: { cpus: 32, mem: "91 GiB", memFree: "18 GiB" },
        running: [
          { id: "ONS-52", title: "seed-security-updates", repo: "homelab", phase: "Live proof", state: "working", step: "vm chain 9 of 13: k8s-up", elapsed: 52, tokens: "640 k", cost: "$0.15", eta: "about 25 min", etaBasis: "the vm chain's median, 38 min", last: "k8s-up: waiting for Longhorn", lastAgo: "2 min" },
          { id: "ONS-37", title: "canary-rebuild", repo: "homelab", phase: "Build", state: "stalled", turn: 3, elapsed: 41, tokens: "2.3 M", cost: "$0.55", eta: "overdue", etaBasis: "75% of runs finish within 24 min", last: "no agent event since turn 3", lastAgo: "14 min" }
        ],
        machines: [],
        queue: [
          { id: "ONS-61", title: "simplify-proof-fixture", repo: "homelab", step: "Live seed-proof, then merge", why: "takes the next free slot", kind: "ready", holds: 2 },
          { id: "ONS-48", title: "vm-upstream-mirrors", repo: "homelab", step: "Live k8s-up, then merge", why: "after ONS-61", kind: "ready", holds: 1 },
          { id: "ONS-53", title: "seed-boot-fallback", repo: "homelab", step: "Build", why: "after ONS-52", kind: "blocked", holds: 1 }
        ]
      }
    ]
  }
};
