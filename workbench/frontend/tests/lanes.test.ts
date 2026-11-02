import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import WorkerLanes from "../src/components/WorkerLanes.svelte";
import { groupLater, jobBox, pct, type Lane, type LaneJob, type StuckCode, type Worker } from "../src/lib/lanes";
import { parseRoute } from "../src/lib/router";

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
});

const NOW = 1_800_000_000;
const settle = async () => { for (let i = 0; i < 3; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const $$ = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const text = (sel: string) => $(sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";

function job(extra: Partial<LaneJob> = {}): LaneJob {
  return { identifier: "ONS-68", key: "sortie/failure-routing", title: "failure-routing", phase: "Build", rule: "build",
    started_at: NOW - 23 * 60, estimate: { seconds: 28 * 60, basis: "median of 9 past build runs" }, tokens: 1_800_000, turns: 4,
    last_event: "edited route.go", last_event_at: NOW - 20, watchable: true, repository: "sortie", ...extra };
}

const lane = (slot: number, extra: Partial<Lane>): Lane => ({ slot, kind: "free", job: null, stuck: null, reason: "", next: [], ...extra });

function worker(lanes: Lane[], extra: Partial<Worker> = {}): Worker {
  const busy = lanes.filter((l) => l.kind === "job").length;
  return { name: "p1", main: true, dispatched: true, reachable: true, slots: lanes.length, busy, started_at: NOW - 5400, poll_seconds: 15,
    verdict: busy ? "working" : "idle", reasons: [], later: [], lanes, ...extra };
}

async function render(workers: Worker[]) {
  app = mount(WorkerLanes, { target: document.body, props: { workers, slug: "ons", now: NOW } });
  await settle();
}

describe("the axis and a job's box", () => {
  it("runs from half an hour back to an hour ahead, with a now line", async () => {
    await render([worker([lane(1, {})])]);
    expect($$(".tick").map((t) => t.textContent)).toEqual(["−30 min", "−15", "now", "+15", "+30", "+45", "+1 h"]);
    expect(pct(0)).toBeCloseTo(33.33, 1);
    expect($(".nowline")!.getAttribute("style")).toContain("0.333");
  });

  it("draws a job from its start to its estimated end, solid to now and hatched after", () => {
    const b = jobBox(job(), NOW);
    expect(b.left).toBeCloseTo(pct(-23));
    expect(b.left + b.width).toBeCloseTo(pct(5));
    expect(b.split).toBe(82);
    expect(b.left_min).toBe(5);
    expect(b.overdue).toBe(false);
  });

  it("ends the box at now and says so when no estimate can say", () => {
    const b = jobBox(job({ estimate: null }), NOW);
    expect(b.left + b.width).toBeCloseTo(pct(0));
    expect(b.left_min).toBeNull();
  });

  it("cuts a job that started before the axis, and marks one past its estimate overdue", () => {
    const b = jobBox(job({ started_at: NOW - 50 * 60, estimate: { seconds: 40 * 60, basis: "b" } }), NOW);
    expect(b.cut).toBe(true);
    expect(b.overdue).toBe(true);
  });
});

describe("Scenario: The home page's top row", () => {
  it("shows each worker with its busy slots, its host facts and one lane per slot", async () => {
    await render([
      worker([lane(1, { kind: "job", job: job() }), lane(2, { reason: "Nothing approved is ready for p1." }), lane(3, {})]),
      worker([lane(1, {}), lane(2, {})], { name: "taichi", main: false, verdict: "idle" }),
    ]);
    expect($$("[data-worker]").map((w) => w.dataset.worker)).toEqual(["p1", "taichi"]);
    expect(text("[data-worker='p1'] [data-busy]")).toBe("1 of 3 slots busy");
    expect(text("[data-worker='p1'] .verdict")).toBe("moving");
    expect(text("[data-worker='p1'] .host")).toBe("serves the plan and runs agents · Sortie up 1 h 30 · polls every 15 s");
    expect($$("[data-worker='taichi'] .lane")).toHaveLength(2);
    const p1 = $$("[data-worker='p1'] .lane");
    expect(text("[data-worker='p1'] [data-lane='job'] .st")).toBe("Build · working");
    expect(p1[0].querySelector(".jm")!.textContent!.replace(/\s+/g, " ").trim()).toBe("23 min · 1.8 M tok · ~5 min left");
    expect(p1[1].querySelector(".st")!.textContent).toBe("Free");
    expect(p1[1].querySelector("p")!.textContent).toBe("Nothing approved is ready for p1.");
  });

  it("gives a job a hover card with every fact, and opens its story and session on click", async () => {
    await render([worker([lane(1, { kind: "job", job: job() })])]);
    const box = $<HTMLAnchorElement>("[data-lane='job'] .job")!;
    const tip = box.querySelector(".tip")!.textContent!.replace(/\s+/g, " ");
    for (const fact of ["ONS-68 failure-routing", "Build · sortie · on p1", "23 min running · 4 turns · 1.8 M tokens",
      "about 5 min left · median of 9 past build runs", "edited route.go · 20 s ago", "Click to watch or steer its agent"]) {
      expect(tip).toContain(fact);
    }
    expect(parseRoute(box.getAttribute("href")!)).toMatchObject({ page: "graph", view: { epic: "sortie", selected: "sortie/failure-routing" } });
  });

  it("says when a job has no estimate", async () => {
    await render([worker([lane(1, { kind: "job", job: job({ estimate: null }) })])]);
    expect(text("[data-lane='job'] .job .f")).toContain("no estimate");
    expect(text("[data-lane='job'] .tip")).toContain("no estimate: too few past runs to say");
  });

  it("shows what runs next in a free slot", async () => {
    await render([worker([lane(1, { reason: "next: ONS-74 moves to review",
      next: [{ identifier: "ONS-74", key: "ao/pr-repair", title: "pr-repair", phase: "Review", rule: "review", estimate: { seconds: 600, basis: "median of 4 past review runs" } }] })])]);
    expect(text(".lane .ll p")).toBe("next: ONS-74 moves to review");
    expect(text(".job.next .f")).toBe("next · review · ~10 min");
  });
});

describe("a stuck slot reads as stuck in its own lane, with why", () => {
  const cases: [StuckCode, Lane, string, string][] = [
    ["held", lane(1, { kind: "job", job: job(), stuck: { code: "held", text: "held: permission request, by brian" } }),
      "Build · held", "held: permission request, by brian"],
    ["stalled", lane(1, { kind: "job", job: job(), stuck: { code: "stalled", text: "no agent event for 15 min" } }),
      "Build · stalled", "no agent event for 15 min"],
    ["retrying", lane(1, { kind: "retry", job: job({ started_at: null, due_at: NOW + 180, attempt: 2 }), stuck: { code: "retrying", text: "retrying, attempt 2: agent exited 1" } }),
      "Build · retrying, attempt 2", "retrying, attempt 2: agent exited 1"],
    ["budget", lane(1, { kind: "stuck", job: job({ started_at: null, since: NOW - 3600 }), stuck: { code: "budget", text: "used its 16 sessions; the session budget holds it" } }),
      "Stuck · session budget", "used its 16 sessions; the session budget holds it"],
    ["question", lane(1, { kind: "stuck", job: job({ started_at: null, since: NOW - 600 }), stuck: { code: "question", text: "asks: Which repository?" } }),
      "Stuck · asks you", "asks: Which repository?"],
  ];
  for (const [code, l, state, why] of cases) {
    it(`for ${code}`, async () => {
      await render([worker([l, lane(2, {})], { verdict: "stuck", busy: l.kind === "job" ? 1 : 0 })]);
      expect(text("[data-worker='p1'] .verdict")).toBe("stuck");
      const el = $(`[data-stuck='${code}']`)!;
      expect(el.querySelector(".st")!.classList.contains("stalled")).toBe(true);
      expect(el.querySelector(".st")!.textContent).toBe(state);
      if (l.kind === "job") {
        expect(el.querySelector(".job")!.classList.contains("stalled")).toBe(true);
        expect(el.querySelector(".tip .alarm")!.textContent).toBe(why);
      } else {
        expect(el.querySelector(".ll .alarm")!.textContent).toBe(why);
      }
    });
  }

  it("asks a person what to do about work that waits on them", async () => {
    await render([worker([cases[3][1], { ...cases[4][1], slot: 2 }], { verdict: "stuck" })]);
    expect(text("[data-stuck='budget'] .fault")).toBe("Needs you: raise its session budget, or take the story over waits for 1 h 00 Open");
    expect(text("[data-stuck='question'] .fault")).toContain("Needs you: answer its question");
    expect($("[data-stuck='question'] .fault button")!.textContent).toBe("Answer");
  });

  it("shows a job past its estimate as overdue", async () => {
    await render([worker([lane(1, { kind: "job", job: job({ started_at: NOW - 40 * 60 }) })])]);
    expect($(".job")!.classList.contains("stalled")).toBe(true);
    expect(text(".lane .jm .alarm")).toBe("overdue");
  });
});

describe("what waits on a worker", () => {
  it("lists blocked stories once per blocker, and pull requests with what they wait on", async () => {
    const later = [
      { kind: "ready" as const, identifier: "ONS-73", key: null, title: "t", text: "#63 waits on nothing: ready to merge",
        pull: { number: 63, url: "https://github.com/o/r/pull/63", repository: "o/r" }, since: NOW - 5400, stuck: true },
      { kind: "blocked" as const, identifier: "ONS-69", key: null, title: "t", text: "after ONS-68" },
      { kind: "blocked" as const, identifier: "ONS-70", key: null, title: "t", text: "after ONS-68" },
      { kind: "waiting" as const, identifier: "ONS-78", key: null, title: "t", text: "#31 waits on CI",
        pull: { number: 31, url: "https://github.com/o/r/pull/31", repository: "o/r" } },
    ];
    expect(groupLater(later).map((i) => i.identifiers)).toEqual([["ONS-73"], ["ONS-69", "ONS-70"], ["ONS-78"]]);
    await render([worker([lane(1, {})], { later, verdict: "stuck" })]);
    const items = $$("[data-later]");
    expect(items.map((i) => i.textContent!.replace(/\s+/g, " ").trim())).toEqual([
      "ONS-73 #63 waits on nothing: ready to merge, for 1 h 30", "ONS-69, ONS-70 after ONS-68", "ONS-78 #31 waits on CI",
    ]);
    expect(items[0].classList.contains("alarm")).toBe(true);
    expect(items[0].querySelector("a")!.getAttribute("href")).toBe("https://github.com/o/r/pull/63");
  });

  it("says a worker is unreachable instead of drawing slots it cannot see", async () => {
    await render([{ name: "taichi", main: false, dispatched: true, reachable: false, verdict: "unreachable", lanes: [],
      reasons: [{ code: "unreachable", text: "taichi's Sortie is unreachable: connection refused", identifier: null, key: null }] }]);
    expect(text("[data-worker='taichi'] .verdict")).toBe("unreachable");
    expect(text("[data-worker='taichi'] .notice")).toBe("taichi's Sortie is unreachable: connection refused. Its slots show again when it answers.");
    expect($$("[data-worker='taichi'] .lane")).toHaveLength(0);
  });
});
