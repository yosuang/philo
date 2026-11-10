// The desktop already shows fixture messages and holds a non-zero business
// cursor. Disconnect the socket. Stage the labeled fixture so that cursor is
// expired. Reconnect. The server returns HTTP 400 with a replacement snapshot.
// The client replaces the transcript from that snapshot once, then follows
// the suffix without duplicating the snapshot text.
//
// This is not the empty-transcript after=0 check in expired-cursor.test.tsx.
// This is not the unexpired after=3 suffix in reconnect-cursor.test.tsx.
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// PHILO_BUSINESS_FIXTURE=fixture-business-source is the labeled fixture.
// It is not a model reply and not Makez.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor and
// PHILO_FIXTURE_SUFFIX=1. The server accepts that local actor token.
// It is not a model key.
//
// Before this test, on a server at http://127.0.0.1:8080:
// 1. Create a bot named Lark and open its conversation.
// 2. Post fixture-business-source events that materialize the text cedar:
//    fixture.seed, message.delta "cedar", message.completed.
// Do not delete those events. The stored business cursor is not 0.
// Do not stage the expiry before the window is holding that cursor.
//
// Run: nsh test expired-nonzero-cursor.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 800));
const heldText = "cedar";
const snapshotText = "lantern";
const shown = "lanternonce";
const fixtureAdapter = "fixture-business-source adapter";
const rebuilt = "Business cursor expired. This chat was rebuilt from the snapshot.";

function visible(app: { texts: () => string[] }) {
  return app.texts().join("\n");
}

function count(text: string, needle: string) {
  return text.split(needle).length - 1;
}

function cursorLine(app: { texts: () => string[] }) {
  return app.texts().find((line) => line.startsWith("Business cursor:")) ?? "";
}

test("expired non-zero cursor replaces the snapshot once then follows the suffix", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await app.waitFor("Lark", { timeout: 15000 });
  await app.press("Lark");
  await app.waitFor(heldText, { timeout: 15000 });
  await app.waitFor(fixtureAdapter, { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  await pause();

  const held = cursorLine(app);
  expect(held).toBe("Business cursor: 3");
  expect(held).not.toBe("Business cursor: 1");
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(snapshotText);
  expect(visible(app)).not.toContain(rebuilt);

  await app.press("Disconnect");
  await app.waitFor("Events: disconnected", { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(held);
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(snapshotText);

  await app.press("Stage expired cursor");
  await app.waitFor("Expired cursor staged", { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(held);
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(snapshotText);
  expect(visible(app)).not.toContain(rebuilt);

  await app.press("Reconnect");
  await app.waitFor(shown, { timeout: 15000 });
  await app.waitFor(rebuilt, { timeout: 15000 });
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 5", { timeout: 15000 });
  await pause();

  let text = visible(app);
  expect(app.texts().filter((line) => line === shown).length).toBe(2);
  expect(text).not.toContain(heldText);
  expect(text).not.toContain(`${shown}${shown}`);
  expect(text).not.toContain("lanternlantern");
  expect(text).not.toContain("onceonce");
  expect(text).not.toContain(`${snapshotText}${snapshotText}`);
  expect(count(text, rebuilt)).toBe(1);
  expect(count(text, fixtureAdapter)).toBe(1);
  const next = cursorLine(app);
  expect(next).toBe("Business cursor: 5");
  expect(next).not.toBe(held);
  expect(next).not.toBe("Business cursor: 1");
  expect(next).not.toBe("Business cursor: 4");
  expect(text).toContain("Adapter: unavailable");
  expect(text).toContain("Makez is not connected.");
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");

  await pause();
  text = visible(app);
  expect(app.texts().filter((line) => line === shown).length).toBe(2);
  expect(text).not.toContain(heldText);
  expect(text).not.toContain("lanternlantern");
  expect(text).not.toContain("onceonce");
  expect(count(text, rebuilt)).toBe(1);
}, 60000);
