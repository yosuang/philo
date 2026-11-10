// PHILO_EXECUTION_FIXTURE=worker on http://127.0.0.1:8080. The desktop sends
// /draft, approves, and then sends another message through the composer.
// The fixture-worker process is still unfinished when the second message is
// sent. The snapshot, requester, and task id stay on that bot. The other bot
// does not show them. The fixture then finishes on its own. The result is
// not a model reply. Leave PHILO_TEST_EDIT_SUMMARY and PHILO_MAKEZ_URL unset.
// The desktop process needs PHILO_ACTOR_TOKEN. The server credential's actor
// is actor-test. This fixture is not Makez.
// The unset window is approval-unset.test.tsx.
//
// nsh test worker-message.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 500));
const summary = "send the weekly note";
const follow = "Willa sends while the task runs";
const other = "Hugo keeps his own notes";
const fixtureEnd = "Result: fixture-worker finished; not a live model and not Makez";

function exact(app: { texts: () => string[] }, line: string) {
  return app.texts().filter((item) => item === line).length;
}

function taskLine(app: { texts: () => string[] }) {
  const found = app.texts().filter((item) => item.startsWith("Task id: "));
  expect(found.length).toBe(1);
  return found[0];
}

test("a new message leaves the unfinished worker on its card", async () => {
  const app = await open(".", { network: "live", width: 1024, height: 748, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: worker", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });

  await app.press("New bot");
  await app.type("Name", "Willa");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });

  await app.type("Message", `/draft ${summary}`);
  await app.press("Send");
  await app.waitFor(`Snapshot: ${summary}`, { timeout: 15000 });
  await app.waitFor("Status: pending", { timeout: 15000 });
  await app.waitFor("Requester: actor-test", { timeout: 15000 });
  await pause();
  expect(exact(app, "Approve")).toBe(1);
  expect(app.texts().join("\n")).not.toContain("Configure the action");
  expect(app.texts().join("\n")).not.toContain("Task id:");

  await app.press("Approve");
  await app.waitFor("Status: approved", { timeout: 15000 });
  await app.waitFor("Status: running", { timeout: 15000 });
  await app.waitFor("Source: philo-server", { timeout: 15000 });
  await pause();
  const task = taskLine(app);
  const text = app.texts().join("\n");
  expect(text).not.toContain("Status: completed");
  expect(text).not.toContain("Result:");
  expect(exact(app, `Snapshot: ${summary}`)).toBe(1);
  expect(exact(app, "Requester: actor-test")).toBe(1);
  expect(exact(app, "Status: approved")).toBe(1);
  expect(exact(app, "Status: running")).toBe(1);
  expect(exact(app, "Approve")).toBe(0);

  await app.type("Message", follow);
  await app.press("Send");
  await app.waitFor(follow, { timeout: 15000 });
  await pause();
  expect(taskLine(app)).toBe(task);
  expect(exact(app, `Snapshot: ${summary}`)).toBe(1);
  expect(exact(app, "Requester: actor-test")).toBe(1);
  expect(exact(app, "Status: approved")).toBe(1);
  expect(exact(app, "Status: running")).toBe(1);
  expect(exact(app, "Source: philo-server")).toBe(1);
  expect(app.texts().join("\n")).not.toContain("Status: completed");
  expect(app.texts().join("\n")).not.toContain("Result:");
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
  expect(app.texts().join("\n")).not.toContain("Configure the action");

  await app.press("New bot");
  await app.type("Name", "Hugo");
  await app.type("What this bot is for", "Be brief.");
  await app.type("model id", "local");
  await app.press("Save bot");
  await app.waitFor("Send a message. The chat is saved on the server.", { timeout: 15000 });
  await app.type("Message", other);
  await app.press("Send");
  await app.waitFor(other, { timeout: 15000 });
  await pause();
  expect(exact(app, `Snapshot: ${summary}`)).toBe(0);
  expect(exact(app, "Requester: actor-test")).toBe(0);
  expect(exact(app, task)).toBe(0);
  expect(exact(app, "Status: running")).toBe(0);
  expect(exact(app, "Status: approved")).toBe(0);
  expect(app.texts().join("\n")).toContain(other);

  await app.press("Willa");
  await app.waitFor(follow, { timeout: 15000 });
  await app.waitFor(`Snapshot: ${summary}`, { timeout: 15000 });
  await app.waitFor("Status: completed", { timeout: 30000 });
  await app.waitFor(fixtureEnd, { timeout: 15000 });
  await pause();
  expect(taskLine(app)).toBe(task);
  expect(exact(app, "Requester: actor-test")).toBe(1);
  expect(exact(app, "Status: approved")).toBe(1);
  expect(exact(app, `Snapshot: ${summary}`)).toBe(1);
  expect(exact(app, "Status: running")).toBe(0);
  expect(exact(app, "Status: completed")).toBe(1);
  expect(exact(app, fixtureEnd)).toBe(1);
  expect(exact(app, "Source: philo-server")).toBe(1);
  expect(app.texts().join("\n")).not.toContain("Result: [philo dev adapter]");
  expect(app.texts().join("\n")).toContain("Makez is not connected.");
}, 150000);
