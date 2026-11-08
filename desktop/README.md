# Philo desktop

Native Shell client for the phase-1 session server. The app is Rust (`src/lib.rs`). `nsh.json` mounts the `App` root. It does not run the agent or store the transcript.

Slogan: **Your agent, your friend.** Always on for you.

## Requirements

- [NSH](https://nsh.dev) `0.4.0` (`nsh` on `PATH`). Install: `curl -fsSL https://nsh.dev/install.sh | sh`
- Rust 1.85 or newer (edition 2024). This crate was checked with stable.
- Linux: GTK 4.14 or newer (`libgtk-4-dev` to build). macOS 14+ and Windows 10 1809+ use the same sources; this environment ran the GTK window only.

## Run

Start the session server from the `philo/session-api` branch (do not copy it into this branch):

```bash
git fetch origin philo/session-api
git worktree add ../philo-session-api origin/philo/session-api
cd ../philo-session-api
docker compose up --build
```

The API listens on `http://127.0.0.1:8080`. `PHILO_MODEL_ADAPTER` defaults to `dev`. Leave `PHILO_MAKEZ_URL` unset so Makez stays disconnected.

From this directory:

```bash
cargo run
```

That opens a GTK, AppKit, or Win32 window at 1280×800. The same root:

```bash
nsh run
nsh run --window-size 1280x800
```

`nsh run` builds NSH's runner for this machine. `cargo run` uses `src/main.rs`.

Settings shows the server URL. The default is `http://127.0.0.1:8080`. Saving it reconnects the event socket at `ws://127.0.0.1:8080/v1/events`.

## What the window does

With the dev adapter, the banner says this is not a live model. Create an agent (name, instructions, model id), start a session, and send a message. The assistant reply streams over the WebSocket. Reload reads `GET /v1/sessions/{id}/messages` again. Cancel posts `POST /v1/sessions/{id}/cancel`. Retry appears only after an assistant message is `error` or `canceled`.

A message that starts with `/draft ` asks the dev adapter for one pending approval. Approve and Reject post `POST /v1/approvals/{id}/decision`. Approve is what lets the server complete a `philo-server` task. Reject does not.

Connectors shows Makez as not connected, with the server's reason, unless `PHILO_MAKEZ_URL` is set and healthy. Attachments, notifications, skills, memory, routines, tool-call transcripts, and connector sign-in are real pages that say they are not available yet.

Light, Dark, and System call `set_color_scheme`.

## Checks

```bash
cargo test --lib
nsh test App.test.tsx
```

`cargo test --lib` covers JSON parsing for the HTTP and WebSocket contract. `App.test.tsx` opens the shell headless: slogan, settings, the theme control, and a page that is not available yet.

With the server running, the GTK window loop is `session-loop.test.tsx`:

```bash
nsh test session-loop.test.tsx --target linux
```

That test needs `network: "live"` and the server on port 8080. `nsh test` with no file also runs it, so start the server first or pass `App.test.tsx` only.

## Contract notes

List bodies are `{"agents":[]}`, `{"sessions":[]}`, `{"messages":[]}`, `{"approvals":[]}`, `{"tasks":[]}`, or `{"connectors":[]}`. A bare array is accepted too. `message.delta` `text` is appended. A stored message `error` is `{ "code", "message" }`. Assistant completion from the server is status `complete`.
