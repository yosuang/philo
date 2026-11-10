// The desktop is already showing one bot's chat. The labeled fixture source
// is committed. This test kills the Philo process with SIGKILL, starts
// another on the same database, and the open client catches up by itself.
// It does not press Reconnect and it does not send the message again.
//
// PHILO_RESTART_URL points at desktop/restart-harness.mjs. That harness is
// the existing test-fixture-source wire, not live Makez. Production does not
// set the URL, so the restart controls are not in the shipping window.
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor.
//
// Run, with the harness already listening:
// nsh test client-restart.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 800));
const wynnText = "Remember the cedar";
const niaText = "Nia keeps her note";
const duringText = "test-fixture-source:during-restart";
const afterText = "test-fixture-source:after-up";
const plainText = "A plain note";

function visible(app: { texts: () => string[] }) {
  return app.texts().join("\n");
}

function count(text: string, needle: string) {
  return text.split(needle).length - 1;
}

function cursorLine(app: { texts: () => string[] }) {
  return app.texts().find((line) => line.startsWith("Business cursor:")) ?? "";
}

function expectNoModelReply(app: { texts: () => string[] }) {
  const text = visible(app);
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");
}

test("the open client catches up after the server process is killed", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Wynn");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", wynnText);
  await app.press("Send");
  await app.waitFor(wynnText, { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 1", { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe("Business cursor: 1");
  expect(visible(app)).not.toContain(duringText);
  expect(visible(app)).not.toContain(afterText);

  await app.press("New bot");
  await app.type("Name", "Nia");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", niaText);
  await app.press("Send");
  await app.waitFor(niaText, { timeout: 15000 });
  await app.waitFor("Business cursor: 1", { timeout: 15000 });
  await pause();
  const niaCursor = cursorLine(app);
  expect(niaCursor).toBe("Business cursor: 1");

  await app.press("Wynn");
  await app.waitFor(wynnText, { timeout: 15000 });
  await app.waitFor("Business cursor: 1", { timeout: 15000 });
  await app.waitFor("Kill server", { timeout: 15000 });

  await app.press("Kill server");
  await app.waitFor("Server killed", { timeout: 20000 });
  await app.press("Arm during log");
  await app.waitFor("During log armed", { timeout: 15000 });
  await app.press("Start server");
  await app.waitFor("Server started", { timeout: 20000 });
  await app.waitFor(`text:${duringText}`, { timeout: 20000 });
  await app.waitFor("Business cursor: 2", { timeout: 15000 });
  await app.waitFor("Events: connected", { timeout: 15000 });
  await pause();

  let text = visible(app);
  expect(text).toContain(wynnText);
  expect(text).toContain(duringText);
  expect(text).not.toContain(afterText);
  expect(text).not.toContain(`${duringText}${duringText}`);
  expect(cursorLine(app)).toBe("Business cursor: 2");
  expectNoModelReply(app);

  await app.press("Arm after log");
  await app.waitFor("After log armed", { timeout: 15000 });
  await app.waitFor(`text:${afterText}`, { timeout: 20000 });
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  await pause();

  text = visible(app);
  // The bot row repeats the latest line. Each log is one transcript bubble.
  expect(app.texts().filter((line) => line === duringText).length).toBe(1);
  expect(app.texts().filter((line) => line === afterText).length).toBe(2);
  expect(text).not.toContain(`${duringText}${duringText}`);
  expect(text).not.toContain(`${afterText}${afterText}`);
  expect(cursorLine(app)).toBe("Business cursor: 3");
  expect(text).toContain("Adapter: unavailable");
  expect(text).toContain("Makez is not connected.");
  expectNoModelReply(app);

  await app.press("Nia");
  await app.waitFor(niaText, { timeout: 15000 });
  await app.waitFor(niaCursor, { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(niaCursor);
  expect(cursorLine(app)).not.toBe("Business cursor: 3");
  expect(visible(app)).toContain(niaText);
  expect(app.texts().filter((line) => line === duringText).length).toBe(0);
  expectNoModelReply(app);
}, 180000);

test("an unset fixture stays unavailable", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Unset fixture", { timeout: 15000 });
  await app.press("Unset fixture");
  await app.waitFor("Fixture unset", { timeout: 20000 });
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Una");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", plainText);
  await app.press("Send");
  await app.waitFor(plainText, { timeout: 15000 });
  await app.waitFor("Execution is unavailable.", { timeout: 15000 });
  await pause();
  expect(visible(app)).toContain("Adapter: unavailable");
  expect(visible(app)).toContain("Makez is not connected.");
  expect(visible(app)).toContain(plainText);
  expectNoModelReply(app);
}, 120000);
