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

`PHILO_EXECUTION_FIXTURE=dev` is the isolated fixture. That process shows the development-adapter banner and streams a fixed local reply. It is not a production success. A message that starts with `/draft ` asks that fixture for one pending approval. Approve and Reject post `POST /v1/approvals/{id}/decision`. On the fixture, approve is what lets the server complete a `philo-server` task. Reject does not. Production does not create that approval.

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

The window receives the HTTP 400 itself, replaces the transcript with that snapshot, and does not append the same text again. `Adapter: unavailable` stays up.

## Contract notes

List bodies are `{"agents":[]}`, `{"sessions":[]}`, `{"messages":[]}`, `{"approvals":[]}`, `{"tasks":[]}`, or `{"connectors":[]}`. A bare array is accepted too. `message.delta` `text` is appended. A stored message `error` is `{ "code", "message" }`. Assistant completion from the server is status `complete`.
