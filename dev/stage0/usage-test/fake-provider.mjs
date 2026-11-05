// An OpenAI-compatible chat endpoint for test-opencode-usage.sh. The main
// session runs one shell command (env, into env.txt), then one subagent;
// the subagent and title requests answer with text. Every response reports
// the same usage, so the run's totals are a multiple of it. It writes the
// port it listens on to FAKE_PORT and one line per served request to
// FAKE_LOG.
import fs from "node:fs"
import http from "node:http"

export const USAGE = { prompt_tokens: 1000, completion_tokens: 70, total_tokens: 1070, prompt_tokens_details: { cached_tokens: 200 } }

const text = (m) => (typeof m?.content === "string" ? m.content : JSON.stringify(m?.content ?? ""))

function reply(body) {
  const messages = body.messages ?? []
  const system = text(messages.find((m) => m.role === "system"))
  const last = messages.at(-1)
  if (/name conversations/i.test(system)) return { kind: "title", text: "A title" }
  if (last?.role === "tool") {
    const calls = messages.flatMap((m) => m.tool_calls ?? [])
    const call = calls.find((c) => c.id === last.tool_call_id)
    if (call?.function?.name === "shell")
      return { kind: "main", tool: "subagent", args: { agent: "general", description: "Child task", prompt: "child task: answer ok" } }
    return { kind: "main", text: "done" }
  }
  if (messages.some((m) => m.role === "user" && text(m).includes("child task"))) return { kind: "child", text: "ok" }
  return { kind: "main", tool: "shell", args: { command: "env > env.txt", description: "Dump the environment" } }
}

let served = 0
const server = http.createServer((req, res) => {
  let raw = ""
  req.on("data", (c) => (raw += c))
  req.on("end", () => {
    if (!req.url.endsWith("/chat/completions")) {
      res.writeHead(404)
      return res.end()
    }
    const r = reply(JSON.parse(raw))
    served++
    fs.appendFileSync(process.env.FAKE_LOG, `${r.kind} ${r.tool ?? "text"}\n`)
    const id = `chatcmpl-${served}`
    const chunk = (choices, extra = {}) =>
      res.write(`data: ${JSON.stringify({ id, object: "chat.completion.chunk", created: 1, model: "fake-model", choices, ...extra })}\n\n`)
    res.writeHead(200, { "content-type": "text/event-stream" })
    if (r.tool) {
      chunk([{ index: 0, delta: { role: "assistant", tool_calls: [{ index: 0, id: `call_${served}`, type: "function", function: { name: r.tool, arguments: JSON.stringify(r.args) } }] } }])
      chunk([{ index: 0, delta: {}, finish_reason: "tool_calls" }])
    } else {
      chunk([{ index: 0, delta: { role: "assistant", content: r.text } }])
      chunk([{ index: 0, delta: {}, finish_reason: "stop" }])
    }
    chunk([], { usage: USAGE })
    res.end("data: [DONE]\n\n")
  })
})
server.listen(0, "127.0.0.1", () => fs.writeFileSync(process.env.FAKE_PORT, String(server.address().port)))
