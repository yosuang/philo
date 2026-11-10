//! Workspace state. Signals stay private to this module; pages call the methods.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use nsh_core::platform::Appearance;
use nsh_core::prelude::*;
use nsh_core::storage::Storage;
use tokio::sync::Mutex;
use tokio::sync::mpsc::{UnboundedSender, unbounded_channel};

use crate::client::{self, BusinessReplay, WsCmd, WsEvent};
use crate::model::{
    Agent, Approval, Connector, DEFAULT_SERVER, Health, Message, Session, Task,
    event_inside_snapshot, normalize_base, replace_transcript, sort_messages,
    stored_status_for_event, transcript_summary,
};

const SERVER_KEY: &str = "serverUrl";
const APPEARANCE_KEY: &str = "appearance";

/// A transcript read waits this long for the subscribe ack, then reads anyway.
const SUBSCRIBE_ACK_BUDGET: Duration = Duration::from_secs(5);
const SUBSCRIBE_ACK_SLICE: Duration = Duration::from_millis(100);

fn subscribe_ack_polls() -> u32 {
    let polls = SUBSCRIBE_ACK_BUDGET.as_millis() / SUBSCRIBE_ACK_SLICE.as_millis();
    u32::try_from(polls).unwrap_or(50)
}

/// The hub does not deliver a publish until the socket is subscribed. An
/// event committed after an unsubscribed read is in neither that response
/// nor the socket. `true` means that event is missing unless something
/// reads the log again.
#[cfg(test)]
fn event_missed_without_another_read(
    ack_at: Option<Duration>,
    event_at: Duration,
    budget: Duration,
) -> bool {
    let read_at = match ack_at {
        Some(ack) if ack <= budget => ack,
        _ => budget,
    };
    let in_read = event_at <= read_at;
    let on_socket = ack_at.is_some_and(|ack| event_at >= ack);
    !in_read && !on_socket
}

/// A later read includes an event committed at or before it. The retry is
/// not gated on the subscribe ack.
#[cfg(test)]
fn retry_read_includes(event_at: Duration, retry_at: Duration) -> bool {
    event_at <= retry_at
}

/// `connected` and `recovered` are reported only after a read that started
/// while this socket was subscribed and that read succeeded. An unsubscribed
/// read is not that catch-up, even when it returned rows.
fn report_synced(read_started_subscribed: bool, read_ok: bool) -> bool {
    read_started_subscribed && read_ok
}

fn extra_read_after_unready(subscribed_before: bool, subscribed_after: bool) -> bool {
    !subscribed_before && subscribed_after
}

/// Capped exponential backoff for one failed business read.
/// The AWS SDK calls the same shape `exponentialDelayWithoutJitter`:
/// `min(cap, base * 2^retry)`, first retry at the base.
/// Full jitter can sleep zero, which does not bound a single client's rate.
/// The base is 1s, the standard-mode delay for a service that is refusing
/// work. The cap is that mode's 20s maximum.
const READ_RETRY_BASE: Duration = Duration::from_secs(1);
const READ_RETRY_CAP: Duration = Duration::from_secs(20);

fn read_retry_delay(retry: u32) -> Duration {
    let shift = retry.min(16);
    READ_RETRY_BASE
        .saturating_mul(1_u32 << shift)
        .min(READ_RETRY_CAP)
}

const LABEL_CATCHING_UP: &str = "Events: catching up";
const LABEL_FAILED: &str = "Events: subscribe failed";
const LABEL_CONNECTED: &str = "Events: connected";
const LABEL_RECOVERED: &str = "Events: recovered";
const LABEL_RECONNECTING: &str = "Events: reconnecting";

struct LiveGate {
    buffering: bool,
    /// Socket generation that opened this buffer. A newer socket discards it.
    generation: u64,
    pending: Vec<crate::model::ServerEvent>,
}

struct ReplayStep {
    saw_expired: bool,
    next: Option<i64>,
}

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
    /// Latest-message line for each bot, keyed by agent id. Not a session list.
    pub previews: Signal<HashMap<String, String>>,
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
    /// Set when this chat was replaced from an expired-cursor snapshot.
    pub rebuilt_from_snapshot: Signal<bool>,
    /// `PHILO_FIXTURE_SUFFIX=1` shows the labeled fixture control. Not Makez.
    fixture_suffix: Signal<bool>,
    /// Set after that control's POST is stored. The transcript is unchanged.
    pub fixture_suffix_note: Signal<String>,
    /// The third column. Closed matches the two-column chat layout.
    pub details_open: Signal<bool>,
    /// Library tab of the details column. Activity is the other tab.
    pub library_tab: Signal<bool>,
    ws_generation: Signal<u64>,
    transcript_epoch: Signal<u64>,
    /// Bumps when the open bot changes so an older resume cannot overwrite it.
    open_generation: Signal<u64>,
    /// One open at a time, so two local callers share the server default conversation.
    resume_lock: Stored<Arc<Mutex<()>>>,
    ws_out: Stored<Option<UnboundedSender<WsCmd>>>,
    /// When set, the supervisor leaves the socket down until reconnect.
    socket_held: Signal<bool>,
    /// Session id from the latest `subscribed` ack. Empty when the socket is down.
    live_subscribed: Signal<String>,
    /// The last business read started before this socket was subscribed.
    read_unsubscribed: Signal<bool>,
    /// This socket already finished a read that started unsubscribed.
    subscribe_failed: Signal<bool>,
    /// Cancels a subscribe retry that has not entered `load_transcript`.
    retry_ticket: Signal<u64>,
    /// How many failed business reads are waiting to be retried. Reset
    /// when a read succeeds, or when this chat is left.
    read_attempt: Signal<u32>,
    /// Business-log HTTP calls this process has started. The fixture pane
    /// shows it so a test can measure the retry rate.
    business_reads: Signal<u64>,
    previous_session: Signal<Option<String>>,
    assistant_ids: Signal<HashMap<String, String>>,
    live_gate: Stored<Arc<std::sync::Mutex<LiveGate>>>,
    catchup_lock: Stored<Arc<Mutex<()>>>,
    business_high_water: Signal<i64>,
    applied_event_ids: Stored<HashSet<String>>,
    submit_serial: Signal<u64>,
    pending_submit: Stored<Option<(String, String, String)>>,
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
            previews: signal(HashMap::new()),
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
            rebuilt_from_snapshot: signal(false),
            fixture_suffix: signal(
                std::env::var("PHILO_FIXTURE_SUFFIX").ok().as_deref() == Some("1"),
            ),
            fixture_suffix_note: signal(String::new()),
            details_open: signal(false),
            library_tab: signal(false),
            ws_generation: signal(0),
            transcript_epoch: signal(0),
            open_generation: signal(0),
            resume_lock: stored(Arc::new(Mutex::new(()))),
            ws_out: stored(None),
            socket_held: signal(false),
            live_subscribed: signal(String::new()),
            read_unsubscribed: signal(false),
            subscribe_failed: signal(false),
            retry_ticket: signal(0),
            read_attempt: signal(0),
            business_reads: signal(0),
            previous_session: signal(None),
            assistant_ids: signal(HashMap::new()),
            live_gate: stored(Arc::new(std::sync::Mutex::new(LiveGate {
                buffering: false,
                generation: 0,
                pending: Vec::new(),
            }))),
            catchup_lock: stored(Arc::new(Mutex::new(()))),
            business_high_water: signal(0),
            applied_event_ids: stored(HashSet::new()),
            submit_serial: signal(0),
            pending_submit: stored(None),
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
            if let Some(session_id) = store.selected_session.get() {
                store.load_transcript(session_id).await;
            } else if let Some(agent_id) = store.selected_agent.get() {
                let generation = store.open_generation.get();
                store.resume_bot(agent_id, generation).await;
            }
            store.load_previews().await;
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
        let same = self.selected_agent.get().as_deref() == Some(id.as_str());
        self.route.set(Route::Chat);
        self.action_error.set(None);
        if same && self.selected_session.get().is_some() {
            return;
        }
        if !same {
            if let Some(previous) = self.selected_session.get() {
                self.remember_assistant(&previous);
                self.previous_session.set(Some(previous));
            }
            self.cancel_read_retry();
            self.subscribe_failed.set(false);
            self.open_generation.update(|generation| *generation += 1);
            self.selected_agent.set(Some(id.clone()));
            self.selected_session.set(None);
            self.messages.set(Vec::new());
            self.approvals.set(Vec::new());
            self.tasks.set(Vec::new());
            self.rebuilt_from_snapshot.set(false);
            self.business_high_water.set(0);
            self.applied_event_ids.with(|seen| seen.clear());
        }
        let generation = self.open_generation.get();
        let store = self;
        go(async move {
            store.resume_bot(id, generation).await;
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
                .set(Some("Choose a bot to configure.".to_owned()));
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
            let created = editing.is_none();
            let result = if let Some(id) = editing {
                client::update_agent(&url, &id, &name, &instructions, &model_id).await
            } else {
                client::create_agent(&url, &name, &instructions, &model_id).await
            };
            store.sending.set(false);
            match result {
                Ok(agent) => {
                    let id = agent.id.clone();
                    store.open_generation.update(|generation| *generation += 1);
                    let generation = store.open_generation.get();
                    store.selected_agent.set(Some(id.clone()));
                    store.route.set(Route::Chat);
                    if created {
                        store.selected_session.set(None);
                        store.messages.set(Vec::new());
                        store.approvals.set(Vec::new());
                        store.tasks.set(Vec::new());
                        store.rebuilt_from_snapshot.set(false);
                        store.business_high_water.set(0);
                        store.applied_event_ids.with(|seen| seen.clear());
                    }
                    match client::list_agents(&url).await {
                        Ok(agents) => store.agents.set(agents),
                        Err(err) => {
                            store.upsert_agent(agent);
                            store.server_error.set(Some(err));
                        }
                    }
                    store.resume_bot(id, generation).await;
                    store.load_previews().await;
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
        let Some(agent_id) = self.selected_agent.get() else {
            self.action_error
                .set(Some("Create or choose a bot before sending.".to_owned()));
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
        let known = self.selected_session.get();
        let generation = self.open_generation.get();
        self.draft.set(String::new());
        self.sending.set(true);
        self.action_error.set(None);
        let store = self;
        go(async move {
            let session_id = if let Some(id) = known {
                id
            } else {
                store.resume_bot(agent_id.clone(), generation).await;
                if store.selected_agent.get().as_deref() != Some(agent_id.as_str()) {
                    store.sending.set(false);
                    return;
                }
                match store.selected_session.get() {
                    Some(id) => id,
                    None => {
                        store.sending.set(false);
                        store.draft.set(content);
                        if store.action_error.get().is_none() {
                            store
                                .action_error
                                .set(Some("This bot's chat is not ready yet.".to_owned()));
                        }
                        return;
                    }
                }
            };
            if store.selected_agent.get().as_deref() != Some(agent_id.as_str())
                || store.selected_session.get().as_deref() != Some(session_id.as_str())
            {
                store.sending.set(false);
                return;
            }
            let key = store.submit_key(&session_id, &content);
            let result =
                client::send_message(&store.server_url.get(), &session_id, &content, &key).await;
            store.sending.set(false);
            match result {
                Ok(posted) => {
                    store.clear_submit(&session_id, &content);
                    if store.selected_session.get().as_deref() != Some(session_id.as_str()) {
                        return;
                    }
                    let unavailable =
                        posted.assistant_message.is_none() && posted.execution == "unavailable";
                    if let Some(message) = posted.user_message {
                        store.upsert_message(message);
                    }
                    if let Some(message) = posted.assistant_message {
                        store.upsert_message(message);
                    }
                    if unavailable {
                        store
                            .action_error
                            .set(Some("Execution is unavailable.".to_owned()));
                    }
                    store.remember_preview();
                    store.load_transcript(session_id).await;
                }
                Err(err) => {
                    if store.selected_agent.get().as_deref() == Some(agent_id.as_str()) {
                        store.draft.set(content);
                        store.action_error.set(Some(err));
                    }
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

    pub fn business_cursor(self) -> i64 {
        self.business_high_water.get()
    }

    /// `message.seq` of the newest stored row. This is not the business cursor.
    pub fn message_seq(self) -> i64 {
        self.messages.with(|list| {
            list.iter()
                .rev()
                .find(|message| message.seq > 0)
                .map(|message| message.seq)
                .unwrap_or(0)
        })
    }

    pub fn fixture_suffix_available(self) -> bool {
        self.fixture_suffix.get()
    }

    pub fn business_reads(self) -> u64 {
        self.business_reads.get()
    }

    /// Ask the labeled fixture to replace the retained log so the cursor this
    /// client holds is expired, and to append one suffix after that snapshot.
    /// The transcript on screen is left unchanged until the next read.
    pub fn stage_expired_cursor(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error.set(Some(
                "Choose a bot before staging an expired cursor.".to_owned(),
            ));
            return;
        };
        self.submit_serial.update(|n| *n += 1);
        let key = format!("fixture-expire-{}", self.submit_serial.get());
        self.fixture_suffix_note.set(String::new());
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::post_fixture_stage_expired(&store.server_url.get(), &session_id, &key)
                .await
            {
                Ok(()) => {
                    log::info!("expired cursor staged session={session_id} key={key}");
                    if store.selected_session.get().as_deref() == Some(session_id.as_str()) {
                        store
                            .fixture_suffix_note
                            .set("Expired cursor staged".to_owned());
                    }
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    /// Store one `fixture-business-source` delta on the open message.
    /// A socket that is not subscribed does not receive the publish.
    /// Reconnect reads the row from the log.
    pub fn post_fixture_suffix(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error.set(Some(
                "Choose a bot before posting a fixture suffix.".to_owned(),
            ));
            return;
        };
        let message_id = self.messages.with(|list| {
            list.iter()
                .rev()
                .find(|message| message.role != "user" && !message.id.is_empty())
                .map(|message| message.id.clone())
        });
        let Some(message_id) = message_id else {
            self.action_error.set(Some(
                "The chat has no fixture message to extend.".to_owned(),
            ));
            return;
        };
        self.submit_serial.update(|n| *n += 1);
        let key = format!("fixture-suffix-{}", self.submit_serial.get());
        self.fixture_suffix_note.set(String::new());
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::post_fixture_delta(
                &store.server_url.get(),
                &session_id,
                &message_id,
                "suffixonce",
                &key,
            )
            .await
            {
                Ok(()) => {
                    log::info!(
                        "fixture suffix stored session={session_id} message={message_id} key={key}"
                    );
                    if store.selected_session.get().as_deref() == Some(session_id.as_str()) {
                        store
                            .fixture_suffix_note
                            .set("Fixture suffix posted".to_owned());
                    }
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    pub fn hold_subscribe(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Choose a bot before holding subscribe.".to_owned()));
            return;
        };
        self.fixture_action("Subscribe hold armed", move |store| {
            let url = store.server_url.get();
            async move { client::post_fixture_hold(&url, &session_id).await }
        });
    }

    pub fn hold_next_subscribe(self) {
        self.fixture_action("Next subscribe hold armed", |store| {
            let url = store.server_url.get();
            async move { client::post_fixture_hold_next(&url).await }
        });
    }

    pub fn release_subscribe(self) {
        self.fixture_action("Subscribe released", |store| {
            let url = store.server_url.get();
            async move { client::post_fixture_release(&url).await }
        });
    }

    pub fn fail_reads(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Choose a bot before failing reads.".to_owned()));
            return;
        };
        self.fixture_action("Reads failing", move |store| {
            let url = store.server_url.get();
            async move { client::post_fixture_fail_reads(&url, &session_id).await }
        });
    }

    pub fn release_reads(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Choose a bot before releasing reads.".to_owned()));
            return;
        };
        self.fixture_action("Reads released", move |store| {
            let url = store.server_url.get();
            async move { client::post_fixture_release_reads(&url, &session_id).await }
        });
    }

    pub fn post_gap_event(self) {
        self.post_labeled_delta(None, "gaponce", "Gap event posted");
    }

    pub fn post_foreign_event(self) {
        let Some(session_id) = self.previous_session.get() else {
            self.action_error.set(Some(
                "Open another bot before posting a foreign event.".to_owned(),
            ));
            return;
        };
        self.post_labeled_delta(Some(session_id), "foreignonce", "Foreign event posted");
    }

    pub fn post_stream_delta(self) {
        self.post_engine_text(None, "streambit", "Stream delta posted");
    }

    pub fn post_old_socket_delta(self) {
        self.post_engine_text(None, "oldsock", "Old socket delta posted");
    }

    pub fn post_foreign_stream(self) {
        let Some(session_id) = self.previous_session.get() else {
            self.action_error.set(Some(
                "Open another bot before posting a foreign stream.".to_owned(),
            ));
            return;
        };
        self.post_engine_text(Some(session_id), "otherbit", "Foreign stream posted");
    }

    pub fn stage_during_read(self) {
        let Some(session_id) = self.selected_session.get() else {
            self.action_error
                .set(Some("Choose a bot before staging a read.".to_owned()));
            return;
        };
        let Some(message_id) = self.assistant_message_id(Some(&session_id)) else {
            self.action_error.set(Some(
                "The chat has no fixture message to extend.".to_owned(),
            ));
            return;
        };
        self.submit_serial.update(|n| *n += 1);
        let key = format!("fixture-during-{}", self.submit_serial.get());
        self.fixture_suffix_note.set(String::new());
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::post_stage_during_read(
                &store.server_url.get(),
                &session_id,
                &message_id,
                "midonce",
                &key,
            )
            .await
            {
                Ok(()) => {
                    log::info!("during read staged session={session_id} key={key}");
                    store
                        .fixture_suffix_note
                        .set("During read staged".to_owned());
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    /// The open socket becomes the old one, but it stays up until the next
    /// frame. That frame is ignored. A new socket opens after it.
    pub fn abandon_socket(self) {
        if self.selected_session.get().is_none() {
            self.action_error.set(Some(
                "Choose a bot before abandoning the socket.".to_owned(),
            ));
            return;
        }
        self.live_subscribed.set(String::new());
        self.ws_generation.update(|generation| *generation += 1);
        self.socket_label.set(LABEL_RECONNECTING.to_owned());
        log::info!("socket generation abandoned; the next frame is from the old socket");
    }

    fn fixture_action<F, Fut>(self, note: &'static str, call: F)
    where
        F: FnOnce(Store) -> Fut + 'static,
        Fut: std::future::Future<Output = Result<(), String>> + 'static,
    {
        self.fixture_suffix_note.set(String::new());
        self.action_error.set(None);
        let store = self;
        go(async move {
            match call(store).await {
                Ok(()) => {
                    log::info!("{note}");
                    store.fixture_suffix_note.set(note.to_owned());
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    fn post_labeled_delta(
        self,
        session_id: Option<String>,
        text: &'static str,
        note: &'static str,
    ) {
        let Some(session_id) = session_id.or_else(|| self.selected_session.get()) else {
            self.action_error.set(Some(
                "Choose a bot before posting a fixture event.".to_owned(),
            ));
            return;
        };
        let Some(message_id) = self.assistant_message_id(Some(session_id.as_str())) else {
            self.action_error.set(Some(
                "The chat has no fixture message to extend.".to_owned(),
            ));
            return;
        };
        self.submit_serial.update(|n| *n += 1);
        let key = format!("fixture-gap-{}", self.submit_serial.get());
        self.clear_fixture_note();
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::post_fixture_delta(
                &store.server_url.get(),
                &session_id,
                &message_id,
                text,
                &key,
            )
            .await
            {
                Ok(()) => {
                    log::info!("fixture delta stored session={session_id} text={text} key={key}");
                    store.set_fixture_note(note);
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    fn post_engine_text(self, session_id: Option<String>, text: &'static str, note: &'static str) {
        let Some(session_id) = session_id.or_else(|| self.selected_session.get()) else {
            self.action_error.set(Some(
                "Choose a bot before posting a stream delta.".to_owned(),
            ));
            return;
        };
        let message_id = self
            .assistant_message_id(Some(session_id.as_str()))
            .unwrap_or_default();
        self.clear_fixture_note();
        self.action_error.set(None);
        let store = self;
        go(async move {
            match client::post_engine_delta(&store.server_url.get(), &session_id, &message_id, text)
                .await
            {
                Ok(()) => {
                    log::info!("fixture engine delta session={session_id} text={text}");
                    store.set_fixture_note(note);
                }
                Err(err) => store.action_error.set(Some(err)),
            }
        });
    }

    fn clear_fixture_note(self) {
        let current = self.fixture_suffix_note.get();
        if current == "Foreign frame ignored" || current == "Old socket frame ignored" {
            return;
        }
        self.fixture_suffix_note.set(String::new());
    }

    fn set_fixture_note(self, note: &str) {
        let current = self.fixture_suffix_note.get();
        if current == "Foreign frame ignored" || current == "Old socket frame ignored" {
            return;
        }
        self.fixture_suffix_note.set(note.to_owned());
    }

    fn remember_assistant(self, session_id: &str) {
        let Some(message_id) = self.assistant_message_id(None) else {
            return;
        };
        let session_id = session_id.to_owned();
        self.assistant_ids.update(|map| {
            map.insert(session_id, message_id);
        });
    }

    fn assistant_message_id(self, session_id: Option<&str>) -> Option<String> {
        if let Some(session_id) = session_id {
            if self.selected_session.get().as_deref() != Some(session_id) {
                return self.assistant_ids.with(|map| map.get(session_id).cloned());
            }
        }
        self.messages.with(|list| {
            list.iter()
                .rev()
                .find(|message| message.role != "user" && !message.id.is_empty())
                .map(|message| message.id.clone())
        })
    }

    /// Drop the event socket and leave it down so the log can move while
    /// this client is not subscribed.
    pub fn disconnect_events(self) {
        self.cancel_read_retry();
        self.socket_held.set(true);
        self.live_subscribed.set(String::new());
        self.reconnect();
        self.socket_label.set("Events: disconnected".to_owned());
    }

    /// Open the socket again. The open path reads the business log from the
    /// cursor this client still holds.
    pub fn reconnect_events(self) {
        self.socket_held.set(false);
        self.live_subscribed.set(String::new());
        self.reconnect();
    }

    pub fn reload_transcript(self) {
        let Some(agent_id) = self.selected_agent.get() else {
            self.action_error
                .set(Some("Choose a bot to reload.".to_owned()));
            return;
        };
        self.action_error.set(None);
        let store = self;
        if let Some(session_id) = self.selected_session.get() {
            go(async move {
                store.load_transcript(session_id).await;
            });
        } else {
            let generation = self.open_generation.get();
            go(async move {
                store.resume_bot(agent_id, generation).await;
            });
        }
    }

    pub fn preview(self, agent_id: &str) -> String {
        self.previews.with(|map| {
            map.get(agent_id)
                .cloned()
                .unwrap_or_else(|| "No messages yet".to_owned())
        })
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

    fn still_opening(self, agent_id: &str, generation: u64) -> bool {
        self.selected_agent.get().as_deref() == Some(agent_id)
            && self.open_generation.get() == generation
    }

    /// Open the bot's server-side default conversation. The desktop does not
    /// choose a session, and it does not keep a session list for navigation.
    async fn resume_bot(self, agent_id: String, generation: u64) {
        let lock = self.resume_lock.with(|lock| Arc::clone(lock));
        let _guard = lock.lock().await;
        if !self.still_opening(&agent_id, generation) {
            return;
        }
        let url = self.server_url.get();
        let session = match client::open_conversation(&url, &agent_id).await {
            Ok(session) => session,
            Err(err) => {
                if self.still_opening(&agent_id, generation) {
                    self.action_error.set(Some(err));
                }
                return;
            }
        };
        if !self.still_opening(&agent_id, generation) {
            return;
        }
        let session_id = session.id.clone();
        if let Some(previous) = self.selected_session.get() {
            if previous != session_id {
                self.remember_assistant(&previous);
                self.previous_session.set(Some(previous));
            }
        }
        self.selected_session.set(Some(session_id.clone()));
        // Buffer before subscribe so a delta that arrives with the ack is not
        // applied onto a transcript this load is about to replace.
        self.begin_live_buffer();
        self.subscribe(&session_id);
        self.load_transcript(session_id).await;
    }

    async fn load_previews(self) {
        let agents = self.agents.get();
        for agent in agents {
            if self.selected_agent.get().as_deref() == Some(agent.id.as_str())
                && self.messages.with(|list| !list.is_empty())
            {
                self.remember_preview();
                continue;
            }
            let url = self.server_url.get();
            let summary = match client::get_conversation(&url, &agent.id).await {
                Ok(session) => match client::list_messages(&url, &session.id).await {
                    Ok(messages) => transcript_summary(&messages),
                    Err(_) => continue,
                },
                Err(err) if err.contains("not_found") || err.contains("404") => {
                    "No messages yet".to_owned()
                }
                Err(_) => continue,
            };
            if self.selected_agent.get().as_deref() == Some(agent.id.as_str())
                && self.messages.with(|list| !list.is_empty())
            {
                continue;
            }
            let id = agent.id.clone();
            self.previews.update(|map| {
                map.insert(id, summary);
            });
        }
    }

    fn remember_preview(self) {
        let Some(agent_id) = self.selected_agent.get() else {
            return;
        };
        let summary = self.messages.with(|list| transcript_summary(list));
        self.previews.update(|map| {
            map.insert(agent_id, summary);
        });
    }

    fn same_session(self, session_id: &str) -> bool {
        self.selected_session.get().as_deref() == Some(session_id)
    }

    fn still_live(self, generation: u64) -> bool {
        self.ws_generation.get() == generation && !self.socket_held.get()
    }

    async fn load_transcript(self, session_id: String) {
        let lock = self.catchup_lock.with(|lock| Arc::clone(lock));
        let _guard = lock.lock().await;
        let generation = self.ws_generation.get();
        if !self.same_session(&session_id) || !self.still_live(generation) {
            return;
        }
        // Subscribe first, then read. Events that commit after the read and
        // were published on the socket stay in the buffer until the snapshot
        // or suffix is installed, so that window is not dropped.
        self.begin_live_buffer();
        if self.live_subscribed.get() != session_id {
            self.subscribe(&session_id);
            for _ in 0..subscribe_ack_polls() {
                if self.live_subscribed.get() == session_id
                    || !self.same_session(&session_id)
                    || !self.still_live(generation)
                {
                    break;
                }
                timer(SUBSCRIBE_ACK_SLICE).await;
            }
        }
        if !self.same_session(&session_id) || !self.still_live(generation) {
            self.discard_live_buffer();
            return;
        }
        let started_subscribed = self.live_subscribed.get() == session_id;
        // A read that starts before the ack can miss a publish the hub drops.
        self.read_unsubscribed.set(!started_subscribed);
        let mut read_ok = self.catch_up(session_id.clone()).await;
        let ack_during = extra_read_after_unready(
            started_subscribed,
            self.same_session(&session_id)
                && self.still_live(generation)
                && self.live_subscribed.get() == session_id,
        );
        if ack_during {
            self.read_unsubscribed.set(false);
            read_ok = self.catch_up(session_id.clone()).await;
        }
        if self.same_session(&session_id) && self.still_live(generation) {
            self.finish_live_buffer();
        } else {
            self.discard_live_buffer();
            return;
        }
        if !self.same_session(&session_id) || !self.still_live(generation) {
            return;
        }
        if report_synced(started_subscribed || ack_during, read_ok) {
            self.read_attempt.set(0);
            self.mark_synced(generation);
        } else if !read_ok {
            // The HTTP read failed. Do not keep a success label, and do not
            // start the next read until the backoff has elapsed.
            self.mark_subscribe_failed(generation);
            self.schedule_read_retry(session_id, generation);
        } else {
            // The read succeeded before the socket was subscribed. The next
            // attempt waits for the ack inside `load_transcript`.
            self.mark_subscribe_failed(generation);
            self.schedule_subscribe_retry(session_id, generation);
        }
    }

    fn mark_synced(self, generation: u64) {
        if !self.still_live(generation) {
            return;
        }
        self.read_unsubscribed.set(false);
        self.retry_ticket.update(|ticket| *ticket += 1);
        let cursor = self.business_high_water.get();
        if self.subscribe_failed.get() {
            self.socket_label.set(LABEL_RECOVERED.to_owned());
            log::info!("subscribe catch-up recovered session after={cursor}");
        } else {
            self.socket_label.set(LABEL_CONNECTED.to_owned());
            log::info!("subscribe catch-up connected session after={cursor}");
        }
    }

    fn mark_subscribe_failed(self, generation: u64) {
        if !self.still_live(generation) {
            return;
        }
        self.subscribe_failed.set(true);
        self.socket_label.set(LABEL_FAILED.to_owned());
        let cursor = self.business_high_water.get();
        log::info!("subscribe catch-up failed session after={cursor}");
    }

    fn cancel_read_retry(self) {
        self.retry_ticket.update(|ticket| *ticket += 1);
        self.read_attempt.set(0);
    }

    fn schedule_subscribe_retry(self, session_id: String, generation: u64) {
        self.spawn_read_retry(session_id, generation, Duration::ZERO);
    }

    fn schedule_read_retry(self, session_id: String, generation: u64) {
        let attempt = self.read_attempt.get();
        self.read_attempt.update(|n| *n = n.saturating_add(1));
        let delay = read_retry_delay(attempt);
        log::info!(
            "business read retry in {}ms attempt={attempt}",
            delay.as_millis()
        );
        self.spawn_read_retry(session_id, generation, delay);
    }

    fn spawn_read_retry(self, session_id: String, generation: u64, delay: Duration) {
        self.retry_ticket.update(|ticket| *ticket += 1);
        let ticket = self.retry_ticket.get();
        let store = self;
        go(async move {
            if !delay.is_zero() {
                timer(delay).await;
            }
            if store.retry_ticket.get() != ticket || !store.still_live(generation) {
                return;
            }
            if !store.same_session(&session_id) {
                return;
            }
            store.load_transcript(session_id).await;
        });
    }

    async fn catch_up(self, session_id: String) -> bool {
        let url = self.server_url.get();
        let generation = self.ws_generation.get();
        let tasks = client::list_tasks(&url, &session_id).await;
        if !self.same_session(&session_id) || !self.still_live(generation) {
            return false;
        }
        // Ask with the cursor this client still holds. A snapshot high water is
        // not expired, so using it first never receives the replacement 400.
        let held = if self.messages.with(Vec::is_empty) {
            self.business_high_water.set(0);
            self.applied_event_ids.with(|seen| seen.clear());
            0
        } else {
            self.business_high_water.get()
        };
        log::info!("business replay session={session_id} after={held}");
        let mut after = held;
        let mut saw_expired = false;
        let mut read_ok = false;
        for _ in 0..8 {
            if !self.same_session(&session_id) || !self.still_live(generation) {
                break;
            }
            self.business_reads.update(|n| *n += 1);
            log::info!(
                "business read call n={} session={session_id} after={after}",
                self.business_reads.get()
            );
            match client::business_events(&url, &session_id, after).await {
                Ok(replay) => {
                    if !self.same_session(&session_id) || !self.still_live(generation) {
                        break;
                    }
                    read_ok = true;
                    let step = self.install_replay(replay, after, saw_expired);
                    saw_expired = step.saw_expired;
                    match step.next {
                        Some(next) => after = next,
                        None => break,
                    }
                }
                Err(err) => {
                    log::warn!("business replay: {err}");
                    // A snapshot can fill an empty transcript, but this read
                    // still failed. Connected and recovered wait for a
                    // business-events response.
                    read_ok = false;
                    if self.messages.with(Vec::is_empty) {
                        if let Ok(snap) = client::snapshot(&url, &session_id).await {
                            if self.same_session(&session_id)
                                && self.messages.with(Vec::is_empty)
                                && self.still_live(generation)
                            {
                                self.install_snapshot(
                                    snap.messages,
                                    snap.approvals,
                                    snap.high_water,
                                );
                            }
                        }
                    }
                    break;
                }
            }
        }
        if let Ok(tasks) = tasks {
            if self.same_session(&session_id) {
                self.tasks.set(tasks);
            }
        }
        self.remember_preview();
        read_ok
    }

    fn install_replay(self, replay: BusinessReplay, after: i64, saw_expired: bool) -> ReplayStep {
        match replay {
            BusinessReplay::Expired(snap) => {
                let high = snap.high_water;
                log::info!(
                    "business cursor {after} expired; replacing transcript with snapshot high_water={high} messages={}",
                    snap.messages.len()
                );
                self.install_snapshot(snap.messages, snap.approvals, high);
                self.rebuilt_from_snapshot.set(true);
                let next = (high > after).then_some(high);
                ReplayStep {
                    saw_expired: true,
                    next,
                }
            }
            BusinessReplay::Events(events) => {
                if events.is_empty() {
                    return ReplayStep {
                        saw_expired,
                        next: None,
                    };
                }
                if !saw_expired {
                    self.rebuilt_from_snapshot.set(false);
                }
                for event in events {
                    self.apply(event);
                }
                let next_cursor = self.business_high_water.get();
                let next = (next_cursor > after).then_some(next_cursor);
                ReplayStep { saw_expired, next }
            }
        }
    }

    fn reconnect(self) {
        self.ws_generation.update(|generation| *generation += 1);
        self.ws_out.replace(None);
        if self.socket_held.get() {
            self.socket_label.set("Events: disconnected".to_owned());
        } else {
            self.socket_label.set("Events: connecting".to_owned());
        }
    }

    fn begin_live_buffer(self) {
        let generation = self.ws_generation.get();
        self.live_gate.with(|gate| {
            let mut gate = gate.lock().expect("live gate");
            if gate.generation != generation {
                gate.pending.clear();
            }
            gate.generation = generation;
            gate.buffering = true;
        });
    }

    fn discard_live_buffer(self) {
        self.live_gate.with(|gate| {
            let mut gate = gate.lock().expect("live gate");
            gate.pending.clear();
            gate.buffering = false;
        });
    }

    fn buffering_live(self) -> bool {
        self.live_gate
            .with(|gate| gate.lock().expect("live gate").buffering)
    }

    /// Apply socket events that were not inside the snapshot just installed.
    /// The flag flips off in the same critical section as the last empty take,
    /// so an event cannot sit in the buffer after catch-up has finished.
    fn finish_live_buffer(self) {
        let generation = self.ws_generation.get();
        loop {
            let batch = self.live_gate.with(|gate| {
                let mut gate = gate.lock().expect("live gate");
                if gate.generation != generation {
                    gate.pending.clear();
                    gate.buffering = false;
                    return Vec::new();
                }
                let batch = std::mem::take(&mut gate.pending);
                if batch.is_empty() {
                    gate.buffering = false;
                }
                batch
            });
            if batch.is_empty() || self.ws_generation.get() != generation {
                if self.ws_generation.get() != generation {
                    self.discard_live_buffer();
                }
                break;
            }
            let open = self.selected_session.get();
            for event in batch {
                if let Some(session_id) = event_session(&event) {
                    if open.as_deref() != Some(session_id) {
                        self.note_foreign(session_id);
                        continue;
                    }
                }
                let high = self.business_high_water.get();
                if crate::model::covered_by_high_water(&event, high) {
                    continue;
                }
                self.apply(event);
            }
        }
    }

    fn ingest_live(self, event: crate::model::ServerEvent) {
        let generation = self.ws_generation.get();
        let mut event = Some(event);
        let queued = self.live_gate.with(|gate| {
            let mut gate = gate.lock().expect("live gate");
            if gate.buffering && gate.generation == generation {
                if let Some(event) = event.take() {
                    gate.pending.push(event);
                }
                true
            } else if gate.buffering {
                true
            } else {
                false
            }
        });
        if !queued {
            if let Some(event) = event {
                self.apply(event);
            }
        }
    }

    /// A socket callback. An older generation is ignored, so it cannot move
    /// the business cursor of the conversation that is open now.
    fn targets_open_chat(self, event: &crate::model::ServerEvent) -> bool {
        event_session(event)
            .is_some_and(|session_id| self.selected_session.get().as_deref() == Some(session_id))
    }

    fn accept_socket(self, generation: u64, event: crate::model::ServerEvent) {
        if self.ws_generation.get() != generation {
            return;
        }
        self.note_subscribed(&event);
        self.ingest_live(event);
    }

    fn note_subscribed(self, event: &crate::model::ServerEvent) {
        if let crate::model::ServerEvent::Ignored(kind) = event {
            if let Some(session_id) = kind.strip_prefix("subscribed ") {
                if session_id.is_empty()
                    || self.selected_session.get().as_deref() != Some(session_id)
                {
                    return;
                }
                let became_ready = self.live_subscribed.get() != session_id;
                self.live_subscribed.set(session_id.to_owned());
                if became_ready && self.read_unsubscribed.get() && !self.buffering_live() {
                    self.read_unsubscribed.set(false);
                    let store = self;
                    let session_id = session_id.to_owned();
                    go(async move {
                        store.load_transcript(session_id).await;
                    });
                }
            }
        }
    }

    fn note_foreign(self, session_id: &str) {
        log::info!("ignored foreign frame session={session_id}");
        if self.fixture_suffix.get() {
            self.fixture_suffix_note
                .set("Foreign frame ignored".to_owned());
        }
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
                while store.socket_held.get() {
                    store.socket_label.set("Events: disconnected".to_owned());
                    timer(Duration::from_millis(100)).await;
                }
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
                    if store.ws_generation.get() != generation || store.socket_held.get() {
                        if let Some(WsEvent::Server(_)) = &event {
                            log::info!("ignored old socket frame");
                            if store.fixture_suffix.get() {
                                store
                                    .fixture_suffix_note
                                    .set("Old socket frame ignored".to_owned());
                            }
                        }
                        break;
                    }
                    match event {
                        Some(WsEvent::Open) => {
                            // The socket is up. Catch-up has not happened yet.
                            store.subscribe_failed.set(false);
                            store.socket_label.set(LABEL_CATCHING_UP.to_owned());
                            if let Some(session_id) = store.selected_session.get() {
                                // The ack has to be for this socket, not the one that just died.
                                store.live_subscribed.set(String::new());
                                store.begin_live_buffer();
                                store.subscribe(&session_id);
                                let again = store;
                                let session_id = session_id.clone();
                                go(async move {
                                    again.load_transcript(session_id).await;
                                });
                            }
                        }
                        Some(WsEvent::Server(event)) => {
                            store.accept_socket(generation, event);
                        }
                        Some(WsEvent::Closed(reason)) => {
                            store.live_subscribed.set(String::new());
                            store
                                .socket_label
                                .set(format!("Events: disconnected: {reason}"));
                            break;
                        }
                        None => {
                            store.live_subscribed.set(String::new());
                            store.socket_label.set("Events: disconnected".to_owned());
                            break;
                        }
                    }
                }
                store.ws_out.replace(None);
                store.live_subscribed.set(String::new());
                if store.socket_held.get() {
                    store.socket_label.set("Events: disconnected".to_owned());
                    continue;
                }
                for _ in 0..20 {
                    if store.ws_generation.get() != generation || store.socket_held.get() {
                        break;
                    }
                    timer(Duration::from_millis(100)).await;
                }
            }
        });
    }

    fn apply(self, event: crate::model::ServerEvent) {
        use crate::model::ServerEvent;
        if self.already_applied(&event) {
            return;
        }
        // `apply_delta` and `set_status` ignore another conversation, but
        // remembering the cursor afterwards would still move this chat.
        if cursor_event(&event) && !self.targets_open_chat(&event) {
            if let Some(session_id) = event_session(&event) {
                self.note_foreign(session_id);
            }
            return;
        }
        match event {
            ServerEvent::Delta {
                event_id,
                cursor,
                message_id,
                session_id,
                text,
            } => {
                self.apply_delta(session_id, message_id, text);
                self.remember_event(&event_id, cursor);
            }
            ServerEvent::Completed {
                event_id,
                cursor,
                message_id,
                session_id,
            } => {
                self.set_status(
                    &session_id,
                    &message_id,
                    stored_status_for_event("message.completed").unwrap_or("complete"),
                    None,
                );
                self.remember_event(&event_id, cursor);
                self.reload_session(session_id);
            }
            ServerEvent::Failed {
                event_id,
                cursor,
                message_id,
                session_id,
                message,
            } => {
                self.set_status(&session_id, &message_id, "error", Some(message.clone()));
                self.action_error.set(Some(message));
                self.remember_event(&event_id, cursor);
                self.reload_session(session_id);
            }
            ServerEvent::Canceled {
                event_id,
                cursor,
                message_id,
                session_id,
            } => {
                self.set_status(&session_id, &message_id, "canceled", None);
                self.remember_event(&event_id, cursor);
                self.reload_session(session_id);
            }
            ServerEvent::Created {
                event_id,
                cursor,
                message,
            } => {
                if self.selected_session.get().as_deref() == Some(message.session_id.as_str())
                    || message.session_id.is_empty()
                {
                    self.upsert_message(message);
                }
                self.remember_event(&event_id, cursor);
            }
            ServerEvent::Updated {
                event_id,
                cursor,
                message_id,
                session_id,
                status,
            } => {
                if !status.is_empty() {
                    self.set_status(&session_id, &message_id, &status, None);
                }
                self.remember_event(&event_id, cursor);
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
                if message.status.is_empty() {
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
            sort_messages(list);
        });
        self.remember_preview();
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
        // Catch-up is already reading the log. A nested reload would apply the
        // same suffix again, or observe it before the caller has finished.
        if self.buffering_live() {
            return;
        }
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
            sort_messages(list);
        });
        self.remember_preview();
    }

    fn install_snapshot(self, messages: Vec<Message>, approvals: Vec<Approval>, high_water: i64) {
        let mut installed = Vec::new();
        replace_transcript(&mut installed, messages);
        self.messages.set(installed);
        self.approvals.set(approvals);
        self.business_high_water.set(high_water);
        self.applied_event_ids.with(|seen| seen.clear());
    }

    fn already_applied(self, event: &crate::model::ServerEvent) -> bool {
        use crate::model::ServerEvent;
        let (event_id, cursor) = match event {
            ServerEvent::Delta {
                event_id, cursor, ..
            }
            | ServerEvent::Completed {
                event_id, cursor, ..
            }
            | ServerEvent::Failed {
                event_id, cursor, ..
            }
            | ServerEvent::Canceled {
                event_id, cursor, ..
            }
            | ServerEvent::Created {
                event_id, cursor, ..
            }
            | ServerEvent::Updated {
                event_id, cursor, ..
            } => (event_id, *cursor),
            _ => return false,
        };
        if event_inside_snapshot(cursor, self.business_high_water.get()) {
            return true;
        }
        if event_id.is_empty() {
            return false;
        }
        self.applied_event_ids.with(|seen| seen.contains(event_id))
    }

    fn remember_event(self, event_id: &str, cursor: i64) {
        if !event_id.is_empty() {
            self.applied_event_ids.with(|seen| {
                seen.insert(event_id.to_owned());
            });
        }
        if cursor > self.business_high_water.get() {
            self.business_high_water.set(cursor);
        }
    }

    fn submit_key(self, session_id: &str, content: &str) -> String {
        let mut reuse = None;
        self.pending_submit.with(|pending| {
            if let Some((session, body, key)) = pending {
                if session == session_id && body == content {
                    reuse = Some(key.clone());
                }
            }
        });
        if let Some(key) = reuse {
            return key;
        }
        self.submit_serial.update(|n| *n += 1);
        let key = format!("desktop-{}", self.submit_serial.get());
        self.pending_submit.replace(Some((
            session_id.to_owned(),
            content.to_owned(),
            key.clone(),
        )));
        key
    }

    fn clear_submit(self, session_id: &str, content: &str) {
        self.pending_submit.with(|pending| {
            let clear = matches!(
                pending.as_ref(),
                Some((session, body, _)) if session == session_id && body == content
            );
            if clear {
                *pending = None;
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

#[cfg(test)]
mod tests {
    use std::collections::{HashMap, HashSet};
    use std::sync::Arc;
    use std::time::Duration;

    use nsh_core::prelude::{Owner, signal, stored};

    use crate::client::BusinessReplay;
    use tokio::sync::Mutex;

    use super::*;
    use crate::model::{Message, ServerEvent};

    fn with_store(check: impl FnOnce(Store)) {
        let runtime = nsh_core::element::reactive::Runtime::default();
        let _entered = runtime.enter();
        let store = Owner::detached().run(test_store);
        check(store);
    }

    fn test_store() -> Store {
        Store {
            server_url: signal(crate::model::DEFAULT_SERVER.to_owned()),
            server_draft: signal(crate::model::DEFAULT_SERVER.to_owned()),
            health: signal(None),
            server_error: signal(None),
            action_error: signal(None),
            agents: signal(Vec::new()),
            sessions: signal(Vec::new()),
            previews: signal(HashMap::new()),
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
            socket_label: signal(String::new()),
            rebuilt_from_snapshot: signal(false),
            fixture_suffix: signal(false),
            fixture_suffix_note: signal(String::new()),
            details_open: signal(false),
            library_tab: signal(false),
            ws_generation: signal(0),
            transcript_epoch: signal(0),
            open_generation: signal(0),
            resume_lock: stored(Arc::new(Mutex::new(()))),
            ws_out: stored(None),
            socket_held: signal(false),
            live_subscribed: signal(String::new()),
            read_unsubscribed: signal(false),
            subscribe_failed: signal(false),
            retry_ticket: signal(0),
            read_attempt: signal(0),
            business_reads: signal(0),
            previous_session: signal(None),
            assistant_ids: signal(HashMap::new()),
            live_gate: stored(Arc::new(std::sync::Mutex::new(LiveGate {
                buffering: false,
                generation: 0,
                pending: Vec::new(),
            }))),
            catchup_lock: stored(Arc::new(Mutex::new(()))),
            business_high_water: signal(0),
            applied_event_ids: stored(HashSet::new()),
            submit_serial: signal(0),
            pending_submit: stored(None),
        }
    }

    fn open_chat(store: Store) {
        store.selected_session.set(Some("chat-a".to_owned()));
        store.messages.set(vec![Message {
            id: "m".into(),
            session_id: "chat-a".into(),
            seq: 1,
            role: "assistant".into(),
            content: "cedar".into(),
            status: "complete".into(),
            ..Message::default()
        }]);
        store.business_high_water.set(3);
        store.ws_generation.set(1);
    }

    fn delta(session_id: &str, cursor: i64, text: &str) -> ServerEvent {
        ServerEvent::Delta {
            event_id: format!("e{cursor}-{session_id}"),
            cursor,
            message_id: "m".into(),
            session_id: session_id.to_owned(),
            text: text.to_owned(),
        }
    }

    #[test]
    fn foreign_session_does_not_move_the_business_cursor_when_not_buffering() {
        with_store(|store| {
            open_chat(store);
            assert!(!store.buffering_live());
            store.ingest_live(delta("chat-b", 9, "nope"));
            assert_eq!(store.business_cursor(), 3, "foreign cursor moved this chat");
            let content = store.messages.with(|list| list[0].content.clone());
            assert_eq!(content, "cedar");
        });
    }

    #[test]
    fn old_socket_callback_does_not_move_the_business_cursor() {
        with_store(|store| {
            open_chat(store);
            store.begin_live_buffer();
            store.ingest_live(delta("chat-a", 9, "stale"));
            store.ws_generation.set(2);
            store.finish_live_buffer();
            assert_eq!(store.business_cursor(), 3, "old socket moved this chat");
            let content = store.messages.with(|list| list[0].content.clone());
            assert_eq!(content, "cedar");
        });
    }

    #[test]
    fn old_socket_callback_while_idle_does_not_move_the_cursor() {
        with_store(|store| {
            open_chat(store);
            assert!(!store.buffering_live());
            store.ws_generation.set(2);
            store.accept_socket(1, delta("chat-a", 9, "stale"));
            assert_eq!(store.business_cursor(), 3);
            let content = store.messages.with(|list| list[0].content.clone());
            assert_eq!(content, "cedar");
        });
    }

    #[test]
    fn business_cursor_is_not_message_seq_or_a_public_cursor() {
        with_store(|store| {
            open_chat(store);
            let event = crate::model::parse_event(
                r#"{"type":"message.created","id":"e4","cursor":4,"publicSeq":80,"message":{"id":"m2","sessionId":"chat-a","seq":99,"role":"assistant","content":"lantern","status":"complete"}}"#,
            )
            .unwrap();
            store.ingest_live(event);
            assert_eq!(store.business_cursor(), 4);
            assert_ne!(store.business_cursor(), 99);
            assert_ne!(store.business_cursor(), 80);
            let seq = store
                .messages
                .with(|list| list.iter().find(|item| item.id == "m2").unwrap().seq);
            assert_eq!(seq, 99);
        });
    }

    #[test]
    fn expired_snapshot_replaces_once_then_suffix_is_not_duplicated() {
        with_store(|store| {
            open_chat(store);
            let snapshot = BusinessReplay::Expired(crate::client::MaterializedSnapshot {
                high_water: 4,
                messages: vec![Message {
                    id: "m".into(),
                    session_id: "chat-a".into(),
                    seq: 1,
                    role: "assistant".into(),
                    content: "lantern".into(),
                    status: "complete".into(),
                    ..Message::default()
                }],
                approvals: Vec::new(),
            });
            let step = store.install_replay(snapshot, 3, false);
            assert_eq!(step.next, Some(4));
            assert!(store.rebuilt_from_snapshot.get());
            assert_eq!(content(store), "lantern");
            assert_eq!(store.business_cursor(), 4);
            assert_ne!(store.business_cursor(), 1);
            let suffix = delta("chat-a", 5, "once");
            let step = store.install_replay(BusinessReplay::Events(vec![suffix.clone()]), 4, true);
            assert_eq!(step.next, Some(5));
            assert_eq!(content(store), "lanternonce");
            assert_eq!(store.business_cursor(), 5);
            assert!(store.rebuilt_from_snapshot.get());
            let step = store.install_replay(BusinessReplay::Events(vec![suffix]), 5, true);
            assert_eq!(step.next, None);
            assert_eq!(content(store), "lanternonce");
            assert_eq!(store.business_cursor(), 5);
        });
    }

    #[test]
    fn business_event_during_replay_is_kept_once() {
        with_store(|store| {
            open_chat(store);
            store.begin_live_buffer();
            let inside = delta("chat-a", 4, "lantern");
            let suffix = delta("chat-a", 5, "once");
            store.ingest_live(inside.clone());
            store.ingest_live(suffix);
            store.ingest_live(delta("chat-b", 9, "nope"));
            store.ingest_live(ServerEvent::Ignored("subscribed chat-a".into()));
            store.apply(inside);
            store.finish_live_buffer();
            assert_eq!(content(store), "cedarlanternonce");
            assert_eq!(store.business_cursor(), 5);
            assert!(!content(store).contains("nope"));
            assert!(!content(store).contains("lanternlantern"));
            assert!(!content(store).contains("onceonce"));
        });
    }

    #[test]
    fn subscribe_ack_budget_is_five_seconds_and_an_unsubscribed_read_is_not_synced() {
        assert_eq!(SUBSCRIBE_ACK_BUDGET, Duration::from_secs(5));
        assert_eq!(subscribe_ack_polls(), 50);
        let budget = SUBSCRIBE_ACK_BUDGET;
        let late_ack = Some(Duration::from_millis(6000));
        let during_gap = Duration::from_millis(5500);
        // The first read, taken when the ack is late or missing, does not
        // contain the event. This predicate does not sleep.
        assert!(event_missed_without_another_read(
            late_ack, during_gap, budget
        ));
        assert!(event_missed_without_another_read(None, during_gap, budget));
        // The retry read is not gated on the ack.
        assert!(retry_read_includes(during_gap, Duration::from_secs(10)));
        assert!(retry_read_includes(during_gap, late_ack.unwrap()));
        assert!(extra_read_after_unready(false, true));
        assert!(!extra_read_after_unready(false, false));
        assert!(!extra_read_after_unready(true, true));
        // A successful unsubscribed read is not connected or recovered.
        assert!(!report_synced(false, true));
        assert!(!report_synced(true, false));
        assert!(report_synced(true, true));
        let timely = Some(Duration::from_millis(100));
        assert!(!event_missed_without_another_read(
            timely,
            Duration::from_millis(50),
            budget,
        ));
        assert!(!event_missed_without_another_read(
            timely,
            Duration::from_millis(200),
            budget,
        ));
    }

    #[test]
    fn failed_read_backoff_is_capped_exponential() {
        assert_eq!(read_retry_delay(0), Duration::from_secs(1));
        assert_eq!(read_retry_delay(1), Duration::from_secs(2));
        assert_eq!(read_retry_delay(2), Duration::from_secs(4));
        assert_eq!(read_retry_delay(3), Duration::from_secs(8));
        assert_eq!(read_retry_delay(4), Duration::from_secs(16));
        assert_eq!(read_retry_delay(5), READ_RETRY_CAP);
        assert_eq!(read_retry_delay(20), READ_RETRY_CAP);
        // One second is the floor, so two seconds cannot hold the old 25/s loop.
        assert!(read_retry_delay(0) * 25 > Duration::from_secs(2));
    }

    /// Callbacks already sitting in the in-process queue. This is not a frame
    /// read from a TCP connection after that connection has closed, and it is
    /// not a Makez cursor. `select_agent` does not bump the connection
    /// generation; the session id is what keeps the previous chat's queued
    /// delta off the chat that is open now. A later generation drops the
    /// queue even when the session id still matches. A missing cursor stays
    /// missing.
    #[test]
    fn queued_callback_after_switch_or_obsolete_generation_does_not_apply() {
        with_store(|store| {
            open_chat(store);
            let cursorless = ServerEvent::Delta {
                event_id: String::new(),
                cursor: 0,
                message_id: "m".into(),
                session_id: "chat-a".into(),
                text: "queued".into(),
            };
            assert_eq!(crate::model::event_business_cursor(&cursorless), None);

            store.begin_live_buffer();
            store.ingest_live(cursorless.clone());
            store.ingest_live(delta("chat-a", 9, "moved"));
            // The open chat changes. The connection generation stays 1,
            // which is what selecting another bot does.
            store.selected_session.set(Some("chat-b".into()));
            store.messages.set(vec![Message {
                id: "b".into(),
                session_id: "chat-b".into(),
                seq: 2,
                role: "assistant".into(),
                content: "lark".into(),
                status: "complete".into(),
                ..Message::default()
            }]);
            store.business_high_water.set(4);
            store.applied_event_ids.with(|seen| seen.clear());
            assert_eq!(store.ws_generation.get(), 1);
            store.finish_live_buffer();
            assert_eq!(store.business_cursor(), 4);
            assert_eq!(store.message_seq(), 2);
            assert_eq!(texts(store), vec!["lark".to_owned()]);

            store.selected_session.set(Some("chat-a".into()));
            store.messages.set(vec![Message {
                id: "m".into(),
                session_id: "chat-a".into(),
                seq: 1,
                role: "assistant".into(),
                content: "cedar".into(),
                status: "complete".into(),
                ..Message::default()
            }]);
            store.business_high_water.set(3);
            store.applied_event_ids.with(|seen| seen.clear());
            store.begin_live_buffer();
            store.ingest_live(cursorless.clone());
            store.ingest_live(delta("chat-a", 9, "moved"));
            store.ws_generation.set(2);
            store.finish_live_buffer();
            assert_eq!(store.business_cursor(), 3);
            assert_eq!(store.message_seq(), 1);
            assert_eq!(texts(store), vec!["cedar".to_owned()]);
            store.accept_socket(1, cursorless);
            store.accept_socket(1, delta("chat-a", 9, "moved"));
            assert_eq!(store.business_cursor(), 3);
            assert_eq!(store.message_seq(), 1);
            assert_eq!(texts(store), vec!["cedar".to_owned()]);
        });
    }

    #[test]
    fn cursorless_delta_does_not_move_the_business_cursor_or_another_chat() {
        with_store(|store| {
            open_chat(store);
            store.ingest_live(ServerEvent::Delta {
                event_id: String::new(),
                cursor: 0,
                message_id: "m".into(),
                session_id: "chat-a".into(),
                text: "z".into(),
            });
            assert_eq!(store.business_cursor(), 3);
            assert_eq!(store.message_seq(), 1);
            assert_eq!(content(store), "cedarz");
            store.ingest_live(ServerEvent::Delta {
                event_id: String::new(),
                cursor: 0,
                message_id: "m".into(),
                session_id: "chat-b".into(),
                text: "nope".into(),
            });
            assert_eq!(store.business_cursor(), 3);
            assert_eq!(store.message_seq(), 1);
            assert_eq!(content(store), "cedarz");
            assert!(!content(store).contains("nope"));
        });
    }

    fn content(store: Store) -> String {
        store.messages.with(|list| list[0].content.clone())
    }

    fn texts(store: Store) -> Vec<String> {
        store
            .messages
            .with(|list| list.iter().map(|message| message.content.clone()).collect())
    }
}

fn cursor_event(event: &crate::model::ServerEvent) -> bool {
    use crate::model::ServerEvent;
    matches!(
        event,
        ServerEvent::Delta { .. }
            | ServerEvent::Completed { .. }
            | ServerEvent::Failed { .. }
            | ServerEvent::Canceled { .. }
            | ServerEvent::Created { .. }
            | ServerEvent::Updated { .. }
    )
}

fn event_session(event: &crate::model::ServerEvent) -> Option<&str> {
    use crate::model::ServerEvent;
    let session_id = match event {
        ServerEvent::Delta { session_id, .. }
        | ServerEvent::Completed { session_id, .. }
        | ServerEvent::Failed { session_id, .. }
        | ServerEvent::Canceled { session_id, .. }
        | ServerEvent::Updated { session_id, .. } => session_id.as_str(),
        ServerEvent::Created { message, .. } => message.session_id.as_str(),
        ServerEvent::Approval(approval) => approval.session_id.as_str(),
        ServerEvent::Task(task) => task.session_id.as_str(),
        ServerEvent::Session(session) => session.id.as_str(),
        ServerEvent::Ignored(_) => return None,
    };
    if session_id.is_empty() {
        None
    } else {
        Some(session_id)
    }
}
