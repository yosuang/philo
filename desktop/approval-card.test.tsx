// The chat shows the approval the server stored. Approving posts the decision.
// PHILO_TEST_EDIT_SUMMARY names one replacement summary. The client posts it
// to POST /v1/approvals/{id}/edit, then posts a decision for the old id, then
// reads GET /v1/approvals. The window has no editor and no button that
// invents the card. The old card stays invalid. The new card shows the new
// snapshot. Approving the new card uses the Approve control.
//
// PHILO_EXECUTION_FIXTURE=dev on http://127.0.0.1:8080. That fixture is not
// live Makez. Leave PHILO_MAKEZ_URL unset. The desktop process needs
// PHILO_ACTOR_TOKEN and PHILO_TEST_EDIT_SUMMARY=send the revised note.
//
// nsh test approval-card.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 400));
const first = "send the weekly note";
const revised = "send the revised note";

function exact(app: { texts: () => string[] }, line: string) {
  return app.texts().filter((item) => item === line).length;
}

test("the chat shows the stored card and an edit blocks the old snapshot", async () => {
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
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });

  await app.type("Message", `/draft ${first}`);
  await app.press("Send");
  await app.waitFor(`Snapshot: ${revised}`, { timeout: 15000 });
  await app.waitFor("Status: invalid", { timeout: 15000 });
  await app.waitFor("approval_not_pending: approval is not pending", { timeout: 15000 });
  await pause();

  expect(exact(app, `Snapshot: ${first}`)).toBe(1);
  expect(exact(app, `Snapshot: ${revised}`)).toBe(1);
  expect(exact(app, "Status: invalid")).toBe(1);
  expect(exact(app, "Status: pending")).toBe(1);
  expect(exact(app, "Approve")).toBe(1);
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
  expect(app.texts().join("\n")).toContain("Draft action is waiting for approval: " + first);
  expect(app.texts().join("\n")).not.toContain("Configure the action");

  await app.press("Approve");
  await app.waitFor("Status: approved", { timeout: 15000 });
  await pause();
  expect(exact(app, "Status: approved")).toBe(1);
  expect(exact(app, "Status: invalid")).toBe(1);
  expect(exact(app, "Status: pending")).toBe(0);
  expect(exact(app, "Approve")).toBe(0);
  expect(exact(app, `Snapshot: ${revised}`)).toBe(1);
  expect(exact(app, `Snapshot: ${first}`)).toBe(1);
  expect(app.texts().join("\n")).toContain("Development adapter. This is not a live model.");
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
}, 120000);
