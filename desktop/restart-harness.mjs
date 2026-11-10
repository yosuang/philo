// Test harness for the labeled fixture source. It is not live Makez and it
// is not a Philo route. The desktop talks to it only when PHILO_RESTART_URL
// is set, which production does not set.
//
// The fixture speaks the same task and event routes the server resume
// already reads. A control port kills the Philo process with SIGKILL and
// starts another on the same database.
//
// PHILO_PUBLISH_CRASH=1 changes Start server after the during log is armed.
// That start runs a process until the resumed row is committed and the
// publish line is reached, then SIGKILLs it before h.Publish and starts
// another process on the same database. Delve stops on that source line.
// The server binary has no pause flag. Leave the variable unset for the
// other restart test.
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { randomBytes } from "node:crypto";
import { createConnection } from "node:net";
import { openSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

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
const killBeforePublish = process.env.PHILO_PUBLISH_CRASH === "1";

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

function dlvClient(socket) {
  let buf = Buffer.alloc(0);
  const pending = [];
  socket.on("data", (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    let nl = buf.indexOf(10);
    while (nl >= 0) {
      const raw = buf.slice(0, nl).toString("utf8");
      buf = buf.slice(nl + 1);
      nl = buf.indexOf(10);
      if (!raw.trim()) continue;
      const msg = JSON.parse(raw);
      const waiter = pending.shift();
      if (!waiter) continue;
      if (msg.error) waiter.reject(new Error(JSON.stringify(msg.error)));
      else waiter.resolve(msg.result);
    }
  });
  return (method, params) =>
    new Promise((resolve, reject) => {
      pending.push({ resolve, reject });
      socket.write(JSON.stringify({ method, params: [params], id: pending.length }) + "\n");
    });
}

function processState(pid) {
  return readFile(`/proc/${pid}/status`, "utf8").catch(() => "");
}

// startKilledBeforePublish commits the armed during log, stops on the
// h.Publish line, and SIGKILLs that process before the call runs. The next
// process reads the stored public seq and does not insert the log again.
async function startKilledBeforePublish() {
  const dlvBin = process.env.PHILO_DLV || "dlv";
  const publishFile = fileURLToPath(new URL("../server/internal/httpapi/httpapi.go", import.meta.url));
  const source = await readFile(publishFile, "utf8");
  const publishLine = source.split("\n").findIndex((line) => line.includes("h.Publish(turn.Binding.ConversationID, item)")) + 1;
  if (publishLine < 1) throw new Error("publish line not found");
  const crashedLog = `${logDir}/philo-before-publish.log`;
  const dlvLog = `${logDir}/dlv.log`;
  const dlvProc = spawn(
    dlvBin,
    [
      "exec",
      bin,
      "--headless",
      "--api-version=2",
      "--accept-multiclient",
      "--check-go-version=false",
      "--listen=127.0.0.1:0",
      "--log",
      `--log-dest=${dlvLog}`,
      `-r=stdout:${crashedLog}`,
      `-r=stderr:${crashedLog}`,
    ],
    { env: philoEnv(true), stdio: ["ignore", "ignore", "ignore"] },
  );
  log(`dlv start pid=${dlvProc.pid} line=${publishLine}`);
  let socket;
  try {
    const apiAddr = await waitFor("dlv listen", async () => {
      const text = await readFile(dlvLog, "utf8").catch(() => "");
      const match = text.match(/API server listening at: (\S+)/);
      return match ? match[1] : "";
    }).then(async () => {
      const text = await readFile(dlvLog, "utf8");
      return text.match(/API server listening at: (\S+)/)[1];
    });
    socket = await new Promise((resolve, reject) => {
      const [host, port] = apiAddr.split(":");
      const conn = createConnection({ host, port: Number(port) });
      conn.once("error", reject);
      conn.once("connect", () => resolve(conn));
    });
    const call = dlvClient(socket);
    const created = await call("RPCServer.CreateBreakpoint", {
      Breakpoint: { name: "beforePublish", file: publishFile, line: publishLine },
    });
    if (!created.Breakpoint?.addrs?.length) throw new Error("publish breakpoint has no address");
    log(`breakpoint file=${created.Breakpoint.file} line=${created.Breakpoint.line}`);
    const continued = call("RPCServer.Command", { name: "continue" });
    continued.catch(() => {});
    const stopped = await Promise.race([
      continued,
      new Promise((_, reject) => setTimeout(() => reject(new Error("publish line was not reached")), 15000)),
    ]);
    const pid = stopped.State?.Pid;
    const goroutineID = stopped.State?.currentGoroutine?.id;
    if (!pid || !goroutineID) {
      await writeFile(`${logDir}/stopped-state.json`, JSON.stringify(stopped.State ?? stopped, null, 2));
      throw new Error("stopped process has no pid or goroutine");
    }
    const traced = await call("RPCServer.Stacktrace", { Id: goroutineID, Depth: 8, Full: false });
    const stack = (traced.Locations || []).map((frame) => `${frame.function?.name || "?"} ${frame.file}:${frame.line}`);
    await writeFile(`${logDir}/publish-crash-stack.txt`, stack.join("\n") + "\n");
    const crashedText = await readFile(crashedLog, "utf8").catch(() => "");
    if (crashedText.includes("startup resume published")) {
      throw new Error("h.Publish already ran");
    }
    const committed = await psql(`SELECT count(*) FROM messages WHERE content = '${duringText}'`);
    if (committed !== "1") throw new Error(`during rows at the publish line = ${committed}`);
    const db = await psql(`
      SELECT 'msg|' || m.role || '|' || m.content || '|' || coalesce(m.turn_id::text, '')
      FROM messages m ORDER BY m.created_at, m.id;
      SELECT 'ev|' || cursor || '|' || event_type || '|' || conversation_id FROM business_events ORDER BY conversation_id, cursor;
      SELECT 'ck|' || applied_public_seq || '|' || task_id FROM source_checkpoints ORDER BY task_id;
      SELECT 'outbox|' || kind || '|' || count(*) FROM outbox GROUP BY kind ORDER BY kind;
      SELECT 'users|' || count(*) FROM messages WHERE role = 'user';
    `);
    await writeFile(`${logDir}/publish-crash-db.txt`, db + "\n");
    process.kill(pid, "SIGKILL");
    log(`kill pid=${pid} requested=SIGKILL phase=before-publish`);
    await waitFor("killed process", async () => {
      const status = await processState(pid);
      return status === "" || status.includes("State:\tZ");
    });
    const status = await processState(pid);
    log(`killed pid=${pid} state=${status.includes("State:\tZ") ? "zombie" : "gone"}`);
    return pid;
  } finally {
    socket?.end();
    if (dlvProc.exitCode === null && !dlvProc.signalCode) dlvProc.kill("SIGKILL");
  }
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
      let killedPid = 0;
      if (killBeforePublish && tasks[0]?.extras.some((row) => row.text === duringText)) {
        killedPid = await startKilledBeforePublish();
      }
      const proc = startPhilo(true);
      await waitFor("listen", healthOk);
      if (tasks[0]?.extras.some((row) => row.text === duringText)) {
        await waitFor("during log", async () => {
          const count = await psql(`SELECT count(*) FROM messages WHERE content = '${duringText}'`);
          return count === "1";
        });
        if (killedPid) {
          await waitFor("successor passed the stored seq", async () => {
            const text = await readFile(proc.logPath, "utf8");
            return text.includes("after=3");
          });
          const count = await psql(`SELECT count(*) FROM messages WHERE content = '${duringText}'`);
          if (count !== "1") throw new Error(`successor changed during rows to ${count}`);
          const text = await readFile(proc.logPath, "utf8");
          if (text.includes("startup resume published")) {
            throw new Error("successor published the log that was already committed");
          }
          const users = await psql(`SELECT count(*) FROM messages WHERE role = 'user'`);
          if (users !== "2") throw new Error(`user rows = ${users}`);
        }
      }
      send(res, 200, { pid: proc.pid, killedPid, mode: "bound" });
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
