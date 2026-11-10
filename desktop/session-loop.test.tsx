// Drives a production session server on http://127.0.0.1:8080.
// Leave PHILO_EXECUTION_FIXTURE unset. PHILO_EXECUTION_FIXTURE=dev is the
// isolated fixture and is not a successful model reply.
// The desktop process needs PHILO_ACTOR_TOKEN for the server credential.
// Run with the server up: `nsh test session-loop.test.tsx --target linux`
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 1200));

const adaMessage = "Hello from Philo";
const beaMessage = "Bea keeps her own notes";
const draftMessage = "/draft Ship the note";

function visible(app: { texts: () => string[] }) {
  return app.texts().join("\n");
}

function expectNoModelReply(app: { texts: () => string[] }) {
  const text = visible(app);
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");
  expect(text).not.toContain("dev adapter");
  expect(text).not.toContain("Status: pending");
  expect(text).not.toContain("Status: completed");
}

test("production submit stays unavailable and does not look like a model reply", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await pause();
  expectNoModelReply(app);
  expect(visible(app)).not.toContain("New session");
  expect(visible(app)).not.toContain("Sessions");

  await app.press("New bot");
  await app.type("Name", "Ada");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await pause();
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  expect(visible(app)).not.toContain("New session");
  expect(visible(app)).not.toContain("Sessions");

  await app.type("Message", adaMessage);
  await app.press("Send");
  await app.waitFor(adaMessage, { timeout: 15000 });
  await app.waitFor("Execution is unavailable.", { timeout: 15000 });
  await pause();
  expectNoModelReply(app);

  await app.press("New bot");
  await app.type("Name", "Bea");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", beaMessage);
  await app.press("Send");
  await app.waitFor(beaMessage, { timeout: 15000 });
  await app.waitFor("Execution is unavailable.", { timeout: 15000 });
  await pause();
  expectNoModelReply(app);

  await app.press("Ada");
  await app.waitFor(adaMessage, { timeout: 15000 });
  await pause();
  expectNoModelReply(app);

  await app.press("Bea");
  await app.waitFor(beaMessage, { timeout: 15000 });
  expectNoModelReply(app);

  await app.press("Ada");
  await app.waitFor(adaMessage, { timeout: 15000 });
  await app.press("Details");
  await app.press("Reload");
  await app.waitFor(adaMessage, { timeout: 15000 });
  await pause();
  expectNoModelReply(app);

  await app.type("Message", draftMessage);
  await app.press("Send");
  await app.waitFor(draftMessage, { timeout: 15000 });
  await app.waitFor("Execution is unavailable.", { timeout: 15000 });
  await pause();
  expectNoModelReply(app);

  await app.press("Connectors");
  await app.waitFor("PHILO_MAKEZ_URL is not set", { timeout: 15000 });
  await pause();
  expect(visible(app)).toContain("Not connected");
  expectNoModelReply(app);

  const again = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await again.waitFor("Adapter: unavailable", { timeout: 15000 });
  await again.waitFor("Ada", { timeout: 15000 });
  await again.press("Ada");
  await again.waitFor(adaMessage, { timeout: 15000 });
  expectNoModelReply(again);
  await again.press("Bea");
  await again.waitFor(beaMessage, { timeout: 15000 });
  expectNoModelReply(again);
  expect(visible(again)).not.toContain("New session");
  expect(visible(again)).not.toContain("Sessions");
});
