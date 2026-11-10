// The other API client for approval-reconnect.test.tsx. It is not the window
// and it is not PHILO_TEST_EDIT_SUMMARY. The fixture server is
// http://127.0.0.1:8080. The bearer is the local test actor, not a model key.
//
// It waits until Cara's chat has a pending card for "send the weekly note",
// waits long enough for that chat to show the card and press Approve, then
// posts one production edit. The old card becomes invalid. The new card
// stores "send the revised note".
//
// node approval-reconnect-edit.mjs
const base = "http://127.0.0.1:8080";
const token = "philo-test-actor";
const first = "send the weekly note";
const revised = "send the revised note";
const settleMs = 1200;

async function api(method, path, body) {
  const response = await fetch(`${base}${path}`, {
    method,
    headers: {
      authorization: `Bearer ${token}`,
      accept: "application/json",
      ...(body ? { "content-type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await response.text();
  let json = null;
  if (text) {
    try {
      json = JSON.parse(text);
    } catch {
      json = null;
    }
  }
  return { status: response.status, text, json };
}

function summaryOf(card) {
  const snapshot = card?.actionSnapshot?.summary;
  if (typeof snapshot === "string" && snapshot.trim() !== "") {
    return snapshot.trim();
  }
  return typeof card?.summary === "string" ? card.summary.trim() : "";
}

const deadline = Date.now() + 60000;
let card = null;
while (!card && Date.now() < deadline) {
  const agents = await api("GET", "/v1/agents");
  const cara = agents.json?.agents?.find((item) => item.name === "Cara");
  if (cara) {
    const conversation = await api("GET", `/v1/agents/${cara.id}/conversation`);
    if (conversation.status === 200 && conversation.json?.id) {
      const listed = await api("GET", `/v1/approvals?sessionId=${conversation.json.id}`);
      const cards = listed.json?.approvals ?? [];
      card = cards.find((item) => item.status === "pending" && summaryOf(item) === first) ?? null;
    }
  }
  if (!card) {
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
}
if (!card?.id) {
  console.error("no pending card");
  process.exit(1);
}
console.log(`saw pending ${card.id}`);
await new Promise((resolve) => setTimeout(resolve, settleMs));
const edited = await api("POST", `/v1/approvals/${card.id}/edit`, { summary: revised });
console.log(`edit ${edited.status} ${edited.text}`);
if (edited.status !== 200) {
  process.exit(1);
}
