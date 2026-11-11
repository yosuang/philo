//! Window state for the three-column shell.
//!
//! Everything the window shows lives here. Nothing in this module opens a
//! socket, reads a database, or asks a model to run. The HTTP client in
//! `client` stays in the crate for a later session and is not started.

use nsh_core::platform::Appearance;
use nsh_core::prelude::*;
use nsh_core::storage::Storage;

const APPEARANCE_KEY: &str = "appearance";

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum AppearanceChoice {
    System,
    Light,
    Dark,
}

impl AppearanceChoice {
    fn scheme(self) -> Appearance {
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

#[derive(Clone, PartialEq, Eq)]
pub struct Bot {
    pub id: String,
    pub name: String,
    pub summary: String,
    pub group: String,
    /// Which blob drawing this bot uses. Not a letter.
    pub shape: u8,
    /// Seed rows. A bot the window creates locally is not seed data.
    pub demo: bool,
}

impl Bot {
    fn matches(&self, query: &str) -> bool {
        self.name.to_lowercase().contains(query) || self.summary.to_lowercase().contains(query)
    }
}

#[derive(Clone, PartialEq, Eq)]
pub struct Line {
    pub id: String,
    pub bot_id: String,
    pub mine: bool,
    /// Shown in the bubble. Demo notes say they are not a model reply.
    pub body: String,
    pub previews: Vec<String>,
}

#[derive(Clone, PartialEq, Eq)]
pub struct Activity {
    pub title: String,
    pub detail: String,
}

#[derive(Clone, PartialEq, Eq)]
pub struct Preview {
    pub id: String,
    pub label: String,
}

#[derive(Clone, Copy)]
pub struct Shell {
    pub bots: Signal<Vec<Bot>>,
    pub lines: Signal<Vec<Line>>,
    pub selected: Signal<Option<String>>,
    pub draft: Signal<String>,
    pub previews: Signal<Vec<Preview>>,
    pub query: Signal<String>,
    pub search_open: Signal<bool>,
    pub collapsed_groups: Signal<Vec<String>>,
    pub sidebar_collapsed: Signal<bool>,
    pub details_open: Signal<bool>,
    pub library: Signal<bool>,
    pub activity_more: Signal<bool>,
    pub suggested_hidden: Signal<bool>,
    pub creating: Signal<bool>,
    pub new_name: Signal<String>,
    pub notice: Signal<Option<String>>,
    pub away_from_bottom: Signal<bool>,
    pub appearance: Signal<AppearanceChoice>,
    ids: Stored<u32>,
    pub transcript: ElementRef,
    pub search_field: ElementRef,
    pub name_field: ElementRef,
}

impl Shell {
    pub fn boot() -> Self {
        let shell = Self {
            bots: signal(seed_bots()),
            lines: signal(seed_lines()),
            selected: signal(Some("philo".to_owned())),
            draft: signal(String::new()),
            previews: signal(Vec::new()),
            query: signal(String::new()),
            search_open: signal(false),
            collapsed_groups: signal(vec!["unassigned".to_owned()]),
            sidebar_collapsed: signal(false),
            details_open: signal(false),
            library: signal(false),
            activity_more: signal(false),
            suggested_hidden: signal(false),
            creating: signal(false),
            new_name: signal(String::new()),
            notice: signal(None),
            away_from_bottom: signal(false),
            appearance: signal(AppearanceChoice::Light),
            ids: stored(100),
            transcript: element_ref(),
            search_field: element_ref(),
            name_field: element_ref(),
        };
        let choice = Storage::get_item(APPEARANCE_KEY)
            .map(|value| AppearanceChoice::parse(&value))
            .unwrap_or(AppearanceChoice::Light);
        shell.set_appearance(choice);
        shell
    }

    pub fn set_appearance(self, choice: AppearanceChoice) {
        self.appearance.set(choice);
        set_color_scheme(choice.scheme());
        let _ = Storage::set_item(APPEARANCE_KEY, choice.as_str());
    }

    pub fn selected_bot(self) -> Option<Bot> {
        let id = self.selected.get()?;
        self.bots
            .with(|bots| bots.iter().find(|bot| bot.id == id).cloned())
    }

    pub fn bots_in(self, group: &str) -> Vec<Bot> {
        let query = self.query.get();
        let query = query.trim().to_lowercase();
        let searching = !query.is_empty();
        self.bots.with(|bots| {
            bots.iter()
                .filter(|bot| bot.group == group && (!searching || bot.matches(&query)))
                .cloned()
                .collect()
        })
    }

    pub fn group_open(self, group: &str) -> bool {
        let query = self.query.get();
        let searching = !query.trim().is_empty();
        if searching {
            return true;
        }
        !self
            .collapsed_groups
            .with(|groups| groups.iter().any(|item| item == group))
    }

    pub fn any_match(self) -> bool {
        !self.bots_in("demo").is_empty() || !self.bots_in("unassigned").is_empty()
    }

    pub fn toggle_group(self, group: &str) {
        let group = group.to_owned();
        self.collapsed_groups.update(|groups| {
            if let Some(at) = groups.iter().position(|item| item == &group) {
                groups.remove(at);
            } else {
                groups.push(group);
            }
        });
    }

    pub fn toggle_sidebar(self) {
        self.sidebar_collapsed.update(|open| *open = !*open);
    }

    pub fn open_search(self) {
        self.sidebar_collapsed.set(false);
        self.search_open.update(|open| *open = !*open);
        if self.search_open.get() {
            let field = self.search_field;
            // The field is mounted by this update. Focus it on the next turn.
            set_timeout(std::time::Duration::from_millis(16), move || field.focus());
        } else {
            self.query.set(String::new());
        }
    }

    pub fn select(self, id: String) {
        self.selected.set(Some(id));
        self.creating.set(false);
        self.draft.set(String::new());
        self.previews.set(Vec::new());
        self.away_from_bottom.set(false);
        self.transcript.scroll_to_end(false);
    }

    pub fn open_new(self) {
        self.sidebar_collapsed.set(false);
        self.creating.set(true);
        self.new_name.set(String::new());
        let field = self.name_field;
        set_timeout(std::time::Duration::from_millis(16), move || field.focus());
    }

    pub fn cancel_new(self) {
        self.creating.set(false);
        self.new_name.set(String::new());
    }

    pub fn create_bot(self) {
        let name = self.new_name.get().trim().to_owned();
        if name.is_empty() {
            self.notice("Give the bot a name.");
            return;
        }
        let id = self.alloc("bot");
        let shape = (self.bots.with(Vec::len) % 8) as u8;
        self.bots.update(|bots| {
            bots.push(Bot {
                id: id.clone(),
                name,
                summary: "Local · no messages yet".to_owned(),
                group: "unassigned".to_owned(),
                shape,
                demo: false,
            });
        });
        self.collapsed_groups
            .update(|groups| groups.retain(|item| item != "unassigned"));
        self.creating.set(false);
        self.new_name.set(String::new());
        self.select(id);
        self.notice
            .set(Some("Local bot. Nothing is stored on a server.".to_owned()));
    }

    pub fn line_count(self) -> usize {
        let id = self.selected.get();
        self.lines.with(|lines| {
            lines
                .iter()
                .filter(|line| Some(&line.bot_id) == id.as_ref())
                .count()
        })
    }

    pub fn line_at(self, index: usize) -> Option<Line> {
        let id = self.selected.get();
        self.lines.with(|lines| {
            lines
                .iter()
                .filter(|line| Some(&line.bot_id) == id.as_ref())
                .nth(index)
                .cloned()
        })
    }

    pub fn line_ids(self) -> Vec<String> {
        let id = self.selected.get();
        self.lines.with(|lines| {
            lines
                .iter()
                .filter(|line| Some(&line.bot_id) == id.as_ref())
                .map(|line| line.id.clone())
                .collect()
        })
    }

    pub fn send(self) {
        let Some(bot_id) = self.selected.get() else {
            self.notice("Choose a bot first.");
            return;
        };
        let text = self.draft.get().trim().to_owned();
        let previews: Vec<String> = self
            .previews
            .with(|items| items.iter().map(|item| item.label.clone()).collect());
        if text.is_empty() && previews.is_empty() {
            return;
        }
        let id = self.alloc("line");
        self.lines.update(|lines| {
            lines.insert(
                0,
                Line {
                    id,
                    bot_id: bot_id.clone(),
                    mine: true,
                    body: text,
                    previews,
                },
            );
        });
        self.bots.update(|bots| {
            if let Some(bot) = bots.iter_mut().find(|bot| bot.id == bot_id) {
                bot.summary = "Local · not sent".to_owned();
            }
        });
        self.draft.set(String::new());
        self.previews.set(Vec::new());
        self.away_from_bottom.set(false);
        self.transcript.scroll_to_end(false);
    }

    pub fn add_preview(self) {
        let n = self.previews.with(Vec::len) + 1;
        let id = self.alloc("preview");
        self.previews.update(|items| {
            items.push(Preview {
                id,
                label: format!("Local preview {n}"),
            });
        });
        self.transcript.scroll_to_end(false);
    }

    pub fn remove_preview(self, id: String) {
        self.previews
            .update(|items| items.retain(|item| item.id != id));
    }

    pub fn toggle_details(self) {
        self.details_open.update(|open| *open = !*open);
    }

    pub fn close_details(self) {
        self.details_open.set(false);
    }

    pub fn show_library(self, library: bool) {
        self.library.set(library);
        self.details_open.set(true);
    }

    pub fn notice(self, text: impl Into<String>) {
        self.notice.set(Some(text.into()));
    }

    pub fn dismiss_notice(self) {
        self.notice.set(None);
    }

    /// Escape leaves the top local layer and does not close the window.
    pub fn dismiss_top(self) {
        if !self.query.get().trim().is_empty() || self.search_open.get() {
            self.search_open.set(false);
            self.query.set(String::new());
        } else if self.creating.get() {
            self.cancel_new();
        } else if self.notice.get().is_some() {
            self.dismiss_notice();
        } else if self.details_open.get() {
            self.close_details();
        }
    }

    pub fn jump_bottom(self) {
        self.away_from_bottom.set(false);
        self.transcript.scroll_to_end(true);
    }

    pub fn note_scroll(self, offset_y: f32, content_h: f32, view_h: f32) {
        let max = (content_h - view_h).max(0.0);
        self.away_from_bottom.set(max - offset_y > 56.0);
    }

    pub fn activity(self) -> Vec<Activity> {
        let Some(bot) = self.selected_bot() else {
            return Vec::new();
        };
        if !bot.demo || bot.id == "desk" || bot.id == "shelf" {
            return Vec::new();
        }
        let mut rows = vec![
            Activity {
                title: "Demo sample".to_owned(),
                detail: "Sample · not a run".to_owned(),
            },
            Activity {
                title: "Demo label".to_owned(),
                detail: "Sample · not a run".to_owned(),
            },
            Activity {
                title: "Demo reminder".to_owned(),
                detail: "Sample · not a run".to_owned(),
            },
        ];
        if self.activity_more.get() {
            rows.push(Activity {
                title: "Demo archive".to_owned(),
                detail: "Sample · not a run".to_owned(),
            });
            rows.push(Activity {
                title: "Demo margin".to_owned(),
                detail: "Sample · not a run".to_owned(),
            });
        }
        rows
    }

    pub fn files(self) -> Vec<String> {
        let Some(bot) = self.selected_bot() else {
            return Vec::new();
        };
        if bot.id == "philo" {
            vec!["Demo note".to_owned(), "Demo sketch".to_owned()]
        } else if bot.demo && bot.id != "desk" && bot.id != "shelf" {
            vec!["Demo file".to_owned()]
        } else {
            Vec::new()
        }
    }

    fn alloc(self, prefix: &str) -> String {
        let n = self.ids.with(|id| {
            *id += 1;
            *id
        });
        format!("{prefix}-{n}")
    }
}

fn seed_bots() -> Vec<Bot> {
    vec![
        bot(
            "philo",
            "Philo",
            "Demo · a local bot in this window",
            "demo",
            0,
        ),
        bot(
            "notes",
            "Notes",
            "Demo · sample lines stay in this window",
            "demo",
            1,
        ),
        bot(
            "field",
            "Field",
            "Demo · nothing here is a model run",
            "demo",
            4,
        ),
        bot("desk", "Desk", "Demo · an empty chat", "demo", 5),
        bot("lamp", "Lamp", "Demo · a short sample", "demo", 6),
        bot("harbor", "Harbor", "Demo · another sample row", "demo", 2),
        bot(
            "kit",
            "Kit",
            "Demo · extra row so the list can scroll",
            "demo",
            7,
        ),
        bot(
            "grove",
            "Grove",
            "Demo · another row for the sidebar scroll",
            "demo",
            1,
        ),
        bot("nook", "Nook", "Demo · one more sidebar row", "demo", 5),
        bot(
            "shelf",
            "Shelf",
            "Demo · unassigned and empty",
            "unassigned",
            3,
        ),
        bot(
            "relay",
            "Relay",
            "Demo · unassigned sample",
            "unassigned",
            4,
        ),
    ]
}

fn bot(id: &str, name: &str, summary: &str, group: &str, shape: u8) -> Bot {
    Bot {
        id: id.to_owned(),
        name: name.to_owned(),
        summary: summary.to_owned(),
        group: group.to_owned(),
        shape,
        demo: true,
    }
}

fn seed_lines() -> Vec<Line> {
    // Newest first. Philo's transcript is long enough to scroll. Relay has
    // one local demo line. Desk and Shelf stay empty.
    let notes = [
        "Demo note. Sample text, not a model reply.",
        "Demo note. No model was asked.",
        "Demo note. This row is sample copy.",
        "Demo note. The window wrote this.",
        "Demo note. Not a run, and not a reply.",
        "Demo note. Older sample, still not a model.",
        "Demo note. One more so the thread fills the window.",
        "Demo note. The top line can sit under the name.",
    ];
    let mine = [
        "Demo line. Kept in this window.",
        "Another demo line. Still local.",
        "Demo line for the scroll.",
        "Demo line, still not sent.",
        "One more demo line.",
        "Demo line near the older end.",
        "Demo line. Still only in this window.",
        "Demo line under the name pill.",
    ];
    let mut lines = Vec::new();
    for index in 0..notes.len() {
        lines.push(philo_line(
            format!("seed-mine-{index}"),
            true,
            mine[index].to_owned(),
        ));
        lines.push(philo_line(
            format!("seed-note-{index}"),
            false,
            notes[index].to_owned(),
        ));
    }
    lines.push(Line {
        id: "relay-1".to_owned(),
        bot_id: "relay".to_owned(),
        mine: true,
        body: "Demo line on Relay. Local only.".to_owned(),
        previews: Vec::new(),
    });
    lines
}

fn philo_line(id: String, mine: bool, body: String) -> Line {
    Line {
        id,
        bot_id: "philo".to_owned(),
        mine,
        body,
        previews: Vec::new(),
    }
}
