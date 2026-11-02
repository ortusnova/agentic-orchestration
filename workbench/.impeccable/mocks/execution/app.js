const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const mins = (m) => (m >= 60 ? `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")}` : `${m} min`);

const KIND = {
  ready: "Ready",
  unowned: "Nobody on it",
  you: "Waits on you",
  blocked: "Blocked",
  held: "Held",
  elsewhere: "Outside Sortie",
};

function slotCard(w, r, i) {
  if (!r) {
    const next = w.queue.find((q) => q.kind === "ready");
    const unowned = w.queue.filter((q) => q.kind === "unowned").length;
    const say = next
      ? `<p>Takes <b>${esc(next.id)}</b> ${esc(next.title)} next</p>`
      : unowned
        ? `<p class="unowned">Free while ${unowned} pull requests wait for a review agent</p>`
        : `<p>Nothing approved can start on this worker</p>`;
    return `<article class="slot free">
      <div class="slot-no">Slot ${i + 1}</div>
      <div class="free-word">Free</div>
      ${say}
    </article>`;
  }
  const state = { working: "Working", starting: `Starting, attempt ${r.attempt}`, stalled: "Stalled" }[r.state];
  const tokens = r.tokens ? `<b>${esc(r.tokens)}</b><span>${esc(r.cost)}</span>` : `<b class="na">not reported</b><span>agent sends no usage</span>`;
  const eta = r.eta
    ? `<b class="${r.eta === "overdue" ? "alarm" : ""}">${esc(r.eta)}</b><span>${esc(r.etaBasis)}</span>`
    : `<b class="na">—</b><span>until the session starts</span>`;
  return `<article class="slot ${r.state}">
    <div class="slot-top"><span class="slot-no">Slot ${i + 1}</span><span class="chip ${r.state}"><i></i>${esc(r.phase)} · ${state}</span></div>
    <h3><span class="id">${esc(r.id)}</span> ${esc(r.title)}</h3>
    <div class="repo">${esc(r.repo)}${r.step ? ` · ${esc(r.step)}` : r.turn ? ` · turn ${r.turn}` : ""}</div>
    <dl class="metrics">
      <div><dt>Running</dt><dd><b class="num">${mins(r.elapsed)}</b></dd></div>
      <div><dt>Tokens</dt><dd>${tokens}</dd></div>
      <div><dt>Finishes in</dt><dd>${eta}</dd></div>
    </dl>
    <div class="last ${r.state === "stalled" ? "alarm" : ""}">${r.lastError ? `Last error: ${esc(r.lastError)} · ` : ""}${esc(r.last)} <span>${esc(r.lastAgo)} ago</span></div>
    <div class="actions"><button class="primary" data-watch="${esc(w.name)}:${i}">Watch</button><button class="ghost" data-watch="${esc(w.name)}:${i}" data-steer>Steer</button></div>
  </article>`;
}

function queueRow(q) {
  return `<li class="q ${q.kind}">
    <span class="id">${esc(q.id)}</span>
    <span class="t">${esc(q.title)}${q.critical ? ' <em class="crit">critical path</em>' : ""}</span>
    <span class="step">${esc(q.step)}</span>
    <span class="why"><i></i>${KIND[q.kind]}<small>${esc(q.why)}</small></span>
    <span class="holds">${q.holds ? `holds ${q.holds}` : ""}</span>
  </li>`;
}

function worker(w) {
  const busy = w.running.length;
  const slots = Array.from({ length: w.slots }, (_, i) => slotCard(w, w.running[i], i)).join("");
  const machines = w.machines.length
    ? `<div class="machines"><h4>Machines Sortie did not start</h4>${w.machines
        .map((m) => `<div class="m"><span class="id">${esc(m.name)}</span> ${esc(m.what)}<span class="mute"> · ${esc(m.started)}${m.age ? ` · ${esc(m.age)}` : ""}</span></div>`)
        .join("")}</div>`
    : "";
  return `<section class="worker" aria-labelledby="w-${w.name}">
    <header class="w-head">
      <h2 id="w-${w.name}">${esc(w.name)}</h2>
      <span class="role">${esc(w.role)}</span>
      <span class="busy"><b class="num">${busy}</b> of ${w.slots} slots busy</span>
      <span class="host">${w.host.cpus} CPUs · ${esc(w.host.memFree)} of ${esc(w.host.mem)} free</span>
    </header>
    <div class="slots" style="--n:${w.slots}">${slots}</div>
    ${machines}
    <div class="queue"><h4>Next on ${esc(w.name)} <span class="mute">${w.queue.length} stories, in the order it takes them</span></h4><ol>${w.queue.map(queueRow).join("")}</ol></div>
  </section>`;
}

const TX = [
  ["me", "› sortie: run ONS-68 failure-routing (attempt 1)"],
  ["", "Reading the spec: agent-platform/run-supervision#A stopped failure goes to whoever can act on it"],
  ["tool", "read internal/orchestrator/exit.go"],
  ["", "The signature from ONS-67 gives each stopped failure a class. I route a platform fault into one record per signature."],
  ["tool", "edit internal/orchestrator/route.go (+84 −3)"],
  ["tool", "go test ./internal/orchestrator -run Route  ok  2.1s"],
];

let state = "now";
function render() {
  const d = window.PROTO[state];
  $("#app").innerHTML = (d.example ? `<p class="example">Example data, to show running slots. Switch to <b>Now</b> for the real state.</p>` : "") + d.workers.map(worker).join("");
}

document.addEventListener("click", (e) => {
  const tab = e.target.closest("[data-state]");
  if (tab) {
    state = tab.dataset.state;
    document.querySelectorAll("[data-state]").forEach((b) => b.setAttribute("aria-selected", b === tab));
    $("#session").hidden = true;
    render();
  }
  const w = e.target.closest("[data-watch]");
  if (w) {
    const [name, i] = w.dataset.watch.split(":");
    const r = window.PROTO[state].workers.find((x) => x.name === name).running[+i];
    $("#s-title").textContent = `${r.id} ${r.title}`;
    $("#s-sub").textContent = `${name} · slot ${+i + 1} · ${r.phase} · ${mins(r.elapsed)}`;
    $("#s-tx").innerHTML = TX.map(([k, t]) => `<div class="${k}">${esc(t)}</div>`).join("");
    $("#session").hidden = false;
    if (w.hasAttribute("data-steer")) $("#session input").focus();
  }
  if (e.target.id === "s-close") $("#session").hidden = true;
});
render();
