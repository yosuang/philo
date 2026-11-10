// The desktop is already showing one bot's chat. The labeled fixture source
// is committed. Start server, with PHILO_PUBLISH_CRASH=1 on the harness,
// runs a process that stores the armed log and is SIGKILLed on the publish
// line, before h.Publish. A second process then listens on the same database.
// The open client reads that row from the cursor it already holds. It does
// not press Reconnect and it does not send the message again.
//
// The server binary is the normal go build. Delve only stops the test
// process on that source line. Production does not set PHILO_RESTART_URL
// or PHILO_PUBLISH_CRASH, and the server has no pause flag.
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor.
//
// Run, with that harness already listening:
// nsh test publish-crash.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 800));
const wynnText = "Remember the cedar";
const niaText = "Nia keeps her note";
const duringText = "test-fixture-source:during-restart";

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

test("the open client shows a log committed before publish", async () => {
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
  await app.waitFor("Server started", { timeout: 30000 });
  await app.waitFor(`text:${duringText}`, { timeout: 20000 });
  await app.waitFor("Business cursor: 2", { timeout: 15000 });
  await app.waitFor("Events: connected", { timeout: 15000 });
  await pause();

  const text = visible(app);
  expect(text).toContain(wynnText);
  expect(text).toContain(duringText);
  // The bot row repeats the latest line. The transcript has one bubble.
  expect(app.texts().filter((line) => line === duringText).length).toBe(2);
  expect(app.texts().filter((line) => line === wynnText).length).toBe(1);
  expect(count(text, duringText)).toBe(2);
  expect(cursorLine(app)).toBe("Business cursor: 2");
  expectNoModelReply(app);

  await app.press("Nia");
  await app.waitFor(niaText, { timeout: 15000 });
  await app.waitFor(niaCursor, { timeout: 15000 });
  await pause();
  expect(cursorLine(app)).toBe(niaCursor);
  expect(cursorLine(app)).toBe("Business cursor: 1");
  expect(visible(app)).toContain(niaText);
  // Wynn's row still shows her latest line. Nia's transcript does not.
  expect(app.texts().filter((line) => line === duringText).length).toBe(1);
  expect(app.texts().filter((line) => line === niaText).length).toBe(2);
  expectNoModelReply(app);
}, 180000);
