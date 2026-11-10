// The subscribe ack is held on the real socket. A business event committed
// after the unsubscribed read must show up on the retry, and the window must
// not say connected or recovered until a read that started subscribed has
// finished. A frame for the other bot, and a frame from the socket generation
// that was abandoned, must not move this chat.
//
// Leave PHILO_EXECUTION_FIXTURE unset. Leave PHILO_MAKEZ_URL unset.
// PHILO_BUSINESS_FIXTURE=fixture-business-source is the labeled fixture.
// It is not a model reply and not Makez.
// The desktop process needs PHILO_ACTOR_TOKEN=philo-test-actor and
// PHILO_FIXTURE_SUFFIX=1. The server accepts that local actor token.
// It is not a model key.
//
// Before this test, on a server at http://127.0.0.1:8080:
// 1. Create bots Reed and Lark and open each conversation.
// 2. Post fixture-business-source events that materialize reed on Reed and
//    lark on Lark: fixture.seed, message.delta, message.completed.
// Do not delete those events. Each stored business cursor is not 0.
// Do not hold subscribe before the window is showing Reed.
//
// Run: nsh test subscribe-gap.test.tsx --target linux
import { expect, open, test } from "nsh/test";

const pause = () => new Promise((resolve) => setTimeout(resolve, 400));
const heldText = "reed";
const gapText = "gaponce";
const duringText = "midonce";
const streamText = "streambit";
const oldText = "oldsock";
const otherText = "lark";
const foreignText = "foreignonce";
const foreignStream = "otherbit";
const fixtureAdapter = "fixture-business-source adapter";

function visible(app: { texts: () => string[] }) {
  return app.texts().join("\n");
}

function count(text: string, needle: string) {
  return text.split(needle).length - 1;
}

function lineStarting(app: { texts: () => string[] }, prefix: string) {
  return app.texts().find((line) => line.startsWith(prefix)) ?? "";
}

test("late or missing subscribe ack retries the read before it reports recovery", async () => {
  const app = await open(".", { network: "live", width: 1100, height: 1100, platform: "linux" });
  await app.waitFor("Your agent, your friend. Always on for you.");
  await app.waitFor("Adapter: unavailable", { timeout: 15000 });
  await app.waitFor("Makez is not connected.", { timeout: 15000 });
  await app.waitFor("Reed", { timeout: 15000 });
  await app.press("Reed");
  await app.waitFor(heldText, { timeout: 15000 });
  await app.waitFor(fixtureAdapter, { timeout: 15000 });
  await app.press("Details");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  await app.waitFor("Message seq: 1", { timeout: 15000 });
  await pause();

  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(lineStarting(app, "Business cursor:")).not.toBe(lineStarting(app, "Message seq:"));
  expect(visible(app)).toContain(heldText);
  expect(visible(app)).not.toContain(gapText);

  await app.press("Hold subscribe");
  await app.waitFor("Subscribe hold armed", { timeout: 15000 });
  await app.press("Disconnect");
  await app.waitFor("Events: disconnected", { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");

  await app.press("Reconnect");
  await app.waitFor("Events: catching up", { timeout: 15000 });
  expect(lineStarting(app, "Events:")).not.toBe("Events: connected");
  expect(lineStarting(app, "Events:")).not.toBe("Events: recovered");
  await app.waitFor("Events: subscribe failed", { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Events:")).toBe("Events: subscribe failed");
  expect(visible(app)).not.toContain("Events: connected");
  expect(visible(app)).not.toContain("Events: recovered");
  expect(visible(app)).not.toContain(gapText);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");

  await app.press("Gap event");
  await app.waitFor("Gap event posted", { timeout: 15000 });
  await pause();
  expect(visible(app)).not.toContain(gapText);
  expect(lineStarting(app, "Events:")).toBe("Events: subscribe failed");

  const caught = `${heldText}${gapText}`;
  await app.waitFor(caught, { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Events:")).toBe("Events: subscribe failed");
  expect(visible(app)).not.toContain("Events: connected");
  expect(visible(app)).not.toContain("Events: recovered");
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 4");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(app.texts().filter((line) => line === caught).length).toBe(2);
  expect(visible(app)).not.toContain(`${gapText}${gapText}`);

  await app.press("Release subscribe");
  await app.waitFor("Subscribe released", { timeout: 15000 });
  await app.waitFor("Events: recovered", { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Events:")).toBe("Events: recovered");
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 4");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(app.texts().filter((line) => line === caught).length).toBe(2);
  expect(visible(app)).not.toContain(`${gapText}${gapText}`);
  expect(visible(app)).toContain("Adapter: unavailable");
  expect(visible(app)).toContain("Makez is not connected.");
  expect(count(visible(app), fixtureAdapter)).toBe(1);

  await app.press("Stage during read");
  await app.waitFor("During read staged", { timeout: 15000 });
  await app.press("Reload");
  const during = `${caught}${duringText}`;
  await app.waitFor(during, { timeout: 15000 });
  await pause();
  expect(app.texts().filter((line) => line === during).length).toBe(2);
  expect(visible(app)).not.toContain(`${duringText}${duringText}`);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 5");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(lineStarting(app, "Events:")).toBe("Events: recovered");

  await app.press("Stream delta");
  await app.waitFor("Stream delta posted", { timeout: 15000 });
  const streamed = `${during}${streamText}`;
  await app.waitFor(streamed, { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 5");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(app.texts().filter((line) => line === streamed).length).toBe(2);
  expect(visible(app)).not.toContain(`${streamText}${streamText}`);

  await app.press("Abandon socket");
  await app.waitFor("Events: reconnecting", { timeout: 15000 });
  await app.press("Old socket delta");
  await app.waitFor("Old socket frame ignored", { timeout: 15000 });
  expect(visible(app)).not.toContain(oldText);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 5");
  await app.waitFor("Events: connected", { timeout: 15000 });
  await pause();
  expect(visible(app)).not.toContain(oldText);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 5");
  expect(lineStarting(app, "Message seq:")).toBe("Message seq: 1");
  expect(app.texts().filter((line) => line === streamed).length).toBe(2);

  await app.press("Hold next subscribe");
  await app.waitFor("Next subscribe hold armed", { timeout: 15000 });
  await app.press("Lark");
  await app.waitFor(otherText, { timeout: 15000 });
  await pause();
  await app.waitFor("Business cursor: 3", { timeout: 15000 });
  const larkSeq = lineStarting(app, "Message seq:");
  const larkCursor = lineStarting(app, "Business cursor:");
  expect(larkCursor).toBe("Business cursor: 3");
  expect(larkSeq).not.toBe("");
  expect(larkSeq).not.toBe("Message seq: 3");
  expect(larkSeq).not.toBe(larkCursor);
  await app.press("Foreign event");
  await app.press("Foreign stream");
  await app.waitFor("Foreign frame ignored", { timeout: 15000 });
  await pause();
  expect(visible(app)).not.toContain(foreignText);
  expect(visible(app)).not.toContain(foreignStream);
  expect(visible(app)).toContain(otherText);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");
  expect(lineStarting(app, "Message seq:")).toBe(larkSeq);
  expect(lineStarting(app, "Events:")).not.toBe("Events: connected");
  expect(lineStarting(app, "Events:")).not.toBe("Events: recovered");

  await app.press("Release subscribe");
  await app.waitFor("Events: recovered", { timeout: 15000 });
  await pause();
  expect(lineStarting(app, "Events:")).toBe("Events: recovered");
  expect(visible(app)).not.toContain(foreignText);
  expect(visible(app)).not.toContain(foreignStream);
  expect(visible(app)).toContain(otherText);
  expect(lineStarting(app, "Business cursor:")).toBe("Business cursor: 3");
  expect(lineStarting(app, "Message seq:")).toBe(larkSeq);
  expect(visible(app)).toContain("Adapter: unavailable");
  expect(visible(app)).toContain("Makez is not connected.");
  expect(visible(app)).not.toContain("[philo dev adapter]");
  expect(visible(app)).not.toContain("Deterministic local reply");
  expect(visible(app)).not.toContain("Development adapter");
}, 120000);
