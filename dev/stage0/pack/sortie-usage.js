// The token usage of one agent-acp run, for the worker daemon's usage
// command (agent-usage). An opencode server plugin, installed into the
// agent user's ~/.config/opencode/plugins/ by install-harness.sh.
//
// agent-acp sets SORTIE_USAGE_FILE before it starts opencode. At setup the
// plugin takes the path out of the environment, so no opencode the agent
// starts in a shell writes the file too, and writes zero totals there.
// Then it sums, over every session of this opencode process (subagents'
// child sessions too), the tokens used since it started, and rewrites the
// file at each usage event and on a timer. A session created since the
// start counts its whole total (session.usage.updated, Session.Info.tokens:
// its steps, title and compactions); an older one that the run resumed
// counts only the steps, titles and compactions it ran since. It never
// fails the agent: an error goes to SORTIE_USAGE_FILE.log, never to the
// console, which may be ACP's stdout.
import fs from "node:fs"
import path from "node:path"

const STATE = Symbol.for("sortie.usage")
const RECONCILE_MS = Number(process.env.SORTIE_USAGE_RECONCILE_MS) || 10_000
const LOG_LIMIT = 1 << 20

const zero = () => ({ input: 0, output: 0, reasoning: 0, cache: { read: 0, write: 0 } })
const num = (n) => (typeof n === "number" && Number.isFinite(n) && n > 0 ? n : 0)
const add = (a, b) => ({
  input: a.input + num(b?.input),
  output: a.output + num(b?.output),
  reasoning: a.reasoning + num(b?.reasoning),
  cache: { read: a.cache.read + num(b?.cache?.read), write: a.cache.write + num(b?.cache?.write) },
})
const size = (t) => t.input + t.output + t.reasoning + t.cache.read + t.cache.write
// A Model.Ref or Model.Info's ID.
const modelOf = (m) => (typeof m === "string" ? m : typeof m?.id === "string" ? m.id : "")
const message = (e) => (e && typeof e.message === "string" ? e.message : String(e))

function tracker(file, start) {
  // sessionID -> { fresh, total, steps, model }. fresh is undefined until
  // session.get says when the session was created; until then it counts as
  // fresh, as almost every session is.
  const sessions = new Map()

  const log = (text) => {
    try {
      const target = `${file}.log`
      const st = fs.statSync(target, { throwIfNoEntry: false })
      if (st && st.size > LOG_LIMIT) return
      fs.appendFileSync(target, `${new Date().toISOString()} ${text}\n`)
    } catch {}
  }

  const snapshot = () => {
    let sum = zero()
    let model = ""
    let biggest = 0
    for (const s of sessions.values()) {
      const used = s.fresh === false ? s.steps : s.total
      sum = add(sum, used)
      if (s.model && size(used) >= biggest) {
        biggest = size(used)
        model = s.model
      }
    }
    return {
      input_tokens: sum.input,
      output_tokens: sum.output + sum.reasoning,
      cache_read_tokens: sum.cache.read,
      cache_write_tokens: sum.cache.write,
      model,
      sessions: sessions.size,
      started_at: new Date(start).toISOString(),
      updated_at: new Date().toISOString(),
    }
  }

  const write = () => {
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true })
      const tmp = `${file}.${process.pid}.tmp`
      fs.writeFileSync(tmp, JSON.stringify(snapshot()) + "\n")
      fs.renameSync(tmp, file)
    } catch (e) {
      log(`write: ${message(e)}`)
    }
  }

  const learn = (ctx, id, info) => {
    if (info) {
      const s = sessions.get(id)
      if (s.fresh === undefined) {
        const created = info.time?.created
        s.fresh = typeof created !== "number" || created >= start
      }
      if (s.fresh && info.tokens) {
        const total = add(zero(), info.tokens)
        if (size(total) >= size(s.total)) s.total = total
      }
      s.model = modelOf(info.model) || s.model
      return
    }
    ctx.session
      .get({ sessionID: id })
      .then((got) => {
        learn(ctx, id, got)
        write()
      })
      .catch((e) => log(`session.get ${id}: ${message(e)}`))
  }

  const entry = (ctx, id) => {
    let s = sessions.get(id)
    if (!s) {
      s = { fresh: undefined, total: zero(), steps: zero(), model: "" }
      sessions.set(id, s)
      learn(ctx, id)
    }
    return s
  }

  const onEvent = (ctx, event) => {
    const data = event?.data
    const id = data?.sessionID
    if (typeof id !== "string") return
    switch (event.type) {
      case "session.created":
        entry(ctx, id)
        break
      case "session.usage.updated":
        entry(ctx, id).total = add(zero(), data.tokens)
        break
      case "session.step.ended":
      case "session.usage.recorded": {
        const s = entry(ctx, id)
        s.steps = add(s.steps, data.tokens)
        break
      }
      case "session.step.started":
      case "session.model.selected": {
        const s = entry(ctx, id)
        s.model = modelOf(data.model) || s.model
        break
      }
      default:
        return
    }
    write()
  }

  // The session totals, read again, in case the event stream missed one.
  const reconcile = async (ctx) => {
    for (const id of sessions.keys()) {
      try {
        learn(ctx, id, await ctx.session.get({ sessionID: id }))
      } catch (e) {
        log(`reconcile ${id}: ${message(e)}`)
      }
    }
    write()
  }

  return { log, write, onEvent, reconcile }
}

export default {
  id: "sortie.usage",
  setup(ctx) {
    // One tracker per process, whatever the number of locations that load it.
    if (globalThis[STATE]) return
    const file = process.env.SORTIE_USAGE_FILE
    if (!file) return
    delete process.env.SORTIE_USAGE_FILE
    const t = tracker(file, Date.now())
    globalThis[STATE] = t
    const abort = new AbortController()
    let timer
    try {
      t.write()
      ;(async () => {
        let delay = 500
        while (!abort.signal.aborted) {
          try {
            for await (const event of ctx.event.subscribe({ signal: abort.signal })) {
              delay = 500
              try {
                t.onEvent(ctx, event)
              } catch (e) {
                t.log(`event ${event?.type}: ${message(e)}`)
              }
            }
          } catch (e) {
            if (abort.signal.aborted) break
            t.log(`event stream: ${message(e)}`)
          }
          if (abort.signal.aborted) break
          await new Promise((r) => setTimeout(r, delay))
          delay = Math.min(delay * 2, 30_000)
        }
      })().catch((e) => t.log(`events: ${message(e)}`))
      timer = setInterval(() => void t.reconcile(ctx).catch((e) => t.log(`reconcile: ${message(e)}`)), RECONCILE_MS)
      timer.unref?.()
      // No shell the agent runs gets the path, whatever opencode passes it.
      Promise.resolve()
        .then(() =>
          ctx.shell?.hook?.("create.before", (event) => {
            if (event?.env) delete event.env.SORTIE_USAGE_FILE
          }),
        )
        .catch((e) => t.log(`shell hook: ${message(e)}`))
    } catch (e) {
      t.log(`setup: ${message(e)}`)
    }
    return () => {
      abort.abort()
      clearInterval(timer)
      t.write()
    }
  },
}
