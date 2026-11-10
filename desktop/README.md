# Philo desktop

Native Shell client for the phase-1 session server. The app is Rust (`src/lib.rs`). `nsh.json` mounts the `App` root. It does not run the agent or store the transcript.

Slogan: **Your agent, your friend.** Always on for you.

## Requirements

- [NSH](https://nsh.dev) `0.4.0` (`nsh` on `PATH`). Install: `curl -fsSL https://nsh.dev/install.sh | sh`
- Rust 1.85 or newer (edition 2024). This crate was checked with stable.
- Linux: GTK 4.14 or newer (`libgtk-4-dev` to build). macOS 14+ and Windows 10 1809+ use the same sources; this environment ran the GTK window only.

## Run

Start the session server from this repository. `server/README.md` has the command. The API listens on `http://127.0.0.1:8080`. Leave `PHILO_EXECUTION_FIXTURE` unset. `PHILO_MODEL_ADAPTER=dev` does not turn the fixture on. Leave `PHILO_MAKEZ_URL` unset so Makez stays disconnected. Set `PHILO_ACTOR_TOKEN` to the bearer token in `PHILO_AUTH_CREDENTIALS`.

From this directory:

```bash
cargo run
```

That opens a GTK, AppKit, or Win32 window at 1024×748. The same root:

```bash
nsh run
nsh run --window-size 1024x748
```

`nsh run` builds NSH's runner for this machine. `cargo run` uses `src/main.rs`.

The window opens as two columns. The sidebar lists bots. Choosing a bot opens that bot's chat and restores its messages. Creating a bot lands in its chat. There is no session list and no step that asks for a session before a message can be sent. A session stays a server record: the client resumes the newest one for that bot, or creates one the first time.

Details, in the chat header, opens the third column and closes it again. That column holds the live chat state, Makez, and the pages that are not available yet. Search and voice are controls only; each one says it is not available.

Settings shows the server URL. The default is `http://127.0.0.1:8080`. Saving it reconnects the event socket at `ws://127.0.0.1:8080/v1/events`.

## What the window does

With the fixture unset, the banner says `Adapter: unavailable`. Create a bot (name, instructions, model id) and send a message. The user message stays in the transcript and the window says `Execution is unavailable.` There is no assistant reply. Switching bots restores each transcript. Reload reads that bot's messages again. If the business cursor the client still holds is expired, the client applies the snapshot and high water from that HTTP 400 and does not append a second copy. The window says the chat was rebuilt from the snapshot. Cancel posts `POST /v1/sessions/{id}/cancel` on the resumed server session. Retry appears only after an assistant message is `error` or `canceled`.

`PHILO_EXECUTION_FIXTURE=dev` is the isolated fixture. That process shows the development-adapter banner and streams a fixed local reply. It is not a production success and it is not Makez. A message that starts with `/draft ` asks that fixture for one pending approval. The chat shows that card from the server: the action snapshot and the stored status. Approve and Reject are shown only while the card is pending and still has a snapshot. They post `POST /v1/approvals/{id}/decision`. There is no editor for the action. `PHILO_TEST_EDIT_SUMMARY`, when set, is one replacement summary. The client posts it to `POST /v1/approvals/{id}/edit`, posts a decision for the old id, and replaces the cards with `GET /v1/approvals`. Production leaves that variable unset, and then the client does not edit. An edit from any client marks the old card `invalid` and stores a new card. The open chat follows that from the live approval event and from the stored list. It does not wait for Reload. A later stored write replaces the card on screen. An older live frame, or a decision response that comes back after that write, does not paint the card approved or make the old snapshot approvable again. A failed decision reloads the stored cards and does not leave the card approved. Switching bots and reconnecting show those stored cards. On the fixture, approve is what lets the server complete a `philo-server` task for the snapshot it stored. Reject does not. With the fixture unset, a draft stays `Execution is unavailable.` and creates no card.

Connectors shows Makez as not connected, with the server's reason, unless `PHILO_MAKEZ_URL` is set and healthy. Attachments, notifications, skills, memory, routines, tool-call transcripts, and connector sign-in are real pages that say they are not available yet.

Light, Dark, and System call `set_color_scheme`.

## Checks

```bash
cargo test --lib
nsh test App.test.tsx
```

`cargo test --lib` covers JSON parsing for the HTTP and WebSocket contract. `App.test.tsx` opens the shell headless: slogan, settings, the theme control, and a page that is not available yet.

With a production server running and `PHILO_EXECUTION_FIXTURE` unset, the GTK window loop is `session-loop.test.tsx`:

```bash
nsh test session-loop.test.tsx --target linux
```

That test needs `network: "live"`, `PHILO_ACTOR_TOKEN`, and the server on port 8080. It checks that a send stays `Execution is unavailable.` and does not show a dev-adapter reply. `nsh test` with no file also runs it, so start the server first or pass `App.test.tsx` only.

`expired-cursor.test.tsx` is the expired business cursor. Leave `PHILO_EXECUTION_FIXTURE` unset. Set `PHILO_BUSINESS_FIXTURE=fixture-business-source`, which is the labeled fixture and not a model reply. Seed one fixture transcript, then delete the early business events so a cursor of `0` is expired. The desktop process needs `PHILO_ACTOR_TOKEN`.

```bash
nsh test expired-cursor.test.tsx --target linux
```

The window receives the HTTP 400 itself, replaces the transcript with that snapshot, and does not append the same text again. `Adapter: unavailable` stays up. That file starts from an empty transcript and `after=0`. It does not reconnect.

`expired-nonzero-cursor.test.tsx` starts from messages that are already on screen and a non-zero business cursor. Set `PHILO_FIXTURE_SUFFIX=1`. Disconnect the socket, stage the labeled fixture so that held cursor is expired, then reconnect. The server returns HTTP 400 with a replacement snapshot. The client replaces the transcript from that snapshot once, then shows the following suffix once. `message.seq` is not the business cursor. The fixture stays labeled. It is not Makez.

```bash
nsh test expired-nonzero-cursor.test.tsx --target linux
```

`reconnect-cursor.test.tsx` starts from messages that are already on screen and a non-zero business cursor. Set `PHILO_FIXTURE_SUFFIX=1` so the window can store one later `fixture-business-source` delta. It disconnects the event socket, stores that delta while the socket is down, then reconnects. The client reads with the cursor it still holds, shows that suffix once, and does not append it again. The fixture stays labeled. It is not Makez.

`read-backoff.test.tsx` is a subscribed Reload whose `business-events` answers HTTP 500. The window leaves `Events: connected`. Further reads wait on a capped exponential backoff, so a couple of seconds do not hold dozens of GETs. Disconnect and switching bots stop that retry. Releasing the failure lets the same chat catch up again. The business cursor is unchanged.

```bash
nsh test read-backoff.test.tsx --target linux
```

`subscribe-gap.test.tsx` holds the real subscribe ack. The window says `Events: catching up` when the socket opens and `Events: subscribe failed` when the 5 second budget ends without an ack. It does not say `Events: connected` or `Events: recovered` for that unsubscribed read. A fixture event committed after that read is missing until the client reads again. After the ack is released, the window says `Events: recovered` and the event is still there once. `Message seq` stays separate from `Business cursor`. A business event published while a read is in progress is shown once. A cursorless fixture delta, the same shape as an engine stream frame, does not move the business cursor. A frame on the socket generation that was abandoned is ignored. A frame for the other bot is ignored. The fixture stays labeled. It is not Makez.

```bash
nsh test subscribe-gap.test.tsx --target linux
```

```bash
nsh test reconnect-cursor.test.tsx --target linux
```

`approval-card.test.tsx` sends `/draft` through the chat to a server with `PHILO_EXECUTION_FIXTURE=dev`. Set `PHILO_TEST_EDIT_SUMMARY` to the replacement summary. The card that appears is the one the API and the store saved, including its action snapshot. The client posts that summary to the production edit route and reads the cards back. The old card stays `invalid` and a decision on it is `approval_not_pending`. The new card shows the new snapshot. Approving uses the Approve control. The window has no editor. The fixture is not Makez.

```bash
nsh test approval-card.test.tsx --target linux
```

`approval-reconnect.test.tsx` leaves `PHILO_TEST_EDIT_SUMMARY` unset. Start `node approval-reconnect-edit.mjs` first. That process is another API client: after the open chat has shown the pending card, it posts one `POST /v1/approvals/{id}/edit`. The window updates to the invalid card and the new snapshot without Reload. Approve is pressed while that decision is still in flight, and the window does not stay on `approved` once the stored card is invalid. Switching to another bot and back, then disconnecting and reconnecting, shows the stored cards. The old snapshot is not approvable again. The fixture is `PHILO_EXECUTION_FIXTURE=dev`. It is not Makez. The bot names must not already be in the list.

```bash
nsh test approval-reconnect.test.tsx --target linux
```

`approval-unset.test.tsx` leaves `PHILO_EXECUTION_FIXTURE` unset. A draft stays `Execution is unavailable.` and the chat has no approval card.

`worker-message.test.tsx` uses `PHILO_EXECUTION_FIXTURE=worker`. That label is not Makez. The desktop sends `/draft`, then Approve. The server stores the pending card from that reply and the running `philo-server` task from the decision route. The task stays `running` while a `philo-fixture-worker` process is alive. The desktop then sends another message through the composer. The same snapshot, requester, and task id stay on that bot. The other bot does not show them. When that process exits, the task shows the fixture result, which is not a model reply. Leave `PHILO_TEST_EDIT_SUMMARY` unset. There is no editor.

```bash
nsh test approval-unset.test.tsx --target linux
```

```bash
nsh test worker-message.test.tsx --target linux
```

`client-restart.test.tsx` opens one bot chat, then kills the Philo process with SIGKILL after the labeled fixture source is committed and starts another process on the same database. The open client catches up by itself. It does not send the message again and it does not press Reconnect. A log armed while the process is down and a log armed after the new process is listening each show once in that bot's chat. The other bot's business cursor stays where it was. The source is `test-fixture-source`, the fixture the server resume already reads. It is not live Makez. Leave `PHILO_EXECUTION_FIXTURE` and `PHILO_MAKEZ_URL` unset. The desktop process needs `PHILO_ACTOR_TOKEN` and `PHILO_RESTART_URL` pointing at `restart-harness.mjs`. Production does not set that URL, so those controls are not in the shipping window. A second test in the same file starts Philo with the fixture unset and checks that a send stays `Execution is unavailable.`

```bash
nsh test client-restart.test.tsx --target linux
```

`publish-crash.test.tsx` is the same two bots and the same labeled fixture. After the during log is armed, Start server runs a process that commits that log and is killed on the publish line, before the client is told. Another process listens on the same database. The open client shows the line once on that bot. The other bot's cursor stays where it was. No second user message is sent. Set `PHILO_PUBLISH_CRASH=1` on the harness. `dlv` must be on `PATH`. The server build has no pause flag. Leave `PHILO_EXECUTION_FIXTURE` and `PHILO_MAKEZ_URL` unset.

```bash
nsh test publish-crash.test.tsx --target linux
```

## Contract notes

List bodies are `{"agents":[]}`, `{"sessions":[]}`, `{"messages":[]}`, `{"approvals":[]}`, `{"tasks":[]}`, or `{"connectors":[]}`. A bare array is accepted too. `message.delta` `text` is appended. A stored message `error` is `{ "code", "message" }`. Assistant completion from the server is status `complete`.
