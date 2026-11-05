// A minimal ACP client for test-opencode-usage.sh: starts the agent
// command, opens one session in the current directory, sends one prompt,
// allows every permission request, and fails on anything but JSON-RPC on
// the agent's stdout (a plugin that writes to the console would break ACP).
//
//   node acp-client.mjs COMMAND [ARG...]
import { spawn } from "node:child_process"

const [command, ...args] = process.argv.slice(2)
const child = spawn(command, args, { stdio: ["pipe", "pipe", "inherit"] })
const waiting = new Map()
let buffer = ""
let next = 1

const send = (message) => child.stdin.write(JSON.stringify({ jsonrpc: "2.0", ...message }) + "\n")
const call = (method, params) =>
  new Promise((resolve, reject) => {
    const id = next++
    waiting.set(id, { resolve, reject })
    send({ id, method, params })
  })

child.stdout.on("data", (data) => {
  buffer += data
  let i
  while ((i = buffer.indexOf("\n")) >= 0) {
    const line = buffer.slice(0, i)
    buffer = buffer.slice(i + 1)
    if (!line.trim()) continue
    let m
    try {
      m = JSON.parse(line)
    } catch {
      console.error(`acp-client: not JSON-RPC on the agent's stdout: ${line}`)
      process.exitCode = 3
      continue
    }
    if (m.method && m.id !== undefined) {
      const allow = m.params?.options?.find((o) => String(o.kind).startsWith("allow"))
      send({ id: m.id, result: { outcome: allow ? { outcome: "selected", optionId: allow.optionId } : { outcome: "cancelled" } } })
    } else if (m.id !== undefined && waiting.has(m.id)) {
      const w = waiting.get(m.id)
      waiting.delete(m.id)
      if (m.error) w.reject(new Error(JSON.stringify(m.error)))
      else w.resolve(m.result)
    }
  }
})

const timer = setTimeout(() => {
  console.error("acp-client: timed out")
  child.kill()
  process.exit(2)
}, Number(process.env.ACP_TIMEOUT_MS) || 120_000)
try {
  await call("initialize", { protocolVersion: 1, clientCapabilities: {} })
  const session = await call("session/new", { cwd: process.cwd(), mcpServers: [] })
  const result = await call("session/prompt", { sessionId: session.sessionId, prompt: [{ type: "text", text: "Run the task." }] })
  console.log(`stop: ${result.stopReason}`)
} catch (e) {
  console.error(`acp-client: ${e.message}`)
  process.exitCode = 1
}
clearTimeout(timer)
child.stdin.end()
await new Promise((resolve) => {
  child.on("exit", resolve)
  setTimeout(() => {
    child.kill()
    resolve()
  }, 10_000).unref()
})
