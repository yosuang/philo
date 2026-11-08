// Drives the live session server on http://127.0.0.1:8080.
// Run with the server up: `nsh test session-loop.test.tsx --target linux`
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 1200));

test("session loop against the dev adapter", async () => {
  const app = await open(".", { network: "live", width: 1280, height: 900, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await pause();

  await app.press("New agent");
  await app.type("Name", "Ada");
  await app.type("What this agent is for", "Be brief.");
  await app.type("model id", "dev");
  await pause();
  await app.press("Save agent");
  await app.waitFor("Start a session for this agent, then send a message.", { timeout: 15000 });

  await app.press("New session");
  await app.waitFor("New session");
  await app.type("Message", "Hello from Philo");
  await app.press("Send");
  await app.waitFor(
    "[philo dev adapter] Deterministic local reply. This is not a live model.\nReceived: Hello from Philo",
    { timeout: 15000 },
  );
  await pause();

  await app.type("Message", "/draft Ship the note");
  await app.press("Send");
  await app.waitFor("Status: pending", { timeout: 15000 });
  await pause();
  await app.press("Approve");
  await app.waitFor("Status: completed", { timeout: 15000 });
  await pause();

  await app.press("Connectors");
  await app.waitFor("PHILO_MAKEZ_URL is not set", { timeout: 15000 });
  await pause();
  expect(app.texts().join("\n")).toContain("Not connected");
});
