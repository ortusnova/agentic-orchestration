// Checks pack/sortie-usage.js against a fake plugin context, for
// test-opencode-usage.sh:
//
//   node plugin-unit.mjs PLUGIN.mjs DIR
//
// Prints ok:/FAIL: lines and exits non-zero on a failure.
import fs from "node:fs"
import path from "node:path"

const [pluginPath, dir] = process.argv.slice(2)
let failed = false
const check = (what, cond, detail = "") => {
  console.log(`${cond ? "ok" : "FAIL"}: ${what}${cond ? "" : ` ${detail}`}`)
  if (!cond) failed = true
}
const tick = () => new Promise((r) => setTimeout(r, 20))
const tokens = (input, output, reasoning = 0, read = 0, write = 0) => ({ input, output, reasoning, cache: { read, write } })

// An event stream the test pushes into, and sessions session.get answers.
function fakeContext(sessions) {
  const queue = []
  let wake
  const ctx = {
    calls: [],
    shellHooks: [],
    location: { directory: "/workspace" },
    session: {
      get: async ({ sessionID }) => {
        ctx.calls.push(sessionID)
        if (!sessions[sessionID]) throw new Error(`no session ${sessionID}`)
        return sessions[sessionID]
      },
    },
    shell: { hook: async (name, fn) => ctx.shellHooks.push([name, fn]) },
    event: {
      subscribe: ({ signal }) => ({
        async *[Symbol.asyncIterator]() {
          while (!signal?.aborted) {
            if (queue.length) yield queue.shift()
            else await new Promise((r) => (wake = r))
          }
        },
      }),
    },
    push: (type, data) => {
      queue.push({ type, data })
      wake?.()
    },
  }
  return ctx
}

const file = path.join(dir, "state", "usage.json")
const read = () => JSON.parse(fs.readFileSync(file, "utf8"))
const consoleCalls = []
for (const k of ["log", "error", "warn", "info", "debug"]) console[k] = ((orig) => (...a) => {
  if (!String(a[0]).match(/^(ok|FAIL): /)) consoleCalls.push(a)
  orig(...a)
})(console[k])

const plugin = (await import(path.resolve(pluginPath))).default
check("the plugin has an ID and a setup", typeof plugin?.id === "string" && typeof plugin?.setup === "function")

// Without SORTIE_USAGE_FILE (an opencode agent-acp did not start), nothing.
delete process.env.SORTIE_USAGE_FILE
const idle = fakeContext({})
check("without SORTIE_USAGE_FILE it does nothing", plugin.setup(idle) === undefined && !fs.existsSync(file))

const start = Date.now()
const old = start - 3_600_000
const sessions = {
  ses_root: { id: "ses_root", time: { created: start + 1 }, tokens: tokens(0, 0), model: { providerID: "anthropic", id: "claude-opus-5-5" } },
  ses_child: { id: "ses_child", parentID: "ses_root", time: { created: start + 2 }, tokens: tokens(0, 0) },
  // A session of an earlier run that this run resumed.
  ses_old: { id: "ses_old", time: { created: old }, tokens: tokens(90_000, 9_000, 0, 50_000, 1_000) },
}
process.env.SORTIE_USAGE_FILE = file
const ctx = fakeContext(sessions)
const cleanup = plugin.setup(ctx)
check("setup returns a cleanup", typeof cleanup === "function")
check("it takes SORTIE_USAGE_FILE out of the environment, so no opencode the agent starts writes the file", process.env.SORTIE_USAGE_FILE === undefined)
check("it writes zero totals at once", fs.existsSync(file) && read().input_tokens === 0 && read().output_tokens === 0, fs.existsSync(file) ? JSON.stringify(read()) : "no file")
await tick()

ctx.push("session.created", { sessionID: "ses_root" })
ctx.push("session.step.started", { sessionID: "ses_root", model: { providerID: "anthropic", id: "claude-opus-5-5" } })
ctx.push("session.step.ended", { sessionID: "ses_root", tokens: tokens(100, 20, 5, 1_000, 300) })
ctx.push("session.usage.updated", { sessionID: "ses_root", tokens: tokens(100, 20, 5, 1_000, 300) })
ctx.push("session.created", { sessionID: "ses_child" })
ctx.push("session.usage.updated", { sessionID: "ses_child", tokens: tokens(40, 10, 0, 400, 0) })
ctx.push("session.usage.updated", { sessionID: "ses_root", tokens: tokens(150, 30, 5, 2_000, 300) })
await tick()
let u = read()
check("it sums every session of the run, a subagent's child session too",
  u.input_tokens === 190 && u.output_tokens === 45 && u.cache_read_tokens === 2_400 && u.cache_write_tokens === 300 && u.sessions === 2,
  JSON.stringify(u))
check("output counts reasoning tokens", u.output_tokens === 30 + 5 + 10, JSON.stringify(u))
check("the model is the one that used the most tokens", u.model === "claude-opus-5-5", JSON.stringify(u))

// A resumed session counts only what this run spent.
ctx.push("session.step.ended", { sessionID: "ses_old", tokens: tokens(7, 3, 0, 11, 0) })
ctx.push("session.usage.updated", { sessionID: "ses_old", tokens: tokens(90_007, 9_003, 0, 50_011, 1_000) })
await tick()
u = read()
check("a resumed session counts only the steps it ran in this run",
  u.input_tokens === 197 && u.output_tokens === 48 && u.cache_read_tokens === 2_411 && u.sessions === 3, JSON.stringify(u))
ctx.push("session.usage.recorded", { sessionID: "ses_old", source: "compaction", tokens: tokens(1, 1, 0, 0, 0) })
await tick()
u = read()
check("and its compactions and titles", u.input_tokens === 198 && u.output_tokens === 49, JSON.stringify(u))

// A session that session.get cannot read still counts, and the error goes
// to the log, never to the console.
ctx.push("session.usage.updated", { sessionID: "ses_gone", tokens: tokens(2, 1) })
ctx.push("session.idle", { sessionID: "ses_root" })
ctx.push("not.a.session", {})
await tick()
u = read()
check("a session it cannot read still counts", u.input_tokens === 200 && u.output_tokens === 50, JSON.stringify(u))
const log = fs.existsSync(`${file}.log`) ? fs.readFileSync(`${file}.log`, "utf8") : ""
check("errors go to the usage file's log", log.includes("no session ses_gone"), log)
check("nothing goes to the console, which may be ACP's stdout", consoleCalls.length === 0, JSON.stringify(consoleCalls))

// A second location in the same process joins the first, without a reset.
process.env.SORTIE_USAGE_FILE = file
check("a second setup in the process does nothing", plugin.setup(fakeContext({})) === undefined && read().input_tokens === 200)
delete process.env.SORTIE_USAGE_FILE

// The shell hook keeps the path out of every command the agent runs.
check("it registers a shell hook", ctx.shellHooks.length === 1 && ctx.shellHooks[0][0] === "create.before")
const shellEvent = { env: { PATH: "/bin", SORTIE_USAGE_FILE: file } }
ctx.shellHooks[0]?.[1](shellEvent)
check("the shell hook drops SORTIE_USAGE_FILE", !("SORTIE_USAGE_FILE" in shellEvent.env) && shellEvent.env.PATH === "/bin")

// The file is always whole: no temporary file left beside it.
check("the file is written atomically", fs.readdirSync(path.dirname(file)).every((f) => !f.endsWith(".tmp")))
cleanup()
check("cleanup leaves the totals", read().input_tokens === 200)
process.exit(failed ? 1 : 0)
