// Test harness for the labeled fixture source. It is not live Makez and it
// is not a Philo route. The desktop talks to it only when PHILO_RESTART_URL
// is set, which production does not set.
//
// The fixture speaks the same task and event routes the server resume
// already reads. A control port kills the Philo process with SIGKILL and
// starts another on the same database.
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { randomBytes } from "node:crypto";
import { openSync } from "node:fs";
import { mkdir, readFile } from "node:fs/promises";

const token = "test-fixture-token";
const workspace = "test-fixture-source";
const duringText = "test-fixture-source:during-restart";
const afterText = "test-fixture-source:after-up";
const capturedTask = "ddeda2f7e3423d3e40d43f69a126afe7";
const dsn = process.env.PHILO_DATABASE_URL;
const bin = process.env.PHILO_BIN;
const logDir = process.env.PHILO_HARNESS_LOG_DIR;
const controlAddr = process.env.PHILO_HARNESS_ADDR || "127.0.0.1:9393";
const philoAddr = "127.0.0.1:8080";

if (!dsn || !bin || !logDir) {
  console.error("PHILO_DATABASE_URL, PHILO_BIN, and PHILO_HARNESS_LOG_DIR are required");
  process.exit(1);
}

const tasks = [];
const byKey = new Map();

function log(line) {
  const text = `${new Date().toISOString()} ${line}`;
  console.log(text);
}

function taskBody(task) {
  return JSON.stringify({
    id: task.id,
    workspace_id: workspace,
    agent_id: "pi",
    agent_version: "1",
    state: "RUNNING",
    config: { execution: { conversation_id: task.conversationId } },
    run: { id: task.runId, state: "RUNNING", cursor: "stdout:2,stderr:0" },
  });
}

function rowsFor(task) {
  return [
    { seq: 1, text: "test-fixture-source:seq:1" },
    { seq: 2, text: "test-fixture-source:seq:2" },
    ...task.extras,
  ];
}

function sse(task, after) {
  let body = "";
  for (const row of rowsFor(task)) {
    if (row.seq <= after) continue;
    const payload = JSON.stringify({
      seq: row.seq,
      run_id: task.runId,
      type: "log",
      payload: { stream: "stdout", text: row.text },
      created_at: "2026-11-10T12:00:00Z",
    });
    body += `id: ${row.seq}\nevent: log\ndata: ${payload}\n\n`;
  }
  return body;
}

function authorized(req, res) {
  if (req.headers.authorization !== `Bearer ${token}` || req.headers["x-makez-workspace-id"] !== workspace) {
    res.writeHead(401);
    res.end("unauthorized");
    return false;
  }
  return true;
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

const fixture = createServer(async (req, res) => {
  const url = new URL(req.url, "http://fixture.local");
  if (req.method === "POST" && url.pathname === "/v1/tasks") {
    if (!authorized(req, res)) return;
    const raw = await readBody(req);
    if (raw.includes(Buffer.from('"owner_id"'))) {
      res.writeHead(400);
      res.end("owner_id is not a field");
      return;
    }
    let body;
    try {
      body = JSON.parse(raw.toString("utf8"));
    } catch {
      res.writeHead(400);
      res.end("json");
      return;
    }
    const key = req.headers["idempotency-key"] || "";
    if (!key.startsWith("turn/") || !key.endsWith("/submit")) {
      res.writeHead(400);
      res.end("idempotency");
      return;
    }
    const existing = byKey.get(key);
    if (existing) {
      res.writeHead(202, { "content-type": "application/json", "x-test-source": workspace });
      res.end(taskBody(existing));
      return;
    }
    const conversationId = body?.config?.execution?.conversation_id || "";
    if (!conversationId || body.agent_id !== "pi" || body.agent_version !== "1") {
      res.writeHead(400);
      res.end("contract");
      return;
    }
    let id = "";
    do {
      id = randomBytes(16).toString("hex");
    } while (id === capturedTask);
    const task = {
      id,
      runId: randomBytes(16).toString("hex"),
      key,
      conversationId,
      extras: [],
    };
    tasks.push(task);
    byKey.set(key, task);
    log(`fixture submit task=${id} conversation=${conversationId} n=${tasks.length}`);
    res.writeHead(202, { "content-type": "application/json", "x-test-source": workspace });
    res.end(taskBody(task));
    return;
  }
  const events = url.pathname.match(/^\/v1\/tasks\/([^/]+)\/events$/);
  const got = url.pathname.match(/^\/v1\/tasks\/([^/]+)$/);
  if (req.method === "GET" && (events || got)) {
    if (!authorized(req, res)) return;
    const id = decodeURIComponent((events || got)[1]);
    const task = tasks.find((item) => item.id === id);
    if (!task) {
      res.writeHead(404);
      res.end("not found");
      return;
    }
    if (events) {
      if (url.searchParams.get("after")) {
        res.writeHead(400);
        res.end("after query is not the resume cursor");
        return;
      }
      const cursor = req.headers["last-event-id"] || "";
      task.cursors = task.cursors || [];
      task.cursors.push(cursor);
      log(`fixture events task=${id} cursor=${cursor === "" ? "empty" : cursor}`);
      const after = cursor === "" ? 0 : Number(cursor);
      if (!Number.isInteger(after) || after < 0) {
        res.writeHead(400);
        res.end("cursor");
        return;
      }
      res.writeHead(200, { "content-type": "text/event-stream", "x-test-source": workspace });
      res.end(sse(task, after));
      return;
    }
    res.writeHead(200, { "content-type": "application/json", "x-test-source": workspace });
    res.end(taskBody(task));
    return;
  }
  res.writeHead(404);
  res.end("not found");
});

await mkdir(logDir, { recursive: true });
await new Promise((resolve) => fixture.listen(0, "127.0.0.1", resolve));
const fixtureURL = `http://127.0.0.1:${fixture.address().port}`;
log(`fixture listening ${fixtureURL}`);

let child = null;
let mode = "bound";

function philoEnv(bound) {
  const env = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!key.startsWith("PHILO_")) env[key] = value;
  }
  env.PHILO_DATABASE_URL = dsn;
  env.PHILO_HTTP_ADDR = philoAddr;
  env.PHILO_AUTH_CREDENTIALS = "philo-test-actor|actor-test|owner-test";
  env.NO_PROXY = "127.0.0.1,localhost";
  if (bound) {
    env.PHILO_MAKEZ_API_URL = fixtureURL;
    env.PHILO_MAKEZ_API_TOKEN = token;
    env.PHILO_MAKEZ_WORKSPACE_ID = workspace;
    env.PHILO_MAKEZ_BINDING = "test-binding";
  }
  return env;
}

function startPhilo(bound) {
  if (child) throw new Error("a Philo process is already running");
  const logPath = `${logDir}/philo-${Date.now()}.log`;
  const fd = openSync(logPath, "a");
  const proc = spawn(bin, [], { env: philoEnv(bound), stdio: ["ignore", fd, fd] });
  proc.logPath = logPath;
  child = proc;
  mode = bound ? "bound" : "unset";
  log(`philo start pid=${proc.pid} mode=${mode} log=${logPath}`);
  proc.on("exit", (code, signal) => {
    log(`philo exit pid=${proc.pid} code=${code} signal=${signal}`);
    if (child === proc) child = null;
  });
  return proc;
}

function waitExit(proc) {
  if (proc.exitCode !== null || proc.signalCode) return Promise.resolve(proc.signalCode);
  return new Promise((resolve) => proc.once("exit", (_code, signal) => resolve(signal)));
}

async function killPhilo() {
  const proc = child;
  if (!proc) return { pid: 0, signal: "absent" };
  const pid = proc.pid;
  proc.kill("SIGKILL");
  const signal = await waitExit(proc);
  log(`kill pid=${pid} requested=SIGKILL wait=${signal}`);
  if (signal !== "SIGKILL") {
    throw new Error(`pid ${pid} ended with ${signal}, not SIGKILL`);
  }
  return { pid, signal };
}

function psql(sql) {
  return new Promise((resolve, reject) => {
    const proc = spawn("psql", [dsn, "-tAc", sql], { stdio: ["ignore", "pipe", "pipe"] });
    let out = "";
    let err = "";
    proc.stdout.on("data", (chunk) => {
      out += chunk;
    });
    proc.stderr.on("data", (chunk) => {
      err += chunk;
    });
    proc.on("exit", (code) => {
      if (code === 0) resolve(out.trim());
      else reject(new Error(err || `psql ${code}`));
    });
  });
}

async function sourceCommitted() {
  if (tasks.length === 0) return false;
  const ids = tasks.map((task) => `'${task.runId}'`).join(",");
  let text = "";
  try {
    text = await psql(
      `SELECT run_id || '=' || applied_public_seq FROM source_checkpoints WHERE run_id IN (${ids}) ORDER BY run_id`,
    );
  } catch (err) {
    log(`checkpoint read: ${err.message}`);
    return false;
  }
  const applied = new Map();
  for (const line of text.split("\n").filter(Boolean)) {
    const [runId, seq] = line.split("=");
    applied.set(runId, Number(seq));
  }
  return tasks.every((task) => (applied.get(task.runId) || 0) >= 2);
}

async function waitFor(label, ready) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    if (await ready()) return;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`${label} was not ready`);
}

async function healthOk() {
  try {
    const response = await fetch(`http://${philoAddr}/v1/health`);
    if (!response.ok) return false;
    const body = await response.json();
    return body.ok === true && body.adapter === "unavailable" && body.makez === "disconnected";
  } catch {
    return false;
  }
}

function send(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, { "content-type": "application/json" });
  res.end(raw);
}

function arm(name, text) {
  const task = tasks[0];
  if (!task) throw new Error("no linked task to arm");
  if (task.extras.some((row) => row.text === text)) return task;
  const seq = 2 + task.extras.length + 1;
  task.extras.push({ seq, text });
  log(`fixture arm ${name} task=${task.id} seq=${seq}`);
  return task;
}

const control = createServer(async (req, res) => {
  try {
    if (req.method === "GET" && req.url === "/status") {
      send(res, 200, {
        mode,
        pid: child?.pid || 0,
        tasks: tasks.length,
        fixture: fixtureURL,
        cursors: tasks.map((task) => task.cursors || []),
      });
      return;
    }
    if (req.method === "POST" && req.url === "/kill") {
      await waitFor("source commit", sourceCommitted);
      const killed = await killPhilo();
      send(res, 200, killed);
      return;
    }
    if (req.method === "POST" && req.url === "/start") {
      const proc = startPhilo(true);
      await waitFor("listen", healthOk);
      if (tasks[0]?.extras.some((row) => row.text === duringText)) {
        await waitFor("during log", async () => {
          const count = await psql(`SELECT count(*) FROM messages WHERE content = '${duringText}'`);
          return count === "1";
        });
      }
      send(res, 200, { pid: proc.pid, mode: "bound" });
      return;
    }
    if (req.method === "POST" && req.url === "/arm/during") {
      const task = arm("during", duringText);
      send(res, 200, { task: task.id, text: duringText });
      return;
    }
    if (req.method === "POST" && req.url === "/arm/after") {
      const task = arm("after", afterText);
      send(res, 200, { task: task.id, text: afterText });
      return;
    }
    if (req.method === "POST" && req.url === "/unset") {
      if (child) await killPhilo();
      const proc = startPhilo(false);
      await waitFor("unset listen", async () => {
        if (!(await healthOk())) return false;
        const text = await readFile(proc.logPath, "utf8");
        return text.includes("execution=unavailable") && !text.includes("startup resume");
      });
      send(res, 200, { pid: proc.pid, mode: "unset" });
      return;
    }
    send(res, 404, { error: "not found" });
  } catch (err) {
    log(`control ${req.url}: ${err.message}`);
    send(res, 500, { error: err.message });
  }
});

await new Promise((resolve) => control.listen(9393, "127.0.0.1", resolve));
log(`control listening http://${controlAddr}`);
const proc = startPhilo(true);
await waitFor("first listen", healthOk);
log(`ready pid=${proc.pid}`);
