// The desktop must receive an expired business cursor itself.
// GET /snapshot is not this check. Leave PHILO_EXECUTION_FIXTURE unset.
// PHILO_BUSINESS_FIXTURE=fixture-business-source is the labeled fixture.
// It is not a model reply and not Makez.
//
// Before this test, on a server at http://127.0.0.1:8080:
// 1. Create a bot named Reed and open its conversation.
// 2. Post fixture-business-source events that materialize the text redgreen.
// 3. Delete business events at or below the first delta so after=0 is expired.
// The message row stays. The client starts with no transcript and cursor 0.
//
// Run: nsh test expired-cursor.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 1200));
const rebuilt = "Business cursor expired. This chat was rebuilt from the snapshot.";
const fixtureAdapter = "fixture-business-source adapter";

function visible(app: { texts: () => string[] }) {
  return app.texts().join("\n");
}

function count(text: string, needle: string) {
  return text.split(needle).length - 1;
}

test("expired business cursor rebuilds the snapshot once", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await app.waitFor("Reed", { timeout: 15000 });
  await app.press("Reed");
  await app.waitFor(rebuilt, { timeout: 15000 });
  await app.waitFor("redgreen", { timeout: 15000 });
  await app.waitFor(fixtureAdapter, { timeout: 15000 });
  await pause();

  let text = visible(app);
  expect(count(text, rebuilt)).toBe(1);
  expect(count(text, fixtureAdapter)).toBe(1);
  expect(count(text, "redgreen")).toBeGreaterThan(0);
  expect(text).not.toContain("redgreenred");
  expect(text).not.toContain("redgreenredgreen");
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).not.toContain("Deterministic local reply");
  expect(text).not.toContain("Development adapter");
  expect(text).toContain("Adapter: unavailable");

  await pause();
  await app.press("Details");
  await app.press("Reload");
  await app.waitFor("redgreen", { timeout: 15000 });
  await app.waitFor(fixtureAdapter, { timeout: 15000 });
  await pause();

  text = visible(app);
  expect(count(text, fixtureAdapter)).toBe(1);
  expect(text).toContain("redgreen");
  expect(text).not.toContain("redgreenred");
  expect(text).not.toContain("redgreenredgreen");
  expect(text).not.toContain("[philo dev adapter]");
  expect(text).toContain("Adapter: unavailable");
});
