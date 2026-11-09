// The Rust root, opened headless by `nsh test`.
import { expect, open, test } from "nsh/test";

test("shows the desktop shell", async () => {
  const app = await open(".", { width: 1024, height: 748, platform: "linux" });
  const text = () => app.texts().join("\n");
  expect(text()).toContain("Philo");
  expect(text()).toContain("Your agent, your friend.");

  await app.press("Details");
  await app.press("Settings");
  expect(text()).toContain("Server URL");
  await app.press("Dark");
  expect(text()).toContain("Appearance: dark");

  await app.press("Skills");
  expect(text()).toContain("Not available yet");

  await app.press("Connectors");
  expect(text()).toContain("Sign-in");
  expect(text()).toContain("Not available yet");
});
