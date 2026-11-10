//! HTTP on the engine thread through NSH `fetch`, and the event socket on
//! the runtime's tokio threads (`background`), which is the Rust path the
//! standard-modules page documents for WebSocket.

use futures_util::{SinkExt, StreamExt};
use nsh_core::prelude::*;
use serde_json::{Value, json};
use tokio::sync::mpsc::{UnboundedReceiver, UnboundedSender};
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::HeaderValue;
use tokio_tungstenite::{connect_async, tungstenite::Message as WsMessage};

use crate::model::{
    Agent, Approval, Connector, Health, Message, PostedMessages, ServerEvent, Session, Task,
    endpoint, endpoint_query, events_url, explain_http, parse_agent, parse_approval, parse_event,
    parse_health, parse_json, parse_list, parse_posted, parse_session,
};

pub struct MaterializedSnapshot {
    pub high_water: i64,
    pub messages: Vec<Message>,
    pub approvals: Vec<Approval>,
}

pub enum BusinessReplay {
    Events(Vec<ServerEvent>),
    Expired(MaterializedSnapshot),
}

pub enum WsCmd {
    Subscribe(String),
}

pub enum WsEvent {
    Open,
    Closed(String),
    Server(ServerEvent),
}

pub async fn health(base: &str) -> Result<Health, String> {
    parse_health(&get_json(&endpoint(base, "/v1/health")?).await?)
}

pub async fn list_agents(base: &str) -> Result<Vec<Agent>, String> {
    let value = get_json(&endpoint(base, "/v1/agents")?).await?;
    parse_list(&value, "agents")
}

pub async fn create_agent(
    base: &str,
    name: &str,
    instructions: &str,
    model_id: &str,
) -> Result<Agent, String> {
    let value = send_json(
        "POST",
        &endpoint(base, "/v1/agents")?,
        json!({
            "name": name,
            "instructions": instructions,
            "modelId": model_id,
        }),
    )
    .await?;
    parse_agent(&value)
}

pub async fn update_agent(
    base: &str,
    id: &str,
    name: &str,
    instructions: &str,
    model_id: &str,
) -> Result<Agent, String> {
    let value = send_json(
        "PATCH",
        &endpoint(base, &format!("/v1/agents/{id}"))?,
        json!({
            "name": name,
            "instructions": instructions,
            "modelId": model_id,
        }),
    )
    .await?;
    parse_agent(&value)
}

pub async fn list_messages(base: &str, session_id: &str) -> Result<Vec<Message>, String> {
    let value = get_json(&endpoint(
        base,
        &format!("/v1/sessions/{session_id}/messages"),
    )?)
    .await?;
    let mut messages: Vec<Message> = parse_list(&value, "messages")?;
    crate::model::sort_messages(&mut messages);
    Ok(messages)
}

pub async fn open_conversation(base: &str, agent_id: &str) -> Result<Session, String> {
    let value = send_json(
        "POST",
        &endpoint(base, &format!("/v1/agents/{agent_id}/conversation"))?,
        json!({}),
    )
    .await?;
    parse_session(&value)
}

pub async fn get_conversation(base: &str, agent_id: &str) -> Result<Session, String> {
    let value = get_json(&endpoint(
        base,
        &format!("/v1/agents/{agent_id}/conversation"),
    )?)
    .await?;
    parse_session(&value)
}

pub async fn snapshot(base: &str, session_id: &str) -> Result<MaterializedSnapshot, String> {
    let value = get_json(&endpoint(
        base,
        &format!("/v1/sessions/{session_id}/snapshot"),
    )?)
    .await?;
    parse_snapshot(&value)
}

pub async fn business_events(
    base: &str,
    session_id: &str,
    after: i64,
) -> Result<BusinessReplay, String> {
    let url = endpoint(
        base,
        &format!("/v1/sessions/{session_id}/business-events?after={after}"),
    )?;
    let (status, text) = request("GET", &url, None).await?;
    let value = parse_json(&text)?;
    if status == 400 {
        if let Some(snap) = value.get("snapshot") {
            return Ok(BusinessReplay::Expired(parse_snapshot(snap)?));
        }
        return Err(explain_http(status, &text));
    }
    if status < 200 || status >= 300 {
        return Err(explain_http(status, &text));
    }
    let events = value
        .get("events")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let mut parsed = Vec::new();
    for event in events {
        parsed.push(parse_event(&event.to_string())?);
    }
    Ok(BusinessReplay::Events(parsed))
}

fn parse_snapshot(value: &Value) -> Result<MaterializedSnapshot, String> {
    let mut messages: Vec<Message> = parse_list(value, "messages")?;
    crate::model::sort_messages(&mut messages);
    let approvals: Vec<Approval> = parse_list(value, "approvals").unwrap_or_default();
    let high_water = value
        .get("highWater")
        .and_then(Value::as_i64)
        .ok_or_else(|| "snapshot is missing highWater".to_owned())?;
    Ok(MaterializedSnapshot {
        high_water,
        messages,
        approvals,
    })
}

pub async fn send_message(
    base: &str,
    session_id: &str,
    content: &str,
    idempotency_key: &str,
) -> Result<PostedMessages, String> {
    let value = send_json_headers(
        "POST",
        &endpoint(base, &format!("/v1/sessions/{session_id}/messages"))?,
        json!({ "content": content, "options": {} }),
        &[("Idempotency-Key", idempotency_key)],
    )
    .await?;
    Ok(parse_posted(&value))
}

pub async fn cancel(base: &str, session_id: &str) -> Result<(), String> {
    let _ = send_json(
        "POST",
        &endpoint(base, &format!("/v1/sessions/{session_id}/cancel"))?,
        json!({}),
    )
    .await?;
    Ok(())
}

pub async fn retry(base: &str, message_id: &str) -> Result<(), String> {
    let _ = send_json(
        "POST",
        &endpoint(base, &format!("/v1/messages/{message_id}/retry"))?,
        json!({}),
    )
    .await?;
    Ok(())
}

pub async fn decide(base: &str, id: &str, decision: &str) -> Result<Approval, String> {
    let value = send_json(
        "POST",
        &endpoint(base, &format!("/v1/approvals/{id}/decision"))?,
        json!({ "decision": decision }),
    )
    .await?;
    parse_approval(&value)
}

pub async fn list_tasks(base: &str, session_id: &str) -> Result<Vec<Task>, String> {
    let value = get_json(&endpoint_query(base, "/v1/tasks", "sessionId", session_id)?).await?;
    parse_list(&value, "tasks")
}

pub async fn list_connectors(base: &str) -> Result<Vec<Connector>, String> {
    let value = get_json(&endpoint(base, "/v1/connectors")?).await?;
    parse_list(&value, "connectors")
}

/// Reads the socket until `commands` is dropped or the peer closes it.
pub fn drive_socket(
    base: String,
    mut commands: UnboundedReceiver<WsCmd>,
    events: UnboundedSender<WsEvent>,
) {
    let _join = background(async move {
        let url = match events_url(&base) {
            Ok(url) => url,
            Err(err) => {
                let _ = events.send(WsEvent::Closed(err));
                return;
            }
        };
        log::info!("connecting {url}");
        let mut pending = None;
        let request = match url.into_client_request() {
            Ok(mut request) => {
                if let Some(token) = actor_token() {
                    if let Ok(value) = HeaderValue::from_str(&format!("Bearer {token}")) {
                        request.headers_mut().insert("Authorization", value);
                    }
                }
                request
            }
            Err(err) => {
                let _ = events.send(WsEvent::Closed(err.to_string()));
                return;
            }
        };
        let connect =
            tokio::time::timeout(std::time::Duration::from_secs(10), connect_async(request));
        tokio::pin!(connect);
        let socket = loop {
            tokio::select! {
                cmd = commands.recv() => {
                    match cmd {
                        Some(WsCmd::Subscribe(id)) => pending = Some(id),
                        None => {
                            let _ = events.send(WsEvent::Closed("stopped".to_owned()));
                            return;
                        }
                    }
                }
                result = &mut connect => {
                    match result {
                        Ok(Ok((socket, _))) => break socket,
                        Ok(Err(err)) => {
                            let _ = events.send(WsEvent::Closed(err.to_string()));
                            return;
                        }
                        Err(_) => {
                            let _ = events.send(WsEvent::Closed("timed out connecting".to_owned()));
                            return;
                        }
                    }
                }
            }
        };
        let _ = events.send(WsEvent::Open);
        let (mut write, mut read) = socket.split();
        let (out_tx, mut out_rx) = tokio::sync::mpsc::unbounded_channel();
        if let Some(id) = pending {
            let _ = out_tx.send(text_message(&subscribe_body(&id)));
        }
        loop {
            tokio::select! {
                cmd = commands.recv() => {
                    match cmd {
                        Some(WsCmd::Subscribe(id)) => {
                            let _ = out_tx.send(text_message(&subscribe_body(&id)));
                        }
                        None => {
                            let _ = events.send(WsEvent::Closed("stopped".to_owned()));
                            return;
                        }
                    }
                }
                outgoing = out_rx.recv() => {
                    match outgoing {
                        Some(message) => {
                            if write.send(message).await.is_err() {
                                let _ = events.send(WsEvent::Closed("could not write to the event socket".to_owned()));
                                return;
                            }
                        }
                        None => return,
                    }
                }
                incoming = read.next() => {
                    match incoming {
                        Some(Ok(WsMessage::Ping(payload))) => {
                            let _ = out_tx.send(WsMessage::Pong(payload));
                        }
                        Some(Ok(other)) => {
                            if let Some(closed) = take_message(other, &events) {
                                let _ = events.send(WsEvent::Closed(closed));
                                return;
                            }
                        }
                        Some(Err(err)) => {
                            let _ = events.send(WsEvent::Closed(err.to_string()));
                            return;
                        }
                        None => {
                            let _ = events.send(WsEvent::Closed("connection closed".to_owned()));
                            return;
                        }
                    }
                }
            }
        }
    });
}

fn subscribe_body(session_id: &str) -> String {
    json!({ "type": "subscribe", "sessionId": session_id }).to_string()
}

fn text_message(body: &str) -> WsMessage {
    WsMessage::Text(body.to_owned().into())
}

fn take_message(message: WsMessage, events: &UnboundedSender<WsEvent>) -> Option<String> {
    match message {
        WsMessage::Text(text) => deliver(&text, events),
        WsMessage::Binary(bytes) => match String::from_utf8(bytes.to_vec()) {
            Ok(text) => deliver(&text, events),
            Err(_) => None,
        },
        WsMessage::Ping(_) | WsMessage::Pong(_) | WsMessage::Frame(_) => None,
        WsMessage::Close(frame) => Some(
            frame
                .map(|frame| frame.reason.to_string())
                .filter(|reason| !reason.is_empty())
                .unwrap_or_else(|| "connection closed".to_owned()),
        ),
    }
}

fn deliver(text: &str, events: &UnboundedSender<WsEvent>) -> Option<String> {
    match parse_event(text) {
        Ok(event) => {
            let _ = events.send(WsEvent::Server(event));
            None
        }
        Err(err) => {
            log::warn!("ignored event ({err}): {text}");
            None
        }
    }
}

fn actor_token() -> Option<String> {
    std::env::var("PHILO_ACTOR_TOKEN")
        .ok()
        .map(|token| token.trim().to_owned())
        .filter(|token| !token.is_empty())
}

fn authorize(request: HttpRequest) -> HttpRequest {
    match actor_token() {
        Some(token) => request.header("Authorization", format!("Bearer {token}")),
        None => request,
    }
}

async fn get_json(url: &str) -> Result<Value, String> {
    let (status, text) = request("GET", url, None).await?;
    if !(200..300).contains(&status) {
        return Err(explain_http(status, &text));
    }
    parse_json(&text)
}

async fn send_json(method: &str, url: &str, body: Value) -> Result<Value, String> {
    send_json_headers(method, url, body, &[]).await
}

async fn send_json_headers(
    method: &str,
    url: &str,
    body: Value,
    headers: &[(&str, &str)],
) -> Result<Value, String> {
    let (status, text) = request(method, url, Some((body, headers))).await?;
    if !(200..300).contains(&status) {
        return Err(explain_http(status, &text));
    }
    parse_json(&text)
}

async fn request(
    method: &str,
    url: &str,
    body: Option<(Value, &[(&str, &str)])>,
) -> Result<(u16, String), String> {
    let mut http = authorize(
        HttpRequest::new(method, url)
            .header("Accept", "application/json")
            .header("Content-Type", "application/json"),
    );
    if let Some((_, headers)) = &body {
        for (name, value) in *headers {
            http = http.header(*name, *value);
        }
    }
    if let Some((payload, _)) = body {
        http = http.body(payload.to_string());
    }
    let response = fetch(http).await?;
    Ok((response.status(), response.text()))
}
