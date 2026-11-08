# Philo session server

Phase 1 API. Postgres is the source of truth. The desktop is a client of this API and is not in this branch.

Local default adapter is `dev`: a deterministic reply, not a live model. Makez is reported disconnected unless `PHILO_MAKEZ_URL` answers a health check.

## Run

From the repo root:

```bash
docker compose up --build
```

The API listens on `http://127.0.0.1:8080`. Postgres is published on `127.0.0.1:5432` with the local-only user and password `philo`. Do not reuse that password. Do not put `PHILO_PROVIDER_API_KEY` in a file that gets committed.

```bash
curl -s http://127.0.0.1:8080/v1/health
curl -s -X POST http://127.0.0.1:8080/v1/agents \
  -H 'content-type: application/json' \
  -d '{"name":"Ada","instructions":"Be brief.","modelId":"dev-local"}'
```

Create a session with `{"agentId":"<id>"}`, then connect a WebSocket to `GET /v1/events` and send `{"type":"subscribe","sessionId":"<id>"}`. Wait for `{"type":"subscribed",...}` before posting a message if you need the first deltas. Deltas are not replayed. `GET /v1/sessions/{id}/messages` is the transcript after a reload, including text saved before a cancel.

There is no authentication. Run this on localhost.

## Test

From the repo root. Docker must be running; tests start Postgres 16 with testcontainers.

```bash
go test ./...
```

## Environment

| Variable | Default | Role |
| --- | --- | --- |
| `PHILO_DATABASE_URL` | required | Postgres DSN |
| `PHILO_HTTP_ADDR` | `:8080` | Listen address |
| `PHILO_MODEL_ADAPTER` | `dev` | `dev` or `provider` |
| `PHILO_PROVIDER_API_KEY` | empty | Required when adapter is `provider` |
| `PHILO_PROVIDER_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible API |
| `PHILO_MAKEZ_URL` | empty | GET this URL for the Makez health check |
| `PHILO_DEV_CHUNK_DELAY` | `40ms` | Pause between dev stream chunks |

An unknown adapter value refuses to start. Provider mode never calls the dev adapter.

## Adapter behavior

`PHILO_MODEL_ADAPTER=dev` streams a fixed local reply. Assistant messages carry `metadata.adapter` of `dev`. The text starts with `[philo dev adapter]` and says it is not a live model. A user message that starts with `/draft ` (slash, draft, space) and has a non-empty summary creates one pending approval of kind `draft_action` after the reply finishes. The summary is the rest of the first line. An empty summary creates no approval. Canceling before the reply finishes creates no approval.

`PHILO_MODEL_ADAPTER=provider` calls `POST {PHILO_PROVIDER_BASE_URL}/chat/completions` with the agent `modelId`, the agent instructions, and the current user message. The call times out after 2 minutes. `/draft` is ordinary text and does not create an approval. If `PHILO_PROVIDER_API_KEY` is missing, the assistant message ends in `error` with code `provider_not_configured`. `GET /v1/health` then reports `adapter: "unconfigured"`. A provider HTTP or stream failure uses code `provider_error`. Neither path falls back to dev.

## Approval and Makez

`POST /v1/approvals/{id}/decision` with `{"decision":"approve"}` stores the approval as approved, inserts a task `{status:"running", source:"philo-server"}`, then stores a completed result from this process. `reject` stores the approval as rejected and creates no task. Makez is not called.

`GET /v1/connectors` lists Makez. It is connected only when `PHILO_MAKEZ_URL` is set and that GET returns a 2xx status. Otherwise it is disconnected, with the reason on the connector object. A failed check is never reported as connected.

## HTTP

Base path `/v1`. JSON object bodies. Server-generated UUID ids. Timestamps are RFC3339 UTC. Success is HTTP 200. Errors are `{"error":{"code","message"}}` with an HTTP error status.

List responses are `{"agents":[]}`, `{"sessions":[]}`, `{"messages":[]}`, `{"approvals":[]}`, `{"tasks":[]}`, and `{"connectors":[]}`.

`POST /v1/sessions/{id}/messages` with `{"content":"..."}` returns `{"userMessage","assistantMessage"}`. The assistant status in that response is `streaming`. Generation has already started. A second message in the same session while one is streaming is `409 reply_in_progress`.

`POST /v1/sessions/{id}/cancel` cancels the streaming reply. The stored body is the text received so far, and the status is `canceled`.

`POST /v1/messages/{id}/retry` is only for an assistant message in `error` or `canceled`. It clears that row and streams again.

Assistant messages include `metadata.adapter`. Failures include `error.code` and `error.message` on the stored message. The WebSocket `message.error` event has `messageId`, `sessionId`, `message`, and `code`.

`message.delta` `text` is the new piece, not the full body. `approval.requested`, `approval.updated`, `task.updated`, and `session.updated` are the full object plus `type`. Approvals also include `messageId` of the assistant message that asked for the draft.

## Not in this server

Attachments, notifications, skills, memory, routines, connector OAuth, and Makez execution are not implemented. There are no sample agents, sample chats, or fake tool traces. The dev draft task is the only task, and it runs inside this process after an explicit approve.
