//! Two columns by default. The details column opens on the right.
//! Unfinished destinations stay in that column and say they are not available.

use nsh_base::{button, theme};
use nsh_core::prelude::*;

use crate::model::Agent;
use crate::pages;
use crate::store::{Route, Store};
use crate::ui;

pub fn shell(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .flex_row()
        .items_stretch()
        .overflow_hidden()
        .bg(move || theme().background)
        .child(sidebar(store))
        .child(main_column(store))
        .child(details(store))
}

fn sidebar(store: Store) -> impl IntoElement {
    view()
        .w(px(248.0))
        .flex_shrink_0()
        .h_full()
        .overflow_hidden()
        .bg(move || theme().sidebar)
        .border_r_1()
        .border_color(move || theme().sidebar_border)
        .child(sidebar_tools(store))
        .child(brand())
        .child(agent_pane(store))
        .child(sidebar_footer(store))
}

fn main_column(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .min_w(px(0.0))
        .h_full()
        .bg(move || theme().background)
        .child(banners(store))
        .child(pages::chat(store))
        .child(pages::agent_form(store))
        .child(pages::settings(store))
        .child(pages::connectors(store))
        .child(pages::unavailable(
            store,
            Route::Attachments,
            "Attachments",
            "File attachments are not available yet. Messages in this version are text only.",
        ))
        .child(pages::unavailable(
            store,
            Route::Notifications,
            "Notifications",
            "Notifications are not available yet. Philo does not deliver or list them.",
        ))
        .child(pages::unavailable(
            store,
            Route::Skills,
            "Skills",
            "Skills are not available yet. An agent cannot load or run a skill from here.",
        ))
        .child(pages::unavailable(
            store,
            Route::Memory,
            "Memory",
            "Memory is not available yet. Philo does not keep notes beyond the server transcript.",
        ))
        .child(pages::unavailable(
            store,
            Route::Routines,
            "Routines",
            "Routines are not available yet. Nothing here runs on a schedule.",
        ))
        .child(pages::unavailable(
            store,
            Route::ToolCalls,
            "Tool calls",
            "Tool-call transcripts are not available yet. The one action this version runs is the approval-backed draft, in the bot's chat.",
        ))
}

fn sidebar_tools(store: Store) -> impl IntoElement {
    view()
        .flex_row()
        .justify_end()
        .items_center()
        .gap_2()
        .px_3()
        .pt_3()
        .child(ui::round_button(
            "Search",
            "⌕",
            false,
            || true,
            move || store.not_available("Search"),
        ))
        .child(ui::round_button(
            "New bot",
            "+",
            false,
            move || !store.sending.get(),
            move || store.open_new_agent(),
        ))
}

fn brand() -> impl IntoElement {
    view()
        .items_center()
        .px_3()
        .pt_3()
        .pb_2()
        .gap_1()
        .child(
            view()
                .w(px(52.0))
                .h(px(52.0))
                .rounded(px(26.0))
                .items_center()
                .justify_center()
                .bg(move || theme().primary)
                .child(
                    text("P")
                        .text_size(px(22.0))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(move || theme().primary_foreground),
                ),
        )
        .child(
            text("Philo")
                .text_size(px(15.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || theme().sidebar_foreground),
        )
        .child(
            text("Your agent, your friend. Always on for you.")
                .text_size(px(11.0))
                .text_color(move || theme().muted_foreground),
        )
}

fn sidebar_footer(store: Store) -> impl IntoElement {
    view()
        .flex_row()
        .items_center()
        .gap_2()
        .px_3()
        .py_3()
        .border_t_1()
        .border_color(move || theme().sidebar_border)
        .child(
            view()
                .w(px(28.0))
                .h(px(28.0))
                .rounded(px(14.0))
                .items_center()
                .justify_center()
                .bg(move || theme().sidebar_accent)
                .child(
                    text("P")
                        .text_size(px(13.0))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(move || theme().sidebar_foreground),
                ),
        )
        .child(
            text("Philo")
                .flex_1()
                .number_of_lines(1)
                .text_size(px(13.0))
                .text_color(move || theme().sidebar_foreground),
        )
        .child(ui::round_button(
            "More",
            "▣",
            false,
            || true,
            move || store.toggle_details(),
        ))
}

fn agent_pane(store: Store) -> impl IntoElement {
    let agents = store.agents;
    view()
        .flex_1()
        .min_h(px(0.0))
        .px_2()
        .child(when(
            move || agents.with(Vec::is_empty),
            move || {
                view().px_2().py_2().child(if_error_or(
                    store,
                    "No bots yet. Create one to start a chat.",
                    "Bots will show here once the server responds.",
                ))
            },
        ))
        .child(agent_list(store, agents))
}

fn agent_list(store: Store, agents: Signal<Vec<Agent>>) -> impl IntoElement {
    virtual_list(
        move || agents.with(Vec::len),
        move |at| {
            button()
                .w_full()
                .flex_row()
                .items_center()
                .gap_2()
                .px_3()
                .py_2()
                .rounded(px(8.0))
                .bg(move || {
                    let selected = store.selected_agent.get();
                    let mine = agents.with(|list| {
                        list.get(at.index())
                            .is_some_and(|agent| Some(&agent.id) == selected.as_ref())
                    });
                    if mine {
                        theme().sidebar_accent
                    } else {
                        theme().sidebar
                    }
                })
                .hover(|style| style.bg(move || theme().sidebar_accent))
                .on_press(move || {
                    if let Some(id) =
                        agents.with(|list| list.get(at.index()).map(|agent| agent.id.clone()))
                    {
                        store.select_agent(id);
                    }
                })
                .child(initial_mark(move || {
                    agents.with(|list| {
                        list.get(at.index())
                            .and_then(|agent| agent.name.chars().next())
                            .unwrap_or('P')
                    })
                }))
                .child(
                    view()
                        .flex_1()
                        .min_w(px(0.0))
                        .child(
                            text(move || {
                                agents.with(|list| {
                                    list.get(at.index())
                                        .map(|agent| agent.name.clone())
                                        .unwrap_or_default()
                                })
                            })
                            .number_of_lines(1)
                            .text_size(px(14.0))
                            .text_color(move || theme().sidebar_foreground),
                        )
                        .child(
                            text(move || {
                                agents.with(|list| {
                                    list.get(at.index())
                                        .map(|agent| store.preview(&agent.id))
                                        .unwrap_or_else(|| "No messages yet".to_owned())
                                })
                            })
                            .number_of_lines(1)
                            .text_size(px(12.0))
                            .text_color(move || theme().muted_foreground),
                        ),
                )
        },
    )
    .keys(move || {
        agents.with(|list| {
            list.iter()
                .map(|agent| agent.id.clone())
                .collect::<Vec<_>>()
        })
    })
    .estimated_item_size(px(40.0))
    .row_gap(px(4.0))
    .flex_1()
}

fn details(store: Store) -> impl IntoElement {
    view()
        .w(px(268.0))
        .flex_shrink_0()
        .h_full()
        .overflow_hidden()
        .bg(move || theme().background)
        .border_l_1()
        .border_color(move || theme().border)
        .style_when(move || !store.details_open.get(), |style| style.hidden())
        .child(details_bar(store))
        .child(
            scroll().flex_1().min_h(px(0.0)).child(
                view()
                    .px_3()
                    .pb_4()
                    .gap_3()
                    .child(details_identity(store))
                    .child(details_tabs(store))
                    .child(
                        when(move || store.library_tab.get(), move || library_pane())
                            .otherwise(move || activity_pane(store)),
                    ),
            ),
        )
}

fn details_bar(store: Store) -> impl IntoElement {
    view()
        .flex_row()
        .justify_end()
        .px_2()
        .pt_2()
        .child(ui::round_button(
            "Close details",
            "×",
            false,
            || true,
            move || store.set_details(false),
        ))
}

fn details_identity(store: Store) -> impl IntoElement {
    view()
        .items_center()
        .gap_2()
        .pt_1()
        .child(
            view()
                .w(px(64.0))
                .h(px(64.0))
                .rounded(px(32.0))
                .items_center()
                .justify_center()
                .bg(move || theme().primary)
                .child(
                    text(move || {
                        let name = store.selected_agent_name();
                        name.chars()
                            .next()
                            .map(|ch| ch.to_string())
                            .unwrap_or_else(|| "P".to_owned())
                    })
                    .text_size(px(26.0))
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_color(move || theme().primary_foreground),
                ),
        )
        .child(
            text(move || {
                let name = store.selected_agent_name();
                if name.is_empty() {
                    "Philo".to_owned()
                } else {
                    name
                }
            })
            .text_size(px(18.0))
            .font_weight(FontWeight::SEMIBOLD)
            .number_of_lines(1),
        )
}

fn details_tabs(store: Store) -> impl IntoElement {
    view()
        .flex_row()
        .justify_center()
        .gap_4()
        .child(tab_button(store, "Activity", false))
        .child(tab_button(store, "Library", true))
}

fn tab_button(store: Store, label: &'static str, library: bool) -> impl IntoElement {
    button()
        .px_2()
        .py_1()
        .rounded(px(8.0))
        .bg(move || {
            if store.library_tab.get() == library {
                theme().muted
            } else {
                theme().background
            }
        })
        .on_press(move || store.show_library(library))
        .child(
            text(label)
                .text_size(px(14.0))
                .text_color(move || theme().foreground),
        )
}

fn library_pane() -> impl IntoElement {
    view()
        .gap_2()
        .child(ui::section_label("Library"))
        .child(ui::body(
            "Not available yet. Philo does not keep a library of files or prompts in this version.",
        ))
}

fn activity_pane(store: Store) -> impl IntoElement {
    view()
        .gap_3()
        .child(ui::section_label("Chat"))
        .child(
            ui::card()
                .child(
                    text(move || {
                        let name = store.selected_agent_name();
                        if name.is_empty() {
                            "No bot".to_owned()
                        } else {
                            name
                        }
                    })
                    .font_weight(FontWeight::SEMIBOLD)
                    .text_size(px(14.0)),
                )
                .child(
                    text(move || {
                        if store.selected_agent.get().is_none() {
                            "No bot selected".to_owned()
                        } else if store.streaming() {
                            "Streaming a reply".to_owned()
                        } else if store.messages.with(|list| list.is_empty()) {
                            "No messages yet".to_owned()
                        } else {
                            "Idle".to_owned()
                        }
                    })
                    .text_size(px(12.0))
                    .number_of_lines(2)
                    .text_color(move || theme().muted_foreground),
                )
                .child(ui::muted("Live chat state, not a review status.")),
        )
        .child(
            view()
                .flex_row()
                .gap_2()
                .child(ui::secondary_button(
                    "Configure bot",
                    move || store.selected_agent.get().is_some(),
                    move || store.open_edit_agent(),
                ))
                .child(ui::secondary_button(
                    "Reload",
                    move || store.selected_agent.get().is_some(),
                    move || store.reload_transcript(),
                )),
        )
        .child(ui::section_label("Suggested"))
        .child(nav_button(store, Route::Skills, "Skills", true))
        .child(nav_button(store, Route::Routines, "Routines", true))
        .child(ui::section_label("Computer"))
        .child(makez_card(store))
        .child(ui::section_label("Voice chat"))
        .child(ui::muted("Voice chat is not available yet."))
        .child(ui::section_label("More"))
        .child(nav_button(
            store,
            Route::Notifications,
            "Notifications",
            true,
        ))
        .child(nav_button(store, Route::Memory, "Memory", true))
        .child(nav_button(store, Route::Attachments, "Attachments", true))
        .child(nav_button(store, Route::ToolCalls, "Tool calls", true))
        .child(nav_button(store, Route::Settings, "Settings", false))
        .child(nav_button(store, Route::Connectors, "Connectors", false))
        .child(nav_button(store, Route::Chat, "Chat", false))
}

fn makez_card(store: Store) -> impl IntoElement {
    ui::card()
        .child(
            text("Makez")
                .font_weight(FontWeight::SEMIBOLD)
                .text_size(px(14.0)),
        )
        .child(
            text(move || {
                let reported = store.connectors.with(|list| {
                    list.iter()
                        .find(|item| {
                            item.id.eq_ignore_ascii_case("makez")
                                || item.name.eq_ignore_ascii_case("makez")
                        })
                        .map(|item| {
                            if item.connected {
                                "Connected".to_owned()
                            } else if item.reason.trim().is_empty() {
                                "Not connected".to_owned()
                            } else {
                                format!("Not connected. {}", item.reason)
                            }
                        })
                });
                if let Some(line) = reported {
                    line
                } else if store.health.with(|health| {
                    health
                        .as_ref()
                        .is_some_and(|item| item.makez == "disconnected")
                }) {
                    "Not connected. Makez is not configured.".to_owned()
                } else {
                    "Connector status is not available until the server responds.".to_owned()
                }
            })
            .text_size(px(12.0))
            .text_color(move || theme().muted_foreground),
        )
}

fn initial_mark(letter: impl Fn() -> char + 'static) -> impl IntoElement {
    view()
        .w(px(28.0))
        .h(px(28.0))
        .rounded(px(14.0))
        .items_center()
        .justify_center()
        .bg(move || theme().primary)
        .child(
            text(move || letter().to_string())
                .text_size(px(13.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || theme().primary_foreground),
        )
}

fn nav_button(
    store: Store,
    route: Route,
    label: &'static str,
    unfinished: bool,
) -> impl IntoElement {
    button()
        .w_full()
        .flex_row()
        .items_center()
        .gap_2()
        .px_3()
        .py_1()
        .rounded(px(8.0))
        .bg(move || {
            if store.route.get() == route {
                theme().sidebar_accent
            } else {
                theme().sidebar
            }
        })
        .hover(|style| style.bg(move || theme().sidebar_accent))
        .on_press(move || store.show(route))
        .child(
            text(label)
                .flex_1()
                .number_of_lines(1)
                .text_size(px(13.0))
                .text_color(move || theme().sidebar_foreground),
        )
        .child(when(
            move || unfinished,
            || {
                text("Not yet")
                    .text_size(px(11.0))
                    .text_color(move || theme().muted_foreground)
            },
        ))
}

fn banners(store: Store) -> impl IntoElement {
    view()
        .child(when(
            move || {
                store
                    .health
                    .with(|health| health.as_ref().is_some_and(|item| item.adapter == "dev"))
            },
            || {
                view()
                    .px_4()
                    .py_2()
                    .bg(move || theme().muted)
                    .border_b_1()
                    .border_color(move || theme().border)
                    .child(
                        text("Development adapter. This is not a live model.")
                            .text_size(px(13.0))
                            .font_weight(FontWeight::SEMIBOLD)
                            .text_color(move || theme().foreground),
                    )
            },
        ))
        .child(when(
            move || {
                store.health.with(|health| {
                    health
                        .as_ref()
                        .is_some_and(|item| !item.adapter.is_empty() && item.adapter != "dev")
                })
            },
            move || {
                view().px_4().py_2().child(
                    text(move || {
                        store.health.with(|health| {
                            health
                                .as_ref()
                                .map(|item| format!("Adapter: {}", item.adapter))
                                .unwrap_or_default()
                        })
                    })
                    .text_size(px(13.0))
                    .text_color(move || theme().foreground),
                )
            },
        ))
        .child(when(
            move || {
                store.health.with(|health| {
                    health
                        .as_ref()
                        .is_some_and(|item| item.makez == "disconnected")
                })
            },
            || {
                view().px_4().pb_2().child(
                    text("Makez is not connected.")
                        .text_size(px(13.0))
                        .text_color(move || theme().muted_foreground),
                )
            },
        ))
        .child(error_line(store, true))
        .child(error_line(store, false))
}

fn error_line(store: Store, server: bool) -> impl IntoElement {
    when(
        move || {
            if server {
                store.server_error.get().is_some()
            } else {
                store.action_error.get().is_some()
            }
        },
        move || {
            view()
                .px_4()
                .py_2()
                .bg(move || theme().card)
                .border_b_1()
                .border_color(move || theme().destructive)
                .child(
                    text(move || {
                        if server {
                            store.server_error.get().unwrap_or_default()
                        } else {
                            store.action_error.get().unwrap_or_default()
                        }
                    })
                    .text_size(px(13.0))
                    .text_color(move || theme().destructive),
                )
        },
    )
}

fn if_error_or(store: Store, empty: &'static str, failed: &'static str) -> Text {
    text(move || {
        if store.server_error.get().is_some() {
            failed.to_owned()
        } else {
            empty.to_owned()
        }
    })
    .text_size(px(12.0))
    .text_color(move || theme().muted_foreground)
}
