//! Workspace state. Signals stay private to this module; pages call the methods.

use std::time::Duration;

use nsh_core::platform::Appearance;
use nsh_core::prelude::*;
use nsh_core::storage::Storage;
use tokio::sync::mpsc::{UnboundedSender, unbounded_channel};

use crate::client::{self, WsCmd, WsEvent};
use crate::model::{
    Agent, Approval, Connector, DEFAULT_SERVER, Health, Message, Session, Task, normalize_base,
    sort_sessions_newest_first,
};

const SERVER_KEY: &str = "serverUrl";
const APPEARANCE_KEY: &str = "appearance";

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Route {
    Chat,
    AgentForm,
    Settings,
    Connectors,
    Attachments,
    Notifications,
    Skills,
    Memory,
    Routines,
    ToolCalls,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum AppearanceChoice {
    System,
    Light,
    Dark,
}

impl AppearanceChoice {
    pub fn label(self) -> &'static str {
        match self {
            Self::System => "Appearance: system",
            Self::Light => "Appearance: light",
            Self::Dark => "Appearance: dark",
        }
    }

    pub fn scheme(self) -> Appearance {
        match self {
            Self::System => Appearance::System,
            Self::Light => Appearance::Light,
            Self::Dark => Appearance::Dark,
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::System => "system",
            Self::Light => "light",
            Self::Dark => "dark",
        }
    }

    fn parse(value: &str) -> Self {
        match value {
            "light" => Self::Light,
            "dark" => Self::Dark,
            _ => Self::System,
        }
    }
}

#[derive(Clone, Copy)]
pub struct Store {
    pub server_url: Signal<String>,
    pub server_draft: Signal<String>,
    pub health: Signal<Option<Health>>,
    pub server_error: Signal<Option<String>>,
    pub action_error: Signal<Option<String>>,
    pub agents: Signal<Vec<Agent>>,
    pub sessions: Signal<Vec<Session>>,
    pub messages: Signal<Vec<Message>>,
    pub approvals: Signal<Vec<Approval>>,
    pub tasks: Signal<Vec<Task>>,
    pub connectors: Signal<Vec<Connector>>,
    pub selected_agent: Signal<Option<String>>,
    pub selected_session: Signal<Option<String>>,
    pub editing_agent: Signal<Option<String>>,
    pub route: Signal<Route>,
    pub draft: Signal<String>,
    pub form_name: Signal<String>,
    pub form_instructions: Signal<String>,
    pub form_model: Signal<String>,
    pub appearance: Signal<AppearanceChoice>,
    pub sending: Signal<bool>,
    pub socket_label: Signal<String>,
    /// The third column. Closed matches the two-column chat layout.
    pub details_open: Signal<bool>,
    /// Library tab of the details column. Activity is the other tab.
    pub library_tab: Signal<bool>,
    ws_generation: Signal<u64>,
    transcript_epoch: Signal<u64>,
    ws_out: Stored<Option<UnboundedSender<WsCmd>>>,
}

/// `spawn` cancels a task when its handle is dropped. Long-running work is
/// detached so it lives with the root owner instead.
fn go(future: impl std::future::Future<Output = ()> + 'static) {
    std::mem::forget(spawn(future));
}

impl Store {
    pub fn boot() -> Self {
        let store = Self {
            server_url: signal(DEFAULT_SERVER.to_owned()),
            server_draft: signal(DEFAULT_SERVER.to_owned()),
            health: signal(None),
            server_error: signal(None),
            action_error: signal(None),
            agents: signal(Vec::new()),
            sessions: signal(Vec::new()),
            messages: signal(Vec::new()),
            approvals: signal(Vec::new()),
            tasks: signal(Vec::new()),
            connectors: signal(Vec::new()),
            selected_agent: signal(None),
            selected_session: signal(None),
            editing_agent: signal(None),
            route: signal(Route::Chat),
            draft: signal(String::new()),
            form_name: signal(String::new()),
            form_instructions: signal(String::new()),
            form_model: signal(String::new()),
            appearance: signal(AppearanceChoice::System),
            sending: signal(false),
            socket_label: signal("Events: connecting".to_owned()),
            details_open: signal(false),
            library_tab: signal(false),
            ws_generation: signal(0),
            transcript_epoch: signal(0),
            ws_out: stored(None),
        };
        if let Some(saved) = Storage::get_item(SERVER_KEY)
            && let Ok(url) = normalize_base(&saved)
        {
            store.server_url.set(url.clone());
            store.server_draft.set(url);
        }
        let choice = Storage::get_item(APPEARANCE_KEY)
            .map(|value| AppearanceChoice::parse(&value))
            .unwrap_or(AppearanceChoice::System);
        store.set_appearance(choice);
        store.refresh_all();
        store.supervise();
        let watch = store;
        std::mem::forget(set_interval(Duration::from_secs(15), move || {
            watch.refresh_status();
        }));
        store
    }

    pub fn show(self, route: Route) {
        log::info!("show {route:?}");
        if route == Route::Settings {
            self.server_draft.set(self.server_url.get());
        }
        self.route.set(route);
    }

    pub fn toggle_details(self) {
        self.details_open.update(|open| *open = !*open);
    }

    pub fn set_details(self, open: bool) {
        self.details_open.set(open);
    }

    pub fn show_library(self, library: bool) {
        self.library_tab.set(library);
        self.details_open.set(true);
    }

    /// A control that looks available in the reference, but is not connected.
    pub fn not_available(self, feature: &'static str) {
        self.action_error
            .set(Some(format!("{feature} is not available yet.")));
    }

    pub fn set_appearance(self, choice: AppearanceChoice) {
        self.appearance.set(choice);
        set_color_scheme(choice.scheme());
        let _ = Storage::set_item(APPEARANCE_KEY, choice.as_str());
    }

    pub fn save_server_url(self) {
        match normalize_base(&self.server_draft.get()) {
            Ok(url) => {
                self.server_url.set(url.clone());
                self.server_draft.set(url.clone());
                if let Err(err) = Storage::set_item(SERVER_KEY, &url) {
                    self.action_error
                        .set(Some(format!("could not save the server URL: {err}")));
                } else {
                    self.action_error.set(None);
                }
                self.server_error.set(None);
                self.reconnect();
                self.refresh_all();
            }
            Err(err) => self.action_error.set(Some(err)),
        }
    }

    pub fn refresh_all(self) {
        let store = self;
        go(async move {
            let url = store.server_url.get();
            store.load_status(&url).await;
            if store.server_url.get() != url {
                return;
            }
            match client::list_agents(&url).await {
                Ok(agents) => {
                    store.agents.set(agents);
                    store.server_error.set(None);
                }
                Err(err) => store.server_error.set(Some(err)),
            }
            if let Some(agent_id) = store.selected_agent.get() {
                store.load_sessions(agent_id).await;
            }
            if let Some(session_id) = store.selected_session.get() {
                store.load_transcript(session_id).await;
            }
        });
    }

    pub fn refresh_status(self) {
        let store = self;
        go(async move {
            let url = store.server_url.get();
            store.load_status(&url).await;
        });
    }

    pub fn select_agent(self, id: String) {
        self.selected_agent.set(Some(id.clone()));
        self.selected_session.set(None);
        self.messages.set(Vec::new());
        self.approvals.set(Vec::new());
        self.tasks.set(Vec::new());
        self.route.set(Route::Chat);
        let store = self;
        go(async move {
            store.load_sessions(id).await;
        });
    }

    pub fn select_session(self, id: String) {
        self.selected_session.set(Some(id.clone()));
        self.route.set(Route::Chat);
        self.action_error.set(None);
        self.subscribe(&id);
        let store = self;
        go(async move {
            store.load_transcript(id).await;
        });
    }

    pub fn open_new_agent(self) {
        log::info!("open new agent");
        self.editing_agent.set(None);
        self.form_name.set(String::new());
        self.form_instructions.set(String::new());
        self.form_model.set(String::new());
        self.action_error.set(None);
        self.route.set(Route::AgentForm);
    }

    pub fn open_edit_agent(self) {
        let Some(id) = self.selected_agent.get() else {
            self.action_error
                .set(Some("Select an agent to configure.".to_owned()));
            return;
        };
        let Some(agent) = self
            .agents
            .with(|list| list.iter().find(|agent| agent.id == id).cloned())
        else {
            self.action_error
                .set(Some("That agent is no longer in the list.".to_owned()));
            return;
        };
        self.editing_agent.set(Some(agent.id));
        self.form_name.set(agent.name);
        self.form_instructions.set(agent.instructions);
        self.form_model.set(agent.model_id);
        self.action_error.set(None);
        self.route.set(Route::AgentForm);
    }

    pub fn save_agent(self) {
        let name = self.form_name.get().trim().to_owned();
        let instructions = self.form_instructions.get().trim().to_owned();
        let model_id = self.form_model.get().trim().to_owned();
        if name.is_empty() || model_id.is_empty() {
            self.action_error
                .set(Some("Name and model id are required.".to_owned()));
            return;
        }
        let editing = self.editing_agent.get();
        self.sending.set(true);
        self.action_error.set(None);
        let store = self;
        go(async move {
            let url = store.server_url.get();
            let result = if let Some(id) = editing {
                client::update_agent(&url, &id, &name, &instructions, &model_id).await
            } else {
                client::create_agent(&url, &name, &instructions, &model_id).await
            };
            store.sending.set(false);
            match result {
                Ok(agent) => {
                    let id = agent.id.clone();
                    store.selected_agent.set(Some(id.clone()));
                    store.route.set(Route::Chat);
                    match client::list_agents(&url).await {
                        Ok(agents) => store.agents.set(agents),
                        Err(err) => {
                            store.upsert_agent(agent);
                            store.server_error.set(Some(err));
                        }
                    }
                    store.load_sessions(id).await;
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn create_session(self) {
        log::info!("create session");
        let Some(agent_id) = self.selected_agent.get() else {
            self.action_error
                .set(Some("Create or select an agent first.".to_owned()));
            return;
        };
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::create_session(&store.server_url.get(), &agent_id, "New session").await {
                Ok(session) => {
                    let id = session.id.clone();
                    store.sessions.update(|list| {
                        list.insert(0, session);
                    });
                    store.select_session(id);
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn send(self) {
        log::info!("send");
        let content = self.draft.get().trim().to_owned();
        if content.is_empty() {
            return;
        }
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Start a session before sending.".to_owned()));
            return;
        };
        if self.streaming() {
            self.action_error.set(Some(
                "A reply is still streaming. Cancel it before sending another message.".to_owned(),
            ));
            return;
        }
        if self.sending.get() {
            return;
        }
        self.draft.set(String::new());
        self.sending.set(true);
        self.action_error.set(None);
        let store = self;
        go(async move {
            let result = client::send_message(&store.server_url.get(), &session_id, &content).await;
            store.sending.set(false);
            match result {
                Ok(posted) => {
                    if let Some(message) = posted.user_message {
                        store.upsert_message(message);
                    }
                    if let Some(message) = posted.assistant_message {
                        store.upsert_message(message);
                    }
                    store.load_transcript(session_id).await;
                }
                Err(err) => {
                    store.draft.set(content);
                    store.action_error.set(Some(err));
                }
            }
        });
    }

    pub fn cancel_reply(self) {
        let Some(session_id) = self.selected_session.get() else {
            return;
        };
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::cancel(&store.server_url.get(), &session_id).await {
                Ok(()) => store.load_transcript(session_id).await,
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn retry(self, message_id: String) {
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::retry(&store.server_url.get(), &message_id).await {
                Ok(()) => {
                    if let Some(session_id) = store.selected_session.get() {
                        store.load_transcript(session_id).await;
                    }
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn decide(self, id: String, decision: &'static str) {
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::decide(&store.server_url.get(), &id, decision).await {
                Ok(approval) => {
                    store.upsert_approval(approval);
                    if let Some(session_id) = store.selected_session.get() {
                        store.load_transcript(session_id).await;
                    }
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn reload_transcript(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Select a session to reload.".to_owned()));
            return;
        };
        self.action_error.set(None);
        let store = self;
        go(async move {
            store.load_transcript(session_id).await;
        });
    }

    pub fn streaming(self) -> bool {
        self.messages
            .with(|list| list.iter().any(Message::is_streaming))
    }

    pub fn selected_agent_name(self) -> String {
        let selected = self.selected_agent.get();
        self.agents.with(|list| {
            selected
                .as_ref()
                .and_then(|id| list.iter().find(|agent| &agent.id == id))
                .map(|agent| agent.name.clone())
                .unwrap_or_default()
        })
    }

    pub fn selected_session_label(self) -> String {
        let selected = self.selected_session.get();
        self.sessions.with(|list| {
            selected
                .as_ref()
                .and_then(|id| list.iter().find(|session| &session.id == id))
                .map(Session::label)
                .unwrap_or_default()
        })
    }

    async fn load_status(self, url: &str) {
        match client::health(url).await {
            Ok(health) => {
                if self.server_url.get() == url {
                    self.health.set(Some(health));
                    self.server_error.set(None);
                }
            }
            Err(err) => {
                if self.server_url.get() == url {
                    self.health.set(None);
                    self.server_error.set(Some(err));
                }
            }
        }
        match client::list_connectors(url).await {
            Ok(connectors) => {
                if self.server_url.get() == url {
                    self.connectors.set(connectors);
                }
            }
            Err(err) => {
                if self.server_url.get() == url && self.server_error.get().is_none() {
                    self.server_error.set(Some(err));
                }
            }
        }
    }

    async fn load_sessions(self, agent_id: String) {
        match client::list_sessions(&self.server_url.get(), &agent_id).await {
            Ok(mut sessions) => {
                if self.selected_agent.get().as_deref() == Some(agent_id.as_str()) {
                    sort_sessions_newest_first(&mut sessions);
                    self.sessions.set(sessions);
                }
            }
            Err(err) => self.action_error.set(Some(err)),
        }
    }

    async fn load_transcript(self, session_id: String) {
        let url = self.server_url.get();
        let epoch = self.transcript_epoch.get();
        let messages = client::list_messages(&url, &session_id).await;
        let approvals = client::list_approvals(&url, &session_id).await;
        let tasks = client::list_tasks(&url, &session_id).await;
        if self.selected_session.get().as_deref() != Some(session_id.as_str()) {
            return;
        }
        match messages {
            Ok(messages) if self.transcript_epoch.get() == epoch => self.messages.set(messages),
            Ok(_) => {}
            Err(err) => self.action_error.set(Some(err)),
        }
        if let Ok(approvals) = approvals {
            self.approvals.set(approvals);
        }
        if let Ok(tasks) = tasks {
            self.tasks.set(tasks);
        }
    }

    fn reconnect(self) {
        self.ws_generation.update(|generation| *generation += 1);
        self.ws_out.replace(None);
        self.socket_label.set("Events: connecting".to_owned());
    }

    fn subscribe(self, session_id: &str) {
        self.ws_out.with(|slot| {
            if let Some(sender) = slot {
                let _ = sender.send(WsCmd::Subscribe(session_id.to_owned()));
            }
        });
    }

    fn supervise(self) {
        let store = self;
        go(async move {
            loop {
                let generation = store.ws_generation.get();
                let base = store.server_url.get();
                let (cmd_tx, cmd_rx) = unbounded_channel();
                let (ev_tx, mut ev_rx) = unbounded_channel();
                store.ws_out.replace(Some(cmd_tx));
                store.socket_label.set("Events: connecting".to_owned());
                if let Some(session_id) = store.selected_session.get() {
                    store.subscribe(&session_id);
                }
                client::drive_socket(base, cmd_rx, ev_tx);
                loop {
                    let event = ev_rx.recv().await;
                    if store.ws_generation.get() != generation {
                        break;
                    }
                    match event {
                        Some(WsEvent::Open) => {
                            store.socket_label.set("Events: connected".to_owned());
                            if let Some(session_id) = store.selected_session.get() {
                                store.subscribe(&session_id);
                            }
                        }
                        Some(WsEvent::Server(event)) => store.apply(event),
                        Some(WsEvent::Closed(reason)) => {
                            store
                                .socket_label
                                .set(format!("Events: disconnected: {reason}"));
                            break;
                        }
                        None => {
                            store.socket_label.set("Events: disconnected".to_owned());
                            break;
                        }
                    }
                }
                store.ws_out.replace(None);
                for _ in 0..20 {
                    if store.ws_generation.get() != generation {
                        break;
                    }
                    timer(Duration::from_millis(100)).await;
                }
            }
        });
    }

    fn apply(self, event: crate::model::ServerEvent) {
        use crate::model::ServerEvent;
        match event {
            ServerEvent::Delta {
                message_id,
                session_id,
                text,
            } => self.apply_delta(session_id, message_id, text),
            ServerEvent::Completed {
                message_id,
                session_id,
            } => {
                self.set_status(&session_id, &message_id, "completed", None);
                self.reload_session(session_id);
            }
            ServerEvent::Failed {
                message_id,
                session_id,
                message,
            } => {
                self.set_status(&session_id, &message_id, "error", Some(message.clone()));
                self.action_error.set(Some(message));
                self.reload_session(session_id);
            }
            ServerEvent::Canceled {
                message_id,
                session_id,
            } => {
                self.set_status(&session_id, &message_id, "canceled", None);
                self.reload_session(session_id);
            }
            ServerEvent::Approval(approval) => self.upsert_approval(approval),
            ServerEvent::Task(task) => self.upsert_task(task),
            ServerEvent::Session(session) => self.upsert_session(session),
            ServerEvent::Ignored(kind) => {
                if !kind.is_empty() {
                    log::info!("ignored event {kind}");
                }
            }
        }
    }

    fn apply_delta(self, session_id: String, message_id: String, text: String) {
        if self.selected_session.get().as_deref() != Some(session_id.as_str()) {
            return;
        }
        self.transcript_epoch.update(|epoch| *epoch += 1);
        self.messages.update(|list| {
            if let Some(message) = list.iter_mut().find(|message| message.id == message_id) {
                message.content.push_str(&text);
                if message.status.is_empty() || message.status == "completed" {
                    message.status = "streaming".to_owned();
                }
            } else {
                list.push(Message {
                    id: message_id,
                    session_id,
                    role: "assistant".to_owned(),
                    content: text,
                    status: "streaming".to_owned(),
                    ..Message::default()
                });
            }
        });
    }

    fn set_status(self, session_id: &str, message_id: &str, status: &str, error: Option<String>) {
        if self.selected_session.get().as_deref() != Some(session_id) {
            return;
        }
        self.messages.update(|list| {
            if let Some(message) = list.iter_mut().find(|message| message.id == message_id) {
                message.status = status.to_owned();
                if let Some(text) = error.clone() {
                    message.error = Some(crate::model::StoredError {
                        message: text,
                        ..crate::model::StoredError::default()
                    });
                }
            }
        });
    }

    fn reload_session(self, session_id: String) {
        let store = self;
        go(async move {
            store.load_transcript(session_id).await;
        });
    }

    fn upsert_message(self, message: Message) {
        self.messages.update(|list| {
            if let Some(slot) = list.iter_mut().find(|item| item.id == message.id) {
                *slot = message;
            } else {
                list.push(message);
            }
        });
    }

    fn upsert_agent(self, agent: Agent) {
        self.agents.update(|list| {
            if let Some(slot) = list.iter_mut().find(|item| item.id == agent.id) {
                *slot = agent;
            } else {
                list.push(agent);
            }
        });
    }

    fn upsert_approval(self, approval: Approval) {
        if self.selected_session.get().as_deref() != Some(approval.session_id.as_str())
            && !approval.session_id.is_empty()
        {
            return;
        }
        self.approvals.update(|list| {
            if let Some(slot) = list.iter_mut().find(|item| item.id == approval.id) {
                *slot = approval;
            } else {
                list.push(approval);
            }
        });
    }

    fn upsert_task(self, task: Task) {
        if !task.session_id.is_empty()
            && self.selected_session.get().as_deref() != Some(task.session_id.as_str())
        {
            return;
        }
        self.tasks.update(|list| {
            if task.id.is_empty() {
                list.push(task);
                return;
            }
            if let Some(slot) = list.iter_mut().find(|item| item.id == task.id) {
                *slot = task;
            } else {
                list.push(task);
            }
        });
    }

    fn upsert_session(self, session: Session) {
        if self.selected_agent.get().as_deref() != Some(session.agent_id.as_str())
            && !session.agent_id.is_empty()
        {
            return;
        }
        self.sessions.update(|list| {
            if let Some(slot) = list.iter_mut().find(|item| item.id == session.id) {
                *slot = session;
            } else {
                list.insert(0, session);
            }
        });
    }
}
