//! JSON shapes for the phase-1 HTTP and WebSocket contract.
//!
//! Field names are camelCase, as the contract's request bodies are. Unknown
//! fields are ignored. List endpoints accept a bare array or an object
//! whose list lives under the collection name (`agents`, `sessions`, …)
//! or `items`.

use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::Value;

pub const DEFAULT_SERVER: &str = "http://127.0.0.1:8080";

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Health {
    #[serde(default)]
    pub ok: bool,
    #[serde(default)]
    pub adapter: String,
    #[serde(default)]
    pub makez: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Agent {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub instructions: String,
    #[serde(default)]
    pub model_id: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Session {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub agent_id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

impl Session {
    /// Server title. The sidebar does not show it.
    #[allow(dead_code)]
    pub fn label(&self) -> String {
        let title = self.title.trim();
        if title.is_empty() {
            let short: String = self.id.chars().take(8).collect();
            if short.is_empty() {
                "Session".to_owned()
            } else {
                format!("Session {short}")
            }
        } else {
            title.to_owned()
        }
    }
}

/// A stored generation failure. The server sends `{ "code", "message" }`.
#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
pub struct StoredError {
    #[serde(default)]
    pub code: String,
    #[serde(default)]
    pub message: String,
}

impl StoredError {
    pub fn text(&self) -> String {
        match (self.code.is_empty(), self.message.is_empty()) {
            (false, false) => format!("{}: {}", self.code, self.message),
            (false, true) => self.code.clone(),
            (true, false) => self.message.clone(),
            (true, true) => String::new(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Message {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub role: String,
    #[serde(default)]
    pub content: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub error: Option<StoredError>,
    #[serde(default)]
    pub metadata: Option<Value>,
    #[serde(default)]
    pub created_at: String,
}

impl Message {
    pub fn is_user(&self) -> bool {
        self.role == "user"
    }

    pub fn is_streaming(&self) -> bool {
        self.role == "assistant" && self.status == "streaming"
    }

    pub fn can_retry(&self) -> bool {
        self.role == "assistant" && matches!(self.status.as_str(), "error" | "canceled")
    }

    /// Adapter name from message metadata, when the server recorded one.
    pub fn adapter(&self) -> Option<&str> {
        self.metadata
            .as_ref()
            .and_then(|value| value.get("adapter"))
            .and_then(Value::as_str)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Approval {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub summary: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub created_at: String,
}

impl Approval {
    pub fn is_pending(&self) -> bool {
        self.status == "pending"
    }
}

#[derive(Clone, Debug, PartialEq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Task {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub approval_id: String,
    #[serde(default)]
    pub status: String,
    #[serde(default)]
    pub source: String,
    #[serde(default)]
    pub result: Option<Value>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

impl Task {
    pub fn result_text(&self) -> String {
        match &self.result {
            Some(Value::String(text)) => text.clone(),
            Some(Value::Null) | None => String::new(),
            Some(other) => other.to_string(),
        }
    }

    pub fn has_result(&self) -> bool {
        !self.result_text().is_empty()
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Connector {
    #[serde(default)]
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub connected: bool,
    #[serde(default)]
    pub reason: String,
}

impl Connector {
    pub fn label(&self) -> String {
        if !self.name.trim().is_empty() {
            self.name.clone()
        } else if !self.id.trim().is_empty() {
            self.id.clone()
        } else {
            "Connector".to_owned()
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
pub struct ErrorBody {
    #[serde(default)]
    pub code: String,
    #[serde(default)]
    pub message: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Default, Deserialize)]
pub struct ErrorEnvelope {
    #[serde(default)]
    pub error: ErrorBody,
}

#[derive(Clone, Debug, PartialEq, Default)]
pub struct PostedMessages {
    pub user_message: Option<Message>,
    pub assistant_message: Option<Message>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum ServerEvent {
    Delta {
        message_id: String,
        session_id: String,
        text: String,
    },
    Completed {
        message_id: String,
        session_id: String,
    },
    Failed {
        message_id: String,
        session_id: String,
        message: String,
    },
    Canceled {
        message_id: String,
        session_id: String,
    },
    Approval(Approval),
    Task(Task),
    Session(Session),
    Ignored(String),
}

/// `http://127.0.0.1:8080` or the same URL with a trailing `/v1`.
pub fn normalize_base(base: &str) -> Result<String, String> {
    let mut base = base.trim().trim_end_matches('/').to_owned();
    if !(base.starts_with("http://") || base.starts_with("https://")) {
        return Err("Server URL must start with http:// or https://".to_owned());
    }
    if base.ends_with("/v1") {
        base.truncate(base.len() - 3);
        base = base.trim_end_matches('/').to_owned();
    }
    if base.is_empty() {
        return Err("Server URL is empty".to_owned());
    }
    Ok(base)
}

pub fn endpoint(base: &str, path: &str) -> Result<String, String> {
    Ok(format!("{}{path}", normalize_base(base)?))
}

pub fn endpoint_query(base: &str, path: &str, key: &str, value: &str) -> Result<String, String> {
    Ok(format!(
        "{}?{}={}",
        endpoint(base, path)?,
        encode(key),
        encode(value)
    ))
}

/// WebSocket URL for `GET /v1/events`.
pub fn events_url(base: &str) -> Result<String, String> {
    let http = endpoint(base, "/v1/events")?;
    if let Some(rest) = http.strip_prefix("https://") {
        Ok(format!("wss://{rest}"))
    } else if let Some(rest) = http.strip_prefix("http://") {
        Ok(format!("ws://{rest}"))
    } else {
        Err("Server URL must start with http:// or https://".to_owned())
    }
}

pub fn explain_http(status: u16, body: &str) -> String {
    if let Ok(envelope) = serde_json::from_str::<ErrorEnvelope>(body) {
        let ErrorBody { code, message } = envelope.error;
        return match (code.is_empty(), message.is_empty()) {
            (false, false) => format!("{code}: {message}"),
            (false, true) => format!("{code} (HTTP {status})"),
            (true, false) => message,
            (true, true) => format!("HTTP {status}"),
        };
    }
    let body = body.trim();
    if body.is_empty() {
        format!("HTTP {status}")
    } else {
        format!("HTTP {status}: {body}")
    }
}

pub fn parse_json(body: &str) -> Result<Value, String> {
    if body.trim().is_empty() {
        return Ok(Value::Null);
    }
    serde_json::from_str(body).map_err(|err| format!("the response was not JSON: {err}"))
}

pub fn parse_list<T: DeserializeOwned>(value: &Value, key: &str) -> Result<Vec<T>, String> {
    let list = if value.is_array() {
        value
    } else if let Some(found) = value.get(key).filter(|item| item.is_array()) {
        found
    } else if let Some(found) = value.get("items").filter(|item| item.is_array()) {
        found
    } else {
        return Err(format!("expected a list of {key}"));
    };
    serde_json::from_value(list.clone()).map_err(|err| format!("could not read {key}: {err}"))
}

pub fn parse_health(value: &Value) -> Result<Health, String> {
    serde_json::from_value(value.clone()).map_err(|err| format!("could not read health: {err}"))
}

pub fn parse_agent(value: &Value) -> Result<Agent, String> {
    let agent: Agent = parse_nested(value, "agent")?;
    if agent.id.is_empty() {
        return Err("the server returned an agent without an id".to_owned());
    }
    Ok(agent)
}

pub fn parse_session(value: &Value) -> Result<Session, String> {
    let session: Session = parse_nested(value, "session")?;
    if session.id.is_empty() {
        return Err("the server returned a session without an id".to_owned());
    }
    Ok(session)
}

pub fn parse_approval(value: &Value) -> Result<Approval, String> {
    let approval: Approval = parse_nested(value, "approval")?;
    if approval.id.is_empty() {
        return Err("the server returned an approval without an id".to_owned());
    }
    Ok(approval)
}

pub fn parse_posted(value: &Value) -> PostedMessages {
    PostedMessages {
        user_message: value
            .get("userMessage")
            .and_then(|item| serde_json::from_value(item.clone()).ok()),
        assistant_message: value
            .get("assistantMessage")
            .and_then(|item| serde_json::from_value(item.clone()).ok()),
    }
}

pub fn parse_event(text: &str) -> Result<ServerEvent, String> {
    let value: Value = serde_json::from_str(text).map_err(|err| err.to_string())?;
    let kind = value
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_owned();
    match kind.as_str() {
        "message.delta" => Ok(ServerEvent::Delta {
            message_id: required(&value, &["messageId", "message_id"])?,
            session_id: required(&value, &["sessionId", "session_id"])?,
            text: optional(&value, &["text", "delta"]),
        }),
        "message.completed" => Ok(ServerEvent::Completed {
            message_id: required(&value, &["messageId", "message_id"])?,
            session_id: required(&value, &["sessionId", "session_id"])?,
        }),
        "message.error" => Ok(ServerEvent::Failed {
            message_id: required(&value, &["messageId", "message_id"])?,
            session_id: required(&value, &["sessionId", "session_id"])?,
            message: optional(&value, &["message", "error"]),
        }),
        "message.canceled" => Ok(ServerEvent::Canceled {
            message_id: required(&value, &["messageId", "message_id"])?,
            session_id: required(&value, &["sessionId", "session_id"])?,
        }),
        "approval.requested" | "approval.updated" => {
            Ok(ServerEvent::Approval(parse_nested(&value, "approval")?))
        }
        "task.updated" => Ok(ServerEvent::Task(parse_nested(&value, "task")?)),
        "session.updated" => Ok(ServerEvent::Session(parse_nested(&value, "session")?)),
        other => Ok(ServerEvent::Ignored(other.to_owned())),
    }
}

pub fn sort_messages(messages: &mut [Message]) {
    if messages
        .iter()
        .all(|message| !message.created_at.is_empty())
    {
        messages.sort_by(|left, right| {
            left.created_at
                .cmp(&right.created_at)
                .then(left.id.cmp(&right.id))
        });
    }
}

/// One line from the newest message, short enough for a bot row.
pub fn message_summary(content: &str) -> String {
    let line = content
        .lines()
        .rev()
        .find(|line| !line.trim().is_empty())
        .unwrap_or("");
    let flat = line.split_whitespace().collect::<Vec<_>>().join(" ");
    let mut chars = flat.chars();
    let summary: String = chars.by_ref().take(72).collect();
    if chars.next().is_some() {
        format!("{summary}…")
    } else if summary.is_empty() {
        "No messages yet".to_owned()
    } else {
        summary
    }
}

pub fn transcript_summary(messages: &[Message]) -> String {
    messages
        .iter()
        .rev()
        .find(|message| !message.content.trim().is_empty())
        .map(|message| message_summary(&message.content))
        .unwrap_or_else(|| "No messages yet".to_owned())
}

pub fn sort_sessions_newest_first(sessions: &mut [Session]) {
    if sessions
        .iter()
        .all(|session| !session.created_at.is_empty())
    {
        sessions.sort_by(|left, right| {
            right
                .created_at
                .cmp(&left.created_at)
                .then(left.id.cmp(&right.id))
        });
    }
}

fn parse_nested<T: DeserializeOwned>(value: &Value, key: &str) -> Result<T, String> {
    let target = value
        .get(key)
        .filter(|item| item.is_object())
        .unwrap_or(value);
    serde_json::from_value(target.clone()).map_err(|err| format!("could not read {key}: {err}"))
}

fn required(value: &Value, keys: &[&str]) -> Result<String, String> {
    optional_filtered(value, keys).ok_or_else(|| format!("missing {}", keys.join(" or ")))
}

fn optional(value: &Value, keys: &[&str]) -> String {
    optional_filtered(value, keys).unwrap_or_default()
}

fn optional_filtered(value: &Value, keys: &[&str]) -> Option<String> {
    keys.iter()
        .find_map(|key| value.get(*key).and_then(Value::as_str).map(str::to_owned))
}

fn encode(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(byte as char);
            }
            _ => out.push_str(&format!("%{byte:02X}")),
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn server_url_becomes_events_socket() {
        assert_eq!(
            events_url("http://127.0.0.1:8080").unwrap(),
            "ws://127.0.0.1:8080/v1/events"
        );
        assert_eq!(
            events_url("https://philo.example/v1/").unwrap(),
            "wss://philo.example/v1/events"
        );
        assert!(events_url("ftp://nope").is_err());
    }

    #[test]
    fn message_summary_uses_the_last_line() {
        let summary = message_summary(
            "[philo dev adapter] Deterministic local reply. This is not a live model.\nReceived: Hello from Philo",
        );
        assert_eq!(summary, "Received: Hello from Philo");
        assert_eq!(message_summary("   "), "No messages yet");
    }

    #[test]
    fn lists_accept_either_envelope() {
        let wrapped = serde_json::json!({"agents": [{"id": "a", "name": "Ada", "modelId": "dev"}]});
        let agents: Vec<Agent> = parse_list(&wrapped, "agents").unwrap();
        assert_eq!(agents[0].model_id, "dev");

        let bare = serde_json::json!([{"id": "s", "agentId": "a", "title": "Today"}]);
        let sessions: Vec<Session> = parse_list(&bare, "sessions").unwrap();
        assert_eq!(sessions[0].label(), "Today");
    }

    #[test]
    fn http_errors_use_the_envelope() {
        let body = r#"{"error":{"code":"provider_not_configured","message":"missing key"}}"#;
        assert_eq!(
            explain_http(409, body),
            "provider_not_configured: missing key"
        );
    }

    #[test]
    fn events_cover_the_contract() {
        let delta =
            parse_event(r#"{"type":"message.delta","messageId":"m","sessionId":"s","text":"Hi"}"#)
                .unwrap();
        assert_eq!(
            delta,
            ServerEvent::Delta {
                message_id: "m".into(),
                session_id: "s".into(),
                text: "Hi".into(),
            }
        );

        let flat = parse_event(
            r#"{"type":"approval.requested","id":"ap","sessionId":"s","kind":"draft_action","summary":"note","status":"pending","createdAt":"2026-11-08T00:00:00Z"}"#,
        )
        .unwrap();
        let ServerEvent::Approval(approval) = flat else {
            panic!("flat approval");
        };
        assert!(approval.is_pending());
        assert_eq!(approval.summary, "note");

        let nested = parse_event(
            r#"{"type":"task.updated","task":{"id":"t","sessionId":"s","status":"running","source":"philo-server","result":"saved"}}"#,
        )
        .unwrap();
        let ServerEvent::Task(task) = nested else {
            panic!("nested task");
        };
        assert_eq!(task.result_text(), "saved");
        assert_eq!(task.source, "philo-server");
    }

    #[test]
    fn stored_message_error_is_an_object() {
        let value = serde_json::json!({
            "id": "a",
            "role": "assistant",
            "status": "error",
            "error": {"code": "provider_not_configured", "message": "missing key"}
        });
        let message: Message = serde_json::from_value(value).unwrap();
        assert_eq!(
            message.error.as_ref().unwrap().text(),
            "provider_not_configured: missing key"
        );
        assert!(message.can_retry());
    }

    #[test]
    fn posted_messages_and_retry_status() {
        let value = serde_json::json!({
            "userMessage": {"id": "u", "role": "user", "content": "Hi", "status": "complete"},
            "assistantMessage": {"id": "a", "role": "assistant", "content": "", "status": "streaming", "metadata": {"adapter": "dev"}}
        });
        let posted = parse_posted(&value);
        assert_eq!(posted.user_message.unwrap().content, "Hi");
        let assistant = posted.assistant_message.unwrap();
        assert!(assistant.is_streaming());
        assert_eq!(assistant.adapter(), Some("dev"));
        assert!(!assistant.can_retry());

        let mut failed = assistant.clone();
        failed.status = "error".into();
        assert!(failed.can_retry());
    }
}
