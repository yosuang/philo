// A subscribed Reload whose business-events answers HTTP 500 must not stay
// on Events: connected, and must not retry that GET in a tight loop.
// The wait is capped exponential backoff: 1s, then 2s, then 4s, up to 20s.
// Disconnect and switching to another bot stop the retry. Releasing the
// failure lets this chat catch up again without moving the business cursor.
//
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// PHILO_BUSINESS_FIXTURE=fixture-business-source is the labeled fixture.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor and
// PHILO_FIXTURE_SUFFIX=1. The token is the local test credential, not a
// model key.
//
// Before this test, on a server at http://127.0.0.1:8080:
// 1. Create bots Reed and Lark and open each conversation.
// 2. Post fixture-business-source events that materialize reed and lark:
//    fixture.seed, message.delta, message.completed.
// Do not arm the read failure before the window is connected.
//
// Run: nsh test read-backoff.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const heldText = "reed";
const otherText = "lark";

function lineStarting(app: { texts: () => string[] }, prefix: string) {
  return app.texts().find((line) => line.startsWith(prefix)) ?? "";
}

function readCount(app: { texts: () => string[] }) {
  const line = lineStarting(app, "Business reads:");
  const value = Number(line.slice("Business reads:".length).trim());
  if (!Number.isFinite(value)) {
    throw new Error(`missing business read count: ${line}`);
  }
  return value;
}

test("subscribed HTTP 500 backs off and stops when the chat is left", async () => {
  const app = await open(".", { network: "live", width: 1100, height: 1100, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await app.waitFor("Reed", { timeout: 15000 });
  await app.press("Reed");
  await app.waitFor(heldText, { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  await app.waitFor("Message seq: 1", { timeout: 15000 });
  await app.waitFor("Business reads: 2", { timeout: 15000 });
  await pause(400);

  const cursor = lineStarting(app, "Business cursor:");
  const seq = lineStarting(app, "Message seq:");
  expect(cursor).toBe("Business cursor: 3");
  expect(seq).toBe("Message seq: 1");
  expect(cursor).not.toBe(seq);

  await app.press("Fail reads");
  await app.waitFor("Reads failing", { timeout: 15000 });
  await app.press("Reload");
  await app.waitFor("Events: subscribe failed", { timeout: 15000 });
  expect(lineStarting(app, "Events:")).not.toBe("Events: connected");
  expect(lineStarting(app, "Events:")).not.toBe("Events: recovered");

  const failedAt = readCount(app);
  await pause(300);
  const burst = readCount(app) - failedAt;
  expect(burst).toBeLessThanOrEqual(1);
  await pause(1500);
  const duringFailure = readCount(app) - failedAt;
  // The first retry waits 1s. The one after that waits 2s more, so it is
  // still in the future at 1.8s. The unbound loop issued about 25 GETs a second.
  expect(duringFailure).toBeGreaterThanOrEqual(1);
  expect(duringFailure).toBeLessThanOrEqual(2);
  expect(lineStarting(app, "Events:")).toBe("Events: subscribe failed");
  expect(lineStarting(app, "Business cursor:")).toBe(cursor);

  await app.press("Disconnect");
  await app.waitFor("Events: disconnected", { timeout: 15000 });
  const disconnected = readCount(app);
  await pause(2500);
  expect(readCount(app)).toBe(disconnected);
  expect(lineStarting(app, "Events:")).toBe("Events: disconnected");

  await app.press("Reconnect");
  await app.waitFor("Events: subscribe failed", { timeout: 15000 });
  await app.press("Lark");
  await app.waitFor(otherText, { timeout: 15000 });
  await app.waitFor("Events: connected", { timeout: 15000 });
  await pause(400);
  const afterSwitch = readCount(app);
  await pause(2500);
  expect(readCount(app)).toBe(afterSwitch);
  expect(lineStarting(app, "Events:")).toBe("Events: connected");
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");

  await app.press("Reed");
  await app.waitFor(heldText, { timeout: 15000 });
  await app.waitFor("Events: subscribe failed", { timeout: 15000 });
  await app.press("Release reads");
  await app.waitFor("Reads released", { timeout: 15000 });
  await app.waitFor("Events: recovered", { timeout: 15000 });
  await pause(2000);
  expect(lineStarting(app, "Events:")).toBe("Events: recovered");
  expect(lineStarting(app, "Business cursor:")).toBe(cursor);
  expect(lineStarting(app, "Message seq:")).toBe(seq);
  expect(app.texts().filter((line) => line === heldText).length).toBeGreaterThan(0);
  expect(app.texts().join("\n")).not.toContain(`${heldText}${heldText}`);
  expect(app.texts().join("\n")).toContain("Adapter: unavailable");
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
  const settled = readCount(app);
  await pause(2000);
  expect(readCount(app)).toBe(settled);
}, 90000);
