//! Sidebar and the main column. Navigation is real; unfinished destinations
//! say so on their page and in the sidebar.

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
        .child(sidebar(store))
        .child(main_column(store))
}

fn sidebar(store: Store) -> impl IntoElement {
    view()
        .w(px(288.0))
        .flex_shrink_0()
        .h_full()
        .bg(move || theme().sidebar)
        .border_r_1()
        .border_color(move || theme().sidebar_border)
        .child(brand())
        .child(label_row("Agents"))
        .child(view().px_3().pb_2().child(ui::secondary_button(
            "New agent",
            move || !store.sending.get(),
            move || store.open_new_agent(),
        )))
        .child(agent_pane(store))
        .child(label_row("Sessions"))
        .child(session_pane(store))
        .child(nav(store))
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
            "Tool-call transcripts are not available yet. The one action this version runs is the approval-backed draft, on the session.",
        ))
}

fn brand() -> impl IntoElement {
    view()
        .px_4()
        .pt_4()
        .pb_3()
        .gap_1()
        .border_b_1()
        .border_color(move || theme().sidebar_border)
        .child(
            text("Philo")
                .text_size(px(22.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || theme().sidebar_foreground),
        )
        .child(
            text("Your agent, your friend. Always on for you.")
                .text_size(px(12.0))
                .text_color(move || theme().muted_foreground),
        )
}

fn label_row(label: &'static str) -> impl IntoElement {
    view().px_4().pt_3().pb_1().child(ui::section_label(label))
}

fn agent_pane(store: Store) -> impl IntoElement {
    let agents = store.agents;
    view()
        .flex_1()
        .min_h(px(72.0))
        .px_2()
        .child(when(
            move || agents.with(Vec::is_empty),
            move || {
                view().px_2().py_2().child(if_error_or(
                    store,
                    "No agents yet. Create one to start a session.",
                    "Agents will show here once the server responds.",
                ))
            },
        ))
        .child(agent_list(store, agents))
}

fn session_pane(store: Store) -> impl IntoElement {
    let sessions = store.sessions;
    view()
        .flex_1()
        .min_h(px(72.0))
        .px_2()
        .child(when(
            move || store.selected_agent.get().is_none() || sessions.with(Vec::is_empty),
            move || {
                view().px_2().py_2().child(
                    text(move || {
                        if store.selected_agent.get().is_none() {
                            "Select an agent to see its sessions.".to_owned()
                        } else if store.server_error.get().is_some() {
                            "Sessions will show here once the server responds.".to_owned()
                        } else {
                            "No sessions yet.".to_owned()
                        }
                    })
                    .text_size(px(12.0))
                    .text_color(move || theme().muted_foreground),
                )
            },
        ))
        .child(session_list(store, sessions))
}

fn agent_list(store: Store, agents: Signal<Vec<Agent>>) -> impl IntoElement {
    virtual_list(
        move || agents.with(Vec::len),
        move |at| {
            button()
                .w_full()
                .flex_row()
                .items_center()
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

fn session_list(store: Store, sessions: Signal<Vec<crate::model::Session>>) -> impl IntoElement {
    virtual_list(
        move || sessions.with(Vec::len),
        move |at| {
            button()
                .w_full()
                .flex_row()
                .items_center()
                .px_3()
                .py_2()
                .rounded(px(8.0))
                .bg(move || {
                    let selected = store.selected_session.get();
                    let mine = sessions.with(|list| {
                        list.get(at.index())
                            .is_some_and(|session| Some(&session.id) == selected.as_ref())
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
                        sessions.with(|list| list.get(at.index()).map(|session| session.id.clone()))
                    {
                        store.select_session(id);
                    }
                })
                .child(
                    text(move || {
                        sessions.with(|list| {
                            list.get(at.index())
                                .map(|session| session.label())
                                .unwrap_or_default()
                        })
                    })
                    .number_of_lines(1)
                    .text_size(px(14.0))
                    .text_color(move || theme().sidebar_foreground),
                )
        },
    )
    .keys(move || {
        sessions.with(|list| {
            list.iter()
                .map(|session| session.id.clone())
                .collect::<Vec<_>>()
        })
    })
    .estimated_item_size(px(40.0))
    .row_gap(px(4.0))
    .flex_1()
}

fn nav(store: Store) -> impl IntoElement {
    view()
        .px_2()
        .py_2()
        .gap_1()
        .border_t_1()
        .border_color(move || theme().sidebar_border)
        .child(nav_button(store, Route::Chat, "Chat", false))
        .child(nav_button(store, Route::Settings, "Settings", false))
        .child(nav_button(store, Route::Connectors, "Connectors", false))
        .child(nav_button(store, Route::Attachments, "Attachments", true))
        .child(nav_button(
            store,
            Route::Notifications,
            "Notifications",
            true,
        ))
        .child(nav_button(store, Route::Skills, "Skills", true))
        .child(nav_button(store, Route::Memory, "Memory", true))
        .child(nav_button(store, Route::Routines, "Routines", true))
        .child(nav_button(store, Route::ToolCalls, "Tool calls", true))
        .child(
            text(move || store.server_url.get())
                .number_of_lines(1)
                .text_size(px(11.0))
                .text_color(move || theme().muted_foreground)
                .px_2()
                .pt_2(),
        )
        .child(
            text(move || store.socket_label.get())
                .number_of_lines(1)
                .text_size(px(11.0))
                .text_color(move || theme().muted_foreground)
                .px_2(),
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
