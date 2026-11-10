// The open chat is already showing a pending card. Another API client edits
// that action. The window follows the stored cards: the old card is invalid,
// the new card shows the new snapshot, and Reload is not used. The test does
// not inject a status. PHILO_TEST_EDIT_SUMMARY stays unset, so this process
// does not post the edit itself.
//
// Start `node approval-reconnect-edit.mjs` first. That process is the other
// client. It waits until this chat has shown the pending card, then posts
// one edit. Approve is pressed while the first card is still pending, so a
// decision is in flight when the edit lands. A late or failed decision must
// not leave the old card approved or approvable. Switching to another bot
// and back, then disconnecting and reconnecting, shows the stored status.
// The old snapshot does not become approvable again.
//
// PHILO_EXECUTION_FIXTURE=dev on http://127.0.0.1:8080. That fixture is not
// live Makez. Leave PHILO_MAKEZ_URL and PHILO_TEST_EDIT_SUMMARY unset. The
// desktop process needs PHILO_ACTOR_TOKEN. The bot names must not already
// be in the list.
//
// nsh test approval-reconnect.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 400));
const first = "send the weekly note";
const revised = "send the revised note";

function exact(app: { texts: () => string[] }, line: string) {
  return app.texts().filter((item) => item === line).length;
}

function storedCards(app: { texts: () => string[] }) {
  expect(exact(app, `Snapshot: ${first}`)).toBe(1);
  expect(exact(app, `Snapshot: ${revised}`)).toBe(1);
  expect(exact(app, "Status: invalid")).toBe(1);
  expect(exact(app, "Status: pending")).toBe(1);
  expect(exact(app, "Status: approved")).toBe(0);
  expect(exact(app, "Approve")).toBe(1);
  expect(app.texts().join("\n")).not.toContain("Configure the action");
}

test("an external edit updates the open card across switch and reconnect", async () => {
  if (process.env.PHILO_TEST_EDIT_SUMMARY) {
    throw new Error("PHILO_TEST_EDIT_SUMMARY must be unset");
  }
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Development adapter. This is not a live model.", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Cara");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Nia");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });

  await app.press("Cara");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });

  await app.type("Message", `/draft ${first}`);
  await app.press("Send");
  await app.waitFor(`Snapshot: ${first}`, { timeout: 15000 });
  await app.waitFor("Status: pending", { timeout: 15000 });
  await app.waitFor("Approve", { timeout: 15000 });
  await pause();
  expect(exact(app, `Snapshot: ${first}`)).toBe(1);
  expect(exact(app, "Status: pending")).toBe(1);
  expect(exact(app, "Approve")).toBe(1);
  expect(exact(app, "Status: approved")).toBe(0);
  expect(exact(app, "Status: invalid")).toBe(0);

  await app.press("Approve");

  await app.waitFor(`Snapshot: ${revised}`, { timeout: 15000 });
  await app.waitFor("Status: invalid", { timeout: 15000 });
  await pause();
  storedCards(app);
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
  expect(app.texts().join("\n")).toContain("Development adapter. This is not a live model.");

  await app.press("Nia");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await pause();
  expect(exact(app, `Snapshot: ${first}`)).toBe(0);
  expect(exact(app, `Snapshot: ${revised}`)).toBe(0);
  expect(exact(app, "Approve")).toBe(0);
  expect(exact(app, "Status: approved")).toBe(0);

  await app.press("Cara");
  await app.waitFor(`Snapshot: ${revised}`, { timeout: 15000 });
  await app.waitFor("Status: invalid", { timeout: 15000 });
  await pause();
  storedCards(app);

  await app.press("Disconnect");
  await app.waitFor("Events: disconnected", { timeout: 15000 });
  await pause();
  storedCards(app);

  await app.press("Reconnect");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor(`Snapshot: ${revised}`, { timeout: 15000 });
  await app.waitFor("Status: invalid", { timeout: 15000 });
  await pause();
  storedCards(app);
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
  expect(app.texts().join("\n")).not.toContain("Configure the action");
}, 180000);
