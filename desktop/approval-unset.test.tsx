// Leave PHILO_EXECUTION_FIXTURE and PHILO_TEST_EDIT_SUMMARY unset. A draft
// stays unavailable and the server creates no approval card. This is not a
// live model and not Makez. The desktop process needs PHILO_ACTOR_TOKEN.
//
// nsh test approval-unset.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 400));
const draft = "/draft send the weekly note";

test("an unset fixture stays unavailable and creates no card", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Una");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", draft);
  await app.press("Send");
  await app.waitFor(draft, { timeout: 15000 });
  await app.waitFor("Execution is unavailable.", { timeout: 15000 });
  await pause();

  const text = app.texts().join("\n");
  expect(text).toContain("Adapter: unavailable");
  expect(text).toContain("Makez is not connected.");
  expect(text).toContain(draft);
  expect(text).not.toContain("Snapshot:");
  expect(text).not.toContain("Status: pending");
  expect(text).not.toContain("Approve");
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");
}, 90000);
