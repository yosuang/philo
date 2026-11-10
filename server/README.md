# Philo session server

Phase 1 API. Postgres is the source of truth. The desktop is a client of this API.

This server does not start or complete a run. A Makez URL is only a health probe. The dev adapter is not the default. Set `PHILO_EXECUTION_FIXTURE=dev` only for the isolated local fixture. Makez is reported disconnected unless `PHILO_MAKEZ_URL` answers a health check. Business routes require `Authorization: Bearer` matched by `PHILO_AUTH_CREDENTIALS`. `GET /v1/health` does not. An owner id is not a Makez workspace id.

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
  -H 'authorization: Bearer <token>' \
  -d '{"name":"Ada","instructions":"Be brief.","modelId":"local"}'
```

Open the bot with `POST /v1/agents/<id>/conversation`. That returns the same conversation to every client. Historical sessions remain on `POST /v1/sessions` and are not the default. `GET /v1/sessions/{id}/snapshot` is the transcript at a business high-water.

Run this on localhost. Without `PHILO_AUTH_CREDENTIALS`, business routes answer `authentication_required`. The credential form is `token|actor|owner`, comma-separated. The actor is an audit record. Do not send an owner id as proof of identity.

## Test

From the repo root. With Docker, tests start Postgres 16 through testcontainers. Without Docker, set `PHILO_TEST_DATABASE_URL` to a Postgres the tests may create databases in.

```bash
go test ./...
```

## Environment

| Variable | Default | Role |
| --- | --- | --- |
| `PHILO_DATABASE_URL` | required | Postgres DSN |
| `PHILO_HTTP_ADDR` | `:8080` | Listen address |
| `PHILO_MODEL_ADAPTER` | empty | Ignored as an execution path. `dev` here does not enable the fixture |
| `PHILO_EXECUTION_FIXTURE` | empty | `dev` opts into the isolated fixture. Any other value leaves execution unavailable |
| `PHILO_BUSINESS_FIXTURE` | empty | `fixture-business-source` mounts the labeled fixture event route. It is not Makez |
| `PHILO_AUTH_CREDENTIALS` | empty | `token\|actor\|owner` entries. Empty means business routes reject callers |
| `PHILO_PROVIDER_API_KEY` | empty | Required when adapter is `provider` |
| `PHILO_PROVIDER_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible API |
| `PHILO_MAKEZ_URL` | empty | GET this URL for the Makez health check |
| `PHILO_DEV_CHUNK_DELAY` | `40ms` | Pause between dev stream chunks |

An unknown adapter value refuses to start. `PHILO_MODEL_ADAPTER=provider` does not start a provider call and does not fall back to dev. Health reports `unavailable` unless the dev fixture is on.

## Adapter behavior

`PHILO_EXECUTION_FIXTURE=dev` streams a fixed local reply. Assistant messages carry `metadata.adapter` of `dev`. The text starts with `[philo dev adapter]` and says it is not a live model. A user message that starts with `/draft ` (slash, draft, space) and has a non-empty summary creates one pending approval of kind `draft_action` after the reply finishes. The summary is the rest of the first line. That card stores the summary as its action snapshot and the bearer actor as its requester. An empty summary creates no approval. Canceling before the reply finishes creates no approval. That fixture also completes its local task after an explicit approve. The result does not change the card. It is not production and not acceptance.

Without that fixture, a submit stores the user message, a turn, a wake intent, and an outbox row. Execution is `unavailable`. No assistant stream is started.

## Approval and Makez

`POST /v1/approvals/{id}/decision` with `{"decision":"approve"}` stores the approval as approved and inserts a task `{status:"running", source:"philo-server"}`. Production does not mark that task completed. `reject` stores the approval as rejected and creates no task. The bearer owner must own the conversation. A body field named `owner` or `requester` is not a credential and does not replace the snapshot. `POST /v1/approvals/{id}/edit` with a new summary invalidates that card and stores a new pending card. The old card cannot approve the edited action. Makez is not called. Philo does not declare a run complete.

`GET /v1/connectors` lists Makez. It is connected only when `PHILO_MAKEZ_URL` is set and that GET returns a 2xx status. Otherwise it is disconnected, with the reason on the connector object. A failed check is never reported as connected.

## HTTP

Base path `/v1`. JSON object bodies. Server-generated UUID ids. Timestamps are RFC3339 UTC. Success is HTTP 200. Errors are `{"error":{"code","message"}}` with an HTTP error status.

List responses are `{"agents":[]}`, `{"sessions":[]}`, `{"messages":[]}`, `{"approvals":[]}`, `{"tasks":[]}`, and `{"connectors":[]}`.

`POST /v1/agents/{id}/conversation` returns the bot's one default conversation, including when two clients open it together. `GET` returns it without creating one.

`POST /v1/sessions/{id}/messages` requires `Idempotency-Key` and `{"content","options"}`. The same scope and the same semantic payload return the same turn and add nothing. A different payload is `409 idempotency_conflict` and adds nothing. The key is not the turn id. Unknown JSON fields are rejected. Without the dev fixture the response is `{"turnId","replayed","execution":"unavailable","userMessage"}`.

`GET /v1/sessions/{id}/snapshot` is the materialized transcript and `highWater`. It does not include the business-event log. `GET /v1/sessions/{id}/business-events?after=` returns later events. An expired cursor is `400 invalid_request` with a replacement snapshot. Message order is `seq`. A status change does not allocate a new `seq`. Stored status `complete` is the event `message.completed`.

`POST /v1/sessions/{id}/cancel` cancels the streaming reply. The stored body is the text received so far, and the status is `canceled`.

`POST /v1/messages/{id}/retry` is only for an assistant message in `error` or `canceled`. It clears that row and streams again.

Assistant messages include `metadata.adapter`. Failures include `error.code` and `error.message` on the stored message. The WebSocket `message.error` event has `messageId`, `sessionId`, `message`, and `code`.

`message.delta` `text` is the new piece, not the full body. `approval.requested`, `approval.updated`, `task.updated`, and `session.updated` are the full object plus `type`. Approvals also include `messageId` of the assistant message that asked for the draft.

## Not in this server

Attachments, notifications, skills, memory, routines, connector OAuth, and Makez execution are not implemented. There are no sample agents, sample chats, or fake tool traces. Idempotency retention, the owner-to-Makez-workspace binding, and source-offset recovery are not decided here. The approval card does not choose a production identity, a lease, or the field that binds a snapshot to a Run. This server does not ingest Makez or recover an attempt.
