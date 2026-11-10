// The desktop already shows fixture messages and holds a non-zero business
// cursor. Disconnect the socket, store one later fixture delta while it is
// down, then reconnect. The client catches that suffix up once.
//
// This is not the empty-transcript after=0 check in expired-cursor.test.tsx.
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// PHILO_BUSINESS_FIXTURE=fixture-business-source is the labeled fixture.
// It is not a model reply and not Makez.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor and
// PHILO_FIXTURE_SUFFIX=1. The server accepts that local actor token.
// It is not a model key.
//
// Before this test, on a server at http://127.0.0.1:8080:
// 1. Create a bot named Wynn and open its conversation.
// 2. Post fixture-business-source events that materialize the text pinecone:
//    fixture.seed, message.delta "pinecone", message.completed.
// Do not delete those events. The stored business cursor is not 0.
//
// Run: nsh test reconnect-cursor.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 800));
const heldText = "pinecone";
const suffixText = "suffixonce";
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

test("reconnect catches up a non-zero business cursor once", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await app.waitFor("Wynn", { timeout: 15000 });
  await app.press("Wynn");
  await app.waitFor(heldText, { timeout: 15000 });
  await app.waitFor(fixtureAdapter, { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  await pause();

  const held = cursorLine(app);
  expect(held).toBe("Business cursor: 3");
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(suffixText);
  expect(visible(app)).not.toContain(rebuilt);

  await app.press("Disconnect");
  await app.waitFor("Events: disconnected", { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(held);
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(suffixText);

  await app.press("Fixture suffix");
  await app.waitFor("Fixture suffix posted", { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(held);
  expect(visible(app)).not.toContain(suffixText);
  expect(visible(app)).toContain(heldText);

  await app.press("Reconnect");
  await app.waitFor(`${heldText}${suffixText}`, { timeout: 15000 });
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 4", { timeout: 15000 });
  await pause();

  let text = visible(app);
  // The bot row repeats the latest line. The transcript itself is one bubble.
  expect(app.texts().filter((line) => line === `${heldText}${suffixText}`).length).toBe(2);
  expect(text).not.toContain(`${heldText}${suffixText}${suffixText}`);
  expect(text).not.toContain(`${suffixText}${suffixText}`);
  expect(text).not.toContain(`${heldText}${heldText}`);
  await app.waitFor("Business cursor: 4", { timeout: 15000 });
  const next = cursorLine(app);
  expect(next).toBe("Business cursor: 4");
  expect(next).not.toBe(held);
  expect(count(text, fixtureAdapter)).toBe(1);
  expect(text).toContain("Adapter: unavailable");
  expect(text).toContain("Makez is not connected.");
  expect(text).not.toContain(rebuilt);
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");

  await pause();
  text = visible(app);
  expect(app.texts().filter((line) => line === `${heldText}${suffixText}`).length).toBe(2);
  expect(text).not.toContain(`${heldText}${suffixText}${suffixText}`);
  expect(text).not.toContain(`${suffixText}${suffixText}`);
}, 60000);
