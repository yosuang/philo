// Drives the live session server on http://127.0.0.1:8080.
// Run with the server up: `nsh test session-loop.test.tsx --target linux`
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 1200));

const adaReply =
  "[philo dev adapter] Deterministic local reply. This is not a live model.\nReceived: Hello from Philo";
const beaReply =
  "[philo dev adapter] Deterministic local reply. This is not a live model.\nReceived: Bea keeps her own notes";

test("bots keep separate chats without a session step", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await pause();

  await app.press("New bot");
  await app.type("Name", "Ada");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "dev");
  await pause();
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  expect(app.texts().join("\n")).not.toContain("New session");
  expect(app.texts().join("\n")).not.toContain("Sessions");

  await app.type("Message", "Hello from Philo");
  await app.press("Send");
  await app.waitFor(adaReply, { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Bea");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "dev");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", "Bea keeps her own notes");
  await app.press("Send");
  await app.waitFor(beaReply, { timeout: 15000 });
  await pause();

  await app.press("Ada");
  await app.waitFor(adaReply, { timeout: 15000 });
  expect(app.texts().join("\n")).not.toContain(beaReply);

  await app.press("Bea");
  await app.waitFor(beaReply, { timeout: 15000 });
  expect(app.texts().join("\n")).not.toContain(adaReply);

  await app.press("Ada");
  await app.waitFor(adaReply, { timeout: 15000 });
  await app.press("Details");
  await app.press("Reload");
  await app.waitFor(adaReply, { timeout: 15000 });
  expect(app.texts().join("\n")).not.toContain(beaReply);
  await app.press("Close details");

  await app.type("Message", "/draft Ship the note");
  await app.press("Send");
  await app.waitFor("Status: pending", { timeout: 15000 });
  await pause();
  await app.press("Approve");
  await app.waitFor("Status: completed", { timeout: 15000 });
  await pause();

  await app.press("Details");
  await app.press("Connectors");
  await app.waitFor("PHILO_MAKEZ_URL is not set", { timeout: 15000 });
  await pause();
  expect(app.texts().join("\n")).toContain("Not connected");

  const again = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await again.waitFor("Ada", { timeout: 15000 });
  await again.press("Ada");
  await again.waitFor(adaReply, { timeout: 15000 });
  expect(again.texts().join("\n")).not.toContain(beaReply);
  await again.press("Bea");
  await again.waitFor(beaReply, { timeout: 15000 });
  expect(again.texts().join("\n")).not.toContain(adaReply);
  expect(again.texts().join("\n")).not.toContain("New session");
});
