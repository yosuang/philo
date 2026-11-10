# Philo desktop feature status

Phase 1 client against the shared HTTP and WebSocket contract. Status is what this app does, not a claim that a server was present when the window was captured.

| # | Surface | Status | Evidence |
| --- | --- | --- | --- |
| 1 | Multi-agent sidebar | implemented | The sidebar lists bots from `GET /v1/agents`. Each row has an initial, the name, a summary of the latest message, and a selected state. Choosing a bot opens its chat. There is no session list. Empty until the server returns bots. |
| 2 | Create and configure an agent | implemented | New bot and Configure bot post `POST /v1/agents` or `PATCH /v1/agents/{id}` with name, instructions, and model id. Creating a bot lands in its chat. |
| 3 | Session history and message stream | implemented | The user does not create or pick a session. Opening a bot resumes its newest server session, or posts one `POST /v1/sessions` when it has none, and reuses that session. Older extra sessions are left in place. The transcript is an inverted virtual list filled from `GET /v1/sessions/{id}/messages`. Reload fetches that list again. |
| 4 | Streaming reply, cancel, retry | implemented | Send posts `POST /v1/sessions/{id}/messages`. `message.delta` appends text. Cancel posts `POST /v1/sessions/{id}/cancel`. Retry is shown only for assistant messages in `error` or `canceled` and posts `POST /v1/messages/{id}/retry`. |
| 5 | Background task status and result | implemented | Tasks from `GET /v1/tasks?sessionId=` and `task.updated` show source, status, and result. |
| 6 | Tool-call display | not implemented | Tool calls is a details-column page that says it is not available yet. The only live action is the approval-backed draft in the bot's chat. |
| 7 | Approval and draft cards | implemented | The chat renders the card returned by the API and the store, including the action snapshot and the stored status. Approve and Reject are shown only for a pending card that still has a snapshot, and they post `POST /v1/approvals/{id}/decision`. There is no desktop editor. An edit from another client updates the open chat to the invalid card and the new snapshot. A later stored write replaces the card on screen. An older or failed decision response does not paint a card approved while the stored status is pending or invalid. Switching bots or reconnecting shows the stored cards. An invalid card cannot be approved. |
| 8 | Attachments and files | not implemented | The Attachments page is navigation only and says file upload is not available yet. |
| 9 | Notifications | not implemented | The Notifications page says delivery is not available yet. |
| 10 | Settings and theme | implemented | Settings edits the server URL (default `http://127.0.0.1:8080`) and calls `set_color_scheme` for light, dark, or system. A development adapter shows the banner "Development adapter. This is not a live model." |
| 11 | Connector status and auth | implemented | Connectors reads `GET /v1/connectors` and `GET /v1/health`. Makez shows not connected, with the server's reason when one is returned. Sign-in on that page says it is not available yet. |
| 12 | Skills, memory, routines | not implemented | Each has a sidebar destination and a page that says it is not available yet. |
