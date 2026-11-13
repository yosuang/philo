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
    /// Name only. Every summary starts with "Demo", so a letter from that
    /// word would leave every row on screen.
    fn matches(&self, query: &str) -> bool {
        self.name.to_lowercase().contains(query)
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
    pub id: String,
    pub title: String,
    /// A local status word. These rows are samples.
    pub status: String,
    /// Shown only while this row is expanded.
    pub note: String,
}

#[derive(Clone, PartialEq, Eq)]
pub struct Preview {
    pub id: String,
    pub label: String,
}

/// Text and local previews left on a bot that is not the one on screen.
/// Nothing here is sent.
#[derive(Clone, PartialEq, Eq)]
struct Unsent {
    bot_id: String,
    text: String,
    previews: Vec<Preview>,
}

/// Activity, Library, and Suggested for one bot. The column's open flag is
/// the window's. Closing the column does not clear this.
/// `project` is the open blurb under that bot's Project pill.
#[derive(Clone, PartialEq, Eq)]
struct Panel {
    bot_id: String,
    library: bool,
    more: bool,
    suggested_hidden: bool,
    expanded: Vec<String>,
    project: bool,
}

impl Panel {
    fn empty(bot_id: String) -> Self {
        Self {
            bot_id,
            library: false,
            more: false,
            suggested_hidden: false,
            expanded: Vec::new(),
            project: false,
        }
    }
}

#[derive(Clone, Copy)]
pub struct Shell {
    pub bots: Signal<Vec<Bot>>,
    pub lines: Signal<Vec<Line>>,
    pub selected: Signal<Option<String>>,
    pub draft: Signal<String>,
    pub previews: Signal<Vec<Preview>>,
    /// Unsent lines for bots other than the one open now.
    unsent: Signal<Vec<Unsent>>,
    pub query: Signal<String>,
    pub search_open: Signal<bool>,
    /// The rail asked for the search field. The field takes this once it
    /// is mounted. A timer started on the rail is dropped with the rail.
    search_focus: Stored<bool>,
    pub collapsed_groups: Signal<Vec<String>>,
    /// Groups folded while a query is set. Clearing the query drops this
    /// and the saved collapse returns. Unassigned starts collapsed.
    search_collapsed: Signal<Vec<String>>,
    pub sidebar_collapsed: Signal<bool>,
    /// The details column. One for the window. What it shows is `panels`.
    pub details_open: Signal<bool>,
    /// Per bot: Activity or Library, expanded rows, Show more, Suggested.
    panels: Signal<Vec<Panel>>,
    pub creating: Signal<bool>,
    pub new_name: Signal<String>,
    pub notice: Signal<Option<String>>,
    pub away_from_bottom: Signal<bool>,
    pub appearance: Signal<AppearanceChoice>,
    ids: Stored<u32>,
    pub transcript: ElementRef,
    pub search_field: ElementRef,
    pub name_field: ElementRef,
    pub composer: ElementRef,
}

impl Shell {
    pub fn boot() -> Self {
        let shell = Self {
            bots: signal(seed_bots()),
            lines: signal(seed_lines()),
            selected: signal(Some("philo".to_owned())),
            draft: signal(String::new()),
            previews: signal(Vec::new()),
            unsent: signal(Vec::new()),
            query: signal(String::new()),
            search_open: signal(false),
            search_focus: stored(false),
            collapsed_groups: signal(vec!["unassigned".to_owned()]),
            search_collapsed: signal(Vec::new()),
            sidebar_collapsed: signal(false),
            details_open: signal(false),
            panels: signal(Vec::new()),
            creating: signal(false),
            new_name: signal(String::new()),
            notice: signal(None),
            away_from_bottom: signal(false),
            appearance: signal(AppearanceChoice::Light),
            ids: stored(100),
            transcript: element_ref(),
            search_field: element_ref(),
            name_field: element_ref(),
            composer: element_ref(),
        };
        let choice = Storage::get_item(APPEARANCE_KEY)
            .map(|value| AppearanceChoice::parse(&value))
            .unwrap_or(AppearanceChoice::Light);
        shell.set_appearance(choice);
        shell.focus_composer();
        shell
    }

    /// Puts the keyboard in the message field once that field can take it.
    /// On open the field has no node yet, and a focus sent before the
    /// native widget is ready does not stick, so this keeps trying.
    /// The new-bot form keeps the keyboard if it opened in the meantime.
    /// An open search field keeps it too.
    pub fn focus_composer(self) {
        let field = self.composer;
        if field.node().is_some() {
            set_timeout(std::time::Duration::from_millis(16), move || {
                if !self.creating.get() {
                    field.focus();
                }
            });
            return;
        }
        // 16ms steps for about three seconds. The waits are scheduled
        // here: the field is not mounted on the first one.
        for step in 1u64..=180 {
            let field = self.composer;
            set_timeout(std::time::Duration::from_millis(16 * step), move || {
                if self.creating.get() || self.search_open.get() || field.node().is_none() {
                    return;
                }
                field.focus();
            });
        }
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

    pub fn set_query(self, text: impl Into<String>) {
        let text = text.into();
        let was_searching = self.searching();
        self.query.set(text);
        // Entering or leaving a query drops folds made for the previous one.
        // A collapsed group opens when the query starts, so its matches show.
        if self.searching() != was_searching {
            self.search_collapsed.set(Vec::new());
        }
    }

    pub fn bots_in(self, group: &str) -> Vec<Bot> {
        let query = self.query_text();
        let searching = self.searching();
        self.bots.with(|bots| {
            bots.iter()
                .filter(|bot| bot.group == group && (!searching || bot.matches(&query)))
                .cloned()
                .collect()
        })
    }

    /// Bots the icon rail shows. The same name filter as the expanded list.
    pub fn visible_bots(self) -> Vec<Bot> {
        let query = self.query_text();
        let searching = self.searching();
        self.bots.with(|bots| {
            bots.iter()
                .filter(|bot| !searching || bot.matches(&query))
                .cloned()
                .collect()
        })
    }

    pub fn group_open(self, group: &str) -> bool {
        !self
            .folded_groups()
            .with(|groups| groups.iter().any(|item| item == group))
    }

    pub fn any_match(self) -> bool {
        !self.bots_in("demo").is_empty() || !self.bots_in("unassigned").is_empty()
    }

    pub fn toggle_group(self, group: &str) {
        let group = group.to_owned();
        self.folded_groups().update(|groups| {
            if let Some(at) = groups.iter().position(|item| item == &group) {
                groups.remove(at);
            } else {
                groups.push(group);
            }
        });
    }

    fn searching(self) -> bool {
        !self.query.get().trim().is_empty()
    }

    fn query_text(self) -> String {
        self.query.get().trim().to_lowercase()
    }

    /// While a query is set, folding a group is only for that query.
    fn folded_groups(self) -> Signal<Vec<String>> {
        if self.searching() {
            self.search_collapsed
        } else {
            self.collapsed_groups
        }
    }

    pub fn toggle_sidebar(self) {
        self.sidebar_collapsed.update(|open| *open = !*open);
    }

    /// The rail has no field. Expand and keep the query. The field focuses
    /// itself when it mounts. This does not clear the query.
    pub fn reveal_search(self) {
        self.search_focus.with(|flag| *flag = true);
        self.sidebar_collapsed.set(false);
        self.search_open.set(true);
    }

    /// Called from the mounted search field. One request, then it is spent.
    pub fn take_search_focus(self) -> bool {
        if self.creating.get() {
            return false;
        }
        self.search_focus.with(|flag| {
            if !*flag {
                return false;
            }
            *flag = false;
            true
        })
    }

    pub fn open_search(self) {
        // The field is not on the rail. Opening search from there shows it
        // and keeps the query. The header toggle is only while expanded.
        if self.sidebar_collapsed.get() {
            self.reveal_search();
            return;
        }
        self.search_open.update(|open| *open = !*open);
        if self.search_open.get() {
            let field = self.search_field;
            // The field is mounted by this update. Focus it on the next turn.
            set_timeout(std::time::Duration::from_millis(16), move || field.focus());
        } else {
            self.set_query(String::new());
            self.focus_active_field();
        }
    }

    /// The portrait is a demo. It does not open an account.
    pub fn show_demo_profile(self) {
        self.notice("This is a demo profile. Accounts are not available.");
    }

    /// Search closed. The new-bot form keeps the keyboard when it is open.
    fn focus_active_field(self) {
        if self.creating.get() {
            let field = self.name_field;
            set_timeout(std::time::Duration::from_millis(16), move || field.focus());
        } else {
            self.focus_composer();
        }
    }

    pub fn select(self, id: String) {
        let same = self.selected.get().as_deref() == Some(id.as_str());
        if !same {
            // The line in the composer belongs to the bot that was open.
            // The next bot gets its own unsent line back, still local.
            self.remember_unsent();
            self.apply_unsent(&id);
            self.selected.set(Some(id.clone()));
            self.away_from_bottom.set(false);
            self.show_newest(false);
            // The transcript's row count changes in this same turn. A scroll
            // then misses the new rows, so the newest line is asked again
            // once the list has them.
            let shell = self;
            let expected = id;
            set_timeout(std::time::Duration::from_millis(48), move || {
                if shell.selected.get().as_deref() != Some(expected.as_str()) {
                    return;
                }
                shell.show_newest(false);
                shell.away_from_bottom.set(false);
            });
        }
        self.creating.set(false);
        self.focus_composer();
    }

    /// Puts the open composer's text and previews aside for that bot.
    fn remember_unsent(self) {
        let Some(bot_id) = self.selected.get() else {
            return;
        };
        let text = self.draft.get();
        let previews = self.previews.get();
        self.unsent
            .update(|rows| park_unsent(rows, bot_id, text, previews));
    }

    /// Shows the unsent line for `bot_id`, or an empty composer.
    fn apply_unsent(self, bot_id: &str) {
        let found = self.unsent.with(|rows| parked(rows, bot_id).cloned());
        match found {
            Some(row) => {
                self.draft.set(row.text);
                self.previews.set(row.previews);
            }
            None => {
                self.draft.set(String::new());
                self.previews.set(Vec::new());
            }
        }
    }

    fn drop_unsent(self, bot_id: &str) {
        let bot_id = bot_id.to_owned();
        self.unsent
            .update(|rows| rows.retain(|row| row.bot_id != bot_id));
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
        self.focus_composer();
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

    /// Cards and chips belong to a bot that has no transcript lines.
    pub fn shows_suggestions(self) -> bool {
        suggestion_cards_visible(
            self.creating.get(),
            self.selected.get().is_some(),
            self.line_count(),
        )
    }

    /// Writes `prompt` into the open bot's composer and focuses the field.
    /// The transcript stays as it was. Nothing is sent, and no model is asked.
    pub fn fill_composer(self, prompt: &str) {
        let Some(text) = composer_fill(
            self.creating.get(),
            self.selected.get().is_some(),
            prompt,
        ) else {
            return;
        };
        self.draft.set(text);
        self.focus_composer();
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
            lines.insert(0, local_outgoing(id, bot_id.clone(), text, previews));
        });
        self.bots.update(|bots| {
            if let Some(bot) = bots.iter_mut().find(|bot| bot.id == bot_id) {
                bot.summary = LOCAL_CAPTION.to_owned();
            }
        });
        self.draft.set(String::new());
        self.previews.set(Vec::new());
        self.drop_unsent(&bot_id);
        self.away_from_bottom.set(false);
        self.show_newest(false);
        self.focus_composer();
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
        self.show_newest(false);
        self.focus_composer();
    }

    pub fn remove_preview(self, id: String) {
        self.previews
            .update(|items| items.retain(|item| item.id != id));
        self.focus_composer();
    }

    pub fn toggle_details(self) {
        self.details_open.update(|open| *open = !*open);
    }

    pub fn close_details(self) {
        self.details_open.set(false);
    }

    pub fn show_library(self, library: bool) {
        self.edit_panel(|panel| panel.library = library);
        self.details_open.set(true);
    }

    /// Library tab for the bot on screen. Activity is the other tab.
    pub fn library_on(self) -> bool {
        self.panel().library
    }

    pub fn reveal_activity(self) {
        self.edit_panel(|panel| panel.more = true);
    }

    pub fn activity_more_on(self) -> bool {
        self.panel().more
    }

    pub fn toggle_suggested(self) {
        self.edit_panel(|panel| panel.suggested_hidden = !panel.suggested_hidden);
    }

    pub fn suggested_hidden_on(self) -> bool {
        self.panel().suggested_hidden
    }

    /// Opens a demo row, or closes it when it is already open.
    pub fn toggle_activity(self, id: String) {
        self.edit_panel(|panel| toggle_expanded(&mut panel.expanded, id));
    }

    pub fn activity_expanded(self, id: &str) -> bool {
        self.panel().expanded.iter().any(|item| item == id)
    }

    /// Opens the demo blurb, or closes it when it is already open.
    /// The flag stays on this bot. It does not navigate, share, or send.
    pub fn toggle_project(self) {
        self.edit_panel(|panel| panel.project = !panel.project);
    }

    /// The Project blurb for the bot on screen.
    pub fn project_on(self) -> bool {
        self.panel().project
    }

    /// The mark under this line, if the row draws one.
    pub fn mark_at(self, index: usize) -> Option<LineMark> {
        let id = self.selected.get();
        self.lines.with(|lines| {
            let mine: Vec<bool> = lines
                .iter()
                .filter(|line| Some(&line.bot_id) == id.as_ref())
                .map(|line| line.mine)
                .collect();
            line_mark(&mine, index)
        })
    }

    pub fn bot_name(self) -> String {
        self.selected_bot().map(|bot| bot.name).unwrap_or_default()
    }

    fn panel(self) -> Panel {
        let id = self.selected.get().unwrap_or_default();
        self.panels.with(|rows| read_panel(rows, &id))
    }

    fn edit_panel(self, edit: impl FnOnce(&mut Panel)) {
        let Some(id) = self.selected.get() else {
            return;
        };
        self.panels
            .update(|rows| write_panel(rows, &id, edit));
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
            self.set_query(String::new());
            self.focus_active_field();
        } else if self.creating.get() {
            self.cancel_new();
        } else if self.notice.get().is_some() {
            self.dismiss_notice();
        } else if self.details_open.get() {
            self.close_details();
        }
    }

    /// Puts item 0, the newest line, at the bottom of the inverted transcript.
    ///
    /// `scroll_to_end` on that list is content offset 0, which is the oldest
    /// line. The list rests at the high offset, where item 0 is in view.
    pub fn show_newest(self, animated: bool) {
        self.transcript
            .scroll_to_index(0, percentage(0.0), px(0.0), animated);
    }

    pub fn jump_bottom(self) {
        self.away_from_bottom.set(false);
        self.show_newest(true);
    }

    pub fn note_scroll(self, offset_y: f32, content_h: f32, view_h: f32) {
        let max = (content_h - view_h).max(0.0);
        self.away_from_bottom.set(max - offset_y > 56.0);
    }

    pub fn activity(self) -> Vec<Activity> {
        match self.selected_bot() {
            Some(bot) => activity_for(&bot, self.activity_more_on()),
            None => Vec::new(),
        }
    }

    pub fn files(self) -> Vec<String> {
        match self.selected_bot() {
            Some(bot) => files_for(&bot),
            None => Vec::new(),
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

fn read_panel(rows: &[Panel], id: &str) -> Panel {
    rows.iter()
        .find(|row| row.bot_id == id)
        .cloned()
        .unwrap_or_else(|| Panel::empty(id.to_owned()))
}

fn write_panel(rows: &mut Vec<Panel>, id: &str, edit: impl FnOnce(&mut Panel)) {
    if let Some(row) = rows.iter_mut().find(|row| row.bot_id == id) {
        edit(row);
    } else {
        let mut row = Panel::empty(id.to_owned());
        edit(&mut row);
        rows.push(row);
    }
}

fn toggle_expanded(ids: &mut Vec<String>, id: String) {
    if let Some(at) = ids.iter().position(|item| item == &id) {
        ids.remove(at);
    } else {
        ids.push(id);
    }
}

fn activity_for(bot: &Bot, more: bool) -> Vec<Activity> {
    if !bot.demo || bot.id == "desk" || bot.id == "shelf" {
        return Vec::new();
    }
    let mut rows = vec![
        sample("sample", "Demo sample", "Demo note. This sample is not a run."),
        sample("label", "Demo label", "Demo note. A label only, still in this window."),
        sample(
            "reminder",
            "Demo reminder",
            "Demo note. Nothing was scheduled.",
        ),
    ];
    if more {
        rows.push(sample(
            "archive",
            "Demo archive",
            "Demo note. An older sample.",
        ));
        rows.push(sample(
            "margin",
            "Demo margin",
            "Demo note. The rest of the sample list.",
        ));
    }
    rows
}

fn files_for(bot: &Bot) -> Vec<String> {
    if bot.id == "philo" {
        vec!["Demo note".to_owned(), "Demo sketch".to_owned()]
    } else if bot.demo && bot.id != "desk" && bot.id != "shelf" {
        vec!["Demo file".to_owned()]
    } else {
        Vec::new()
    }
}

fn sample(id: &str, title: &str, note: &str) -> Activity {
    Activity {
        id: id.to_owned(),
        title: title.to_owned(),
        status: "Demo".to_owned(),
        note: note.to_owned(),
    }
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
    // Newest first. The oldest note is wide enough to pass behind the
    // name pill. Relay has one local demo line. Desk and Shelf stay empty.
    let notes = [
        "Demo note. Sample text.",
        "Demo note. No model was asked.",
        "Demo note. This row is sample copy.",
        "Demo note. The window wrote this.",
        "Demo note. Not a run.",
        "Demo note. Older sample.",
        "Demo note. Older sample, still in this window only.",
    ];
    let mine = [
        "Demo line. Kept in this window.",
        "Another demo line. Still local.",
        "Demo line for the scroll.",
        "Demo line, still not sent.",
        "One more demo line.",
        "Demo line near the older end.",
        "Demo line. The one at the older end.",
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

/// A demo prompt for an empty chat. The press writes `prompt` into the
/// composer. It does not name a tool, and it does not run one.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub struct Starter {
    pub title: &'static str,
    pub detail: &'static str,
    pub prompt: &'static str,
    pub icon: &'static str,
    pub color: &'static str,
}

const STARTER_CARDS: [Starter; 4] = [
    Starter {
        title: "A short greeting",
        detail: "Hello, kept in this window",
        prompt: "Write a short greeting I can keep in this window.",
        icon: "sparkle",
        color: "#f93598",
    },
    Starter {
        title: "Three local notes",
        detail: "Plain lines, still only a draft",
        prompt: "List three short notes for this empty chat.",
        icon: "plus",
        color: "#7c6ad6",
    },
    Starter {
        title: "A rough plan",
        detail: "A few steps to edit first",
        prompt: "Sketch a few steps I can edit before sending.",
        icon: "clock",
        color: "#e0a23a",
    },
    Starter {
        title: "A reminder",
        detail: "One line that stays here",
        prompt: "Leave a reminder that this note stays on this machine.",
        icon: "check",
        color: "#2f9d78",
    },
];

const STARTER_CHIPS: [Starter; 4] = [
    Starter {
        title: "Say hello",
        detail: "",
        prompt: "Say hello",
        icon: "",
        color: "",
    },
    Starter {
        title: "What is this window?",
        detail: "",
        prompt: "What is this window?",
        icon: "",
        color: "",
    },
    Starter {
        title: "A local reminder",
        detail: "",
        prompt: "A local reminder",
        icon: "",
        color: "",
    },
    Starter {
        title: "Three draft points",
        detail: "",
        prompt: "Three draft points",
        icon: "",
        color: "",
    },
];

pub fn starter_cards() -> &'static [Starter] {
    &STARTER_CARDS
}

pub fn starter_chips() -> &'static [Starter] {
    &STARTER_CHIPS
}

/// The caption on a line this window stored. It is not a model reply.
pub const LOCAL_CAPTION: &str = "Local · not sent";

/// Shown while this bot's Project pill is open. It names no real project.
pub const PROJECT_BLURB: &str = "Demo project. Nothing is opened or sent.";

/// What a transcript row draws under its bubble.
/// `mine` in [`line_mark`] is newest first, the same order as `Shell::line_at`.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum LineMark {
    /// One bot note, or one local line on its own.
    Messaged,
    /// Two or more local lines in a row. Drawn once, on the newest of them.
    Count(usize),
}

impl LineMark {
    /// The words before the bot name. The name and the Project pill follow.
    pub fn prefix(self) -> String {
        match self {
            Self::Messaged => "Messaged".to_owned(),
            Self::Count(count) => format!("{count} messages with"),
        }
    }
}

/// The mark for `mine[index]`. A bot note is `Messaged`. A local line on its
/// own is `Messaged`. A run of two or more local lines is the count, and only
/// the newest line of that run carries it.
pub fn line_mark(mine: &[bool], index: usize) -> Option<LineMark> {
    let mine_line = *mine.get(index)?;
    if !mine_line {
        return Some(LineMark::Messaged);
    }
    let newest = run_newest(mine, index);
    let count = run_count(mine, newest);
    if count >= 2 {
        if index == newest {
            Some(LineMark::Count(count))
        } else {
            None
        }
    } else {
        Some(LineMark::Messaged)
    }
}

/// Newest index of the local run that contains `index`. `mine` is newest first,
/// so the newest line is the smallest index.
fn run_newest(mine: &[bool], index: usize) -> usize {
    let mut at = index;
    while at > 0 && mine[at - 1] {
        at -= 1;
    }
    at
}

fn run_count(mine: &[bool], newest: usize) -> usize {
    mine.iter()
        .skip(newest)
        .take_while(|mine_line| **mine_line)
        .count()
}

fn suggestion_cards_visible(creating: bool, has_bot: bool, line_count: usize) -> bool {
    !creating && has_bot && line_count == 0
}

/// The draft a card or chip would write. `None` leaves the composer alone.
fn composer_fill(creating: bool, has_bot: bool, prompt: &str) -> Option<String> {
    if creating || !has_bot {
        None
    } else {
        Some(prompt.to_owned())
    }
}

fn park_unsent(rows: &mut Vec<Unsent>, bot_id: String, text: String, previews: Vec<Preview>) {
    rows.retain(|row| row.bot_id != bot_id);
    if !text.is_empty() || !previews.is_empty() {
        rows.push(Unsent {
            bot_id,
            text,
            previews,
        });
    }
}

fn parked<'a>(rows: &'a [Unsent], bot_id: &str) -> Option<&'a Unsent> {
    rows.iter().find(|row| row.bot_id == bot_id)
}

fn local_outgoing(id: String, bot_id: String, body: String, previews: Vec<String>) -> Line {
    Line {
        id,
        bot_id,
        mine: true,
        body,
        previews,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bot_named(id: &str) -> Bot {
        seed_bots()
            .into_iter()
            .find(|bot| bot.id == id)
            .unwrap_or_else(|| panic!("seed bot {id}"))
    }

    #[test]
    fn empty_chat_prompts_fill_a_draft_and_a_local_line_hides_them() {
        assert_eq!(starter_cards().len(), 4);
        assert_eq!(starter_chips().len(), 4);
        assert!(suggestion_cards_visible(false, true, 0));
        assert!(!suggestion_cards_visible(false, true, 1));
        assert!(!suggestion_cards_visible(true, true, 0));
        assert!(!suggestion_cards_visible(false, false, 0));

        let prompt = starter_cards()[0].prompt;
        let mut lines = Vec::new();
        let filled = composer_fill(false, true, prompt).expect("open bot");
        assert_eq!(filled, prompt);
        assert!(lines.is_empty());
        assert!(composer_fill(true, true, prompt).is_none());

        let line = local_outgoing("line-1".to_owned(), "desk".to_owned(), filled, Vec::new());
        assert!(line.mine);
        assert_eq!(line.body, prompt);
        assert!(line.previews.is_empty());
        lines.push(line);
        assert!(!suggestion_cards_visible(false, true, lines.len()));
        assert!(lines.iter().all(|item| item.mine));
        assert_eq!(LOCAL_CAPTION, "Local · not sent");
    }

    #[test]
    fn a_chip_draft_stays_with_its_bot() {
        let mut rows = Vec::new();
        park_unsent(
            &mut rows,
            "philo".to_owned(),
            "Philo's unsent line".to_owned(),
            Vec::new(),
        );
        park_unsent(
            &mut rows,
            "notes".to_owned(),
            "Notes draft".to_owned(),
            Vec::new(),
        );
        let desk = composer_fill(false, true, starter_chips()[3].prompt).unwrap();
        assert_eq!(desk, "Three draft points");
        park_unsent(&mut rows, "desk".to_owned(), desk, Vec::new());
        assert_eq!(parked(&rows, "philo").unwrap().text, "Philo's unsent line");
        assert_eq!(parked(&rows, "notes").unwrap().text, "Notes draft");
        assert_eq!(parked(&rows, "desk").unwrap().text, "Three draft points");
        park_unsent(&mut rows, "shelf".to_owned(), String::new(), Vec::new());
        assert!(parked(&rows, "shelf").is_none());
        assert!(suggestion_cards_visible(false, true, 0));
    }

    #[test]
    fn starter_copy_does_not_name_a_tool() {
        let banned = [
            "Search the web",
            "Describe a photo",
            "Run some code",
            "Weather in Lisbon",
        ];
        for starter in starter_cards().iter().chain(starter_chips()) {
            assert!(!starter.prompt.is_empty());
            assert!(!starter.title.is_empty());
            for word in banned {
                assert!(!starter.title.contains(word));
                assert!(!starter.detail.contains(word));
                assert!(!starter.prompt.contains(word));
            }
        }
        assert!(starter_cards().iter().all(|card| !card.detail.is_empty()));
        assert!(starter_chips().iter().all(|chip| chip.prompt == chip.title));
    }

    #[test]
    fn closing_the_column_keeps_the_bots_tab_and_row() {
        let mut panels = Vec::new();
        let mut open = true;
        assert!(open);
        write_panel(&mut panels, "philo", |panel| panel.library = true);
        write_panel(&mut panels, "philo", |panel| {
            toggle_expanded(&mut panel.expanded, "sample".to_owned());
        });
        write_panel(&mut panels, "philo", |panel| panel.suggested_hidden = true);
        write_panel(&mut panels, "philo", |panel| panel.more = true);
        open = false;
        assert!(!open);
        let philo = read_panel(&panels, "philo");
        assert!(philo.library);
        assert_eq!(philo.expanded, vec!["sample".to_owned()]);
        assert!(philo.suggested_hidden);
        assert!(philo.more);
    }

    #[test]
    fn library_tab_and_expanded_row_stay_with_the_bot() {
        let mut panels = Vec::new();
        write_panel(&mut panels, "philo", |panel| {
            panel.library = true;
            panel.suggested_hidden = true;
            panel.more = true;
            toggle_expanded(&mut panel.expanded, "sample".to_owned());
        });
        let philo_bot = bot_named("philo");
        assert_eq!(activity_for(&philo_bot, true).len(), 5);

        let notes = read_panel(&panels, "notes");
        assert!(!notes.library);
        assert!(notes.expanded.is_empty());
        assert!(!notes.suggested_hidden);
        assert!(!notes.more);
        assert_eq!(activity_for(&bot_named("notes"), notes.more).len(), 3);
        write_panel(&mut panels, "notes", |panel| {
            toggle_expanded(&mut panel.expanded, "label".to_owned());
        });

        let philo = read_panel(&panels, "philo");
        assert!(philo.library);
        assert!(philo.expanded.iter().any(|id| id == "sample"));
        assert!(!philo.expanded.iter().any(|id| id == "label"));
        assert!(philo.suggested_hidden);
        assert!(philo.more);
        let row = activity_for(&philo_bot, philo.more)
            .into_iter()
            .find(|item| item.id == "sample")
            .expect("sample row");
        assert_eq!(row.status, "Demo");
        assert!(row.note.contains("not a run"));

        write_panel(&mut panels, "philo", |panel| {
            toggle_expanded(&mut panel.expanded, "sample".to_owned());
        });
        assert!(read_panel(&panels, "philo").expanded.is_empty());
        assert!(
            read_panel(&panels, "notes")
                .expanded
                .iter()
                .any(|id| id == "label")
        );
    }

    #[test]
    fn library_names_are_not_the_activity_rows() {
        let philo = bot_named("philo");
        let rows = activity_for(&philo, false);
        assert!(rows.iter().all(|row| row.status == "Demo"));
        assert!(rows.iter().all(|row| !row.note.is_empty()));
        let files = files_for(&philo);
        assert!(!files.is_empty());
        assert!(files.iter().all(|name| rows.iter().all(|row| {
            row.title != *name && row.id != *name && row.note != *name
        })));
        assert!(activity_for(&bot_named("desk"), false).is_empty());
        assert!(files_for(&bot_named("desk")).is_empty());
        let local = Bot {
            id: "local-1".to_owned(),
            name: "Local".to_owned(),
            summary: "Local".to_owned(),
            group: "unassigned".to_owned(),
            shape: 1,
            demo: false,
        };
        assert!(activity_for(&local, true).is_empty());
        assert!(files_for(&local).is_empty());
    }

    #[test]
    fn a_note_and_a_single_local_line_say_messaged() {
        // Newest first, the way Philo's seed alternates.
        let mine = [true, false, true, false];
        assert_eq!(line_mark(&mine, 0), Some(LineMark::Messaged));
        assert_eq!(line_mark(&mine, 1), Some(LineMark::Messaged));
        assert_eq!(line_mark(&mine, 2), Some(LineMark::Messaged));
        assert_eq!(line_mark(&mine, 3), Some(LineMark::Messaged));
        assert_eq!(LineMark::Messaged.prefix(), "Messaged");
        assert!(line_mark(&[], 0).is_none());
    }

    #[test]
    fn consecutive_local_lines_count_once_on_the_newest() {
        let mine = [true, true, false, true];
        assert_eq!(line_mark(&mine, 0), Some(LineMark::Count(2)));
        assert_eq!(line_mark(&mine, 1), None);
        assert_eq!(line_mark(&mine, 2), Some(LineMark::Messaged));
        assert_eq!(line_mark(&mine, 3), Some(LineMark::Messaged));
        assert_eq!(LineMark::Count(2).prefix(), "2 messages with");

        let three = [true, true, true];
        assert_eq!(line_mark(&three, 0), Some(LineMark::Count(3)));
        assert_eq!(line_mark(&three, 1), None);
        assert_eq!(line_mark(&three, 2), None);
        assert_eq!(LineMark::Count(3).prefix(), "3 messages with");

        // A run that is not the newest row still counts on its own newest line.
        let older = [false, true, true, false];
        assert_eq!(line_mark(&older, 0), Some(LineMark::Messaged));
        assert_eq!(line_mark(&older, 1), Some(LineMark::Count(2)));
        assert_eq!(line_mark(&older, 2), None);
        assert_eq!(line_mark(&older, 3), Some(LineMark::Messaged));
    }

    #[test]
    fn the_project_blurb_stays_with_its_bot() {
        let mut panels = Vec::new();
        write_panel(&mut panels, "philo", |panel| panel.project = true);
        assert!(read_panel(&panels, "philo").project);
        assert!(!read_panel(&panels, "desk").project);
        write_panel(&mut panels, "desk", |panel| panel.project = true);
        write_panel(&mut panels, "desk", |panel| panel.project = false);
        assert!(read_panel(&panels, "philo").project);
        assert!(!read_panel(&panels, "desk").project);
        assert_eq!(PROJECT_BLURB, "Demo project. Nothing is opened or sent.");
    }

    #[test]
    fn one_sent_line_is_messaged_and_a_second_counts() {
        let first = local_outgoing("line-1".into(), "desk".into(), "Hello".into(), Vec::new());
        assert!(first.mine);
        assert_eq!(line_mark(&[first.mine], 0), Some(LineMark::Messaged));
        let second = local_outgoing("line-2".into(), "desk".into(), "Again".into(), Vec::new());
        // Newest first, the order `send` inserts.
        assert_eq!(
            line_mark(&[second.mine, first.mine], 0),
            Some(LineMark::Count(2))
        );
        assert_eq!(line_mark(&[second.mine, first.mine], 1), None);
        assert_eq!(LOCAL_CAPTION, "Local · not sent");
    }
}
