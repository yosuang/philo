//! The main column's pages. Each page stays mounted and hides itself when
//! another route is current, so the visible page is a direct flex child.

use nsh_base::theme;
use nsh_core::prelude::*;

use crate::model::{Approval, Message, Task};
use crate::store::{Route, Store};
use crate::ui;

pub fn chat(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .min_w(px(0.0))
        .style_when(
            move || store.route.get() != Route::Chat,
            |style| style.hidden(),
        )
        .child(chat_header(store))
        .child(transcript(store))
        .child(approval_strip(store))
        .child(task_strip(store))
        .child(composer(store))
}

pub fn agent_form(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .p_5()
        .gap_3()
        .style_when(move || store.route.get() != Route::AgentForm, |style| {
            style.hidden()
        })
        .child(
            text(move || {
                if store.editing_agent.get().is_some() {
                    "Configure agent".to_owned()
                } else {
                    "New agent".to_owned()
                }
            })
            .text_size(px(22.0))
            .font_weight(FontWeight::SEMIBOLD)
            .text_color(move || theme().foreground),
        )
        .child(ui::muted(
            "The server stores the name, instructions, and model id. Philo does not run the model itself.",
        ))
        .child(ui::section_label("Name"))
        .child(
            ui::field("Name")
                .value(move || store.form_name.get())
                .on_change(move |text: &str| store.form_name.set(text.to_owned()))
                .test_id("agent-name"),
        )
        .child(ui::section_label("Instructions"))
        .child(
            ui::field("What this agent is for")
                .value(move || store.form_instructions.get())
                .on_change(move |text: &str| store.form_instructions.set(text.to_owned()))
                .multiline(true)
                .min_h(px(96.0)),
        )
        .child(ui::section_label("Model id"))
        .child(
            ui::field("model id")
                .value(move || store.form_model.get())
                .on_change(move |text: &str| store.form_model.set(text.to_owned()))
                .test_id("agent-model"),
        )
        .child(
            view()
                .flex_row()
                .gap_2()
                .pt_2()
                .child(ui::primary_button(
                    "Save agent",
                    move || !store.sending.get(),
                    move || store.save_agent(),
                ))
                .child(ui::secondary_button(
                    "Back to chat",
                    || true,
                    move || store.show(Route::Chat),
                )),
        )
}

pub fn settings(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .p_5()
        .gap_4()
        .style_when(move || store.route.get() != Route::Settings, |style| {
            style.hidden()
        })
        .child(ui::page_title("Settings"))
        .child(ui::muted(
            "The server address, the adapter banner, and appearance. Other settings are not available yet.",
        ))
        .child(ui::section_label("Server URL"))
        .child(
            ui::field("http://127.0.0.1:8080")
                .value(move || store.server_draft.get())
                .on_change(move |text: &str| store.server_draft.set(text.to_owned()))
                .test_id("server-url"),
        )
        .child(
            view()
                .flex_row()
                .gap_2()
                .child(ui::primary_button(
                    "Save server",
                    || true,
                    move || store.save_server_url(),
                ))
                .child(ui::secondary_button(
                    "Check server",
                    || true,
                    move || store.refresh_all(),
                )),
        )
        .child(ui::section_label("Appearance"))
        .child(text(move || store.appearance.get().label()).text_size(px(14.0)))
        .child(
            view()
                .flex_row()
                .gap_2()
                .child(appearance_button(store, crate::store::AppearanceChoice::Light, "Light"))
                .child(appearance_button(store, crate::store::AppearanceChoice::Dark, "Dark"))
                .child(appearance_button(
                    store,
                    crate::store::AppearanceChoice::System,
                    "System",
                )),
        )
}

pub fn connectors(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .p_5()
        .gap_3()
        .style_when(move || store.route.get() != Route::Connectors, |style| {
            style.hidden()
        })
        .child(ui::page_title("Connectors"))
        .child(ui::muted(
            "Makez and other connectors report their own status. Sign-in is not available yet.",
        ))
        .child(connector_list(store))
        .child(
            ui::card()
                .child(ui::section_label("Sign-in"))
                .child(ui::body("Not available yet."))
                .child(ui::muted(
                    "Connector accounts cannot be linked from Philo in this version. There is no auth flow to start.",
                )),
        )
}

pub fn unavailable(
    store: Store,
    route: Route,
    title: &'static str,
    detail: &'static str,
) -> impl IntoElement {
    view()
        .flex_1()
        .p_5()
        .gap_3()
        .style_when(move || store.route.get() != route, |style| style.hidden())
        .child(ui::page_title(title))
        .child(
            text("Not available yet.")
                .text_size(px(15.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || theme().foreground),
        )
        .child(ui::body(detail))
        .child(ui::secondary_button(
            "Back to chat",
            || true,
            move || store.show(Route::Chat),
        ))
}

fn chat_header(store: Store) -> impl IntoElement {
    view()
        .px_4()
        .py_3()
        .gap_2()
        .border_b_1()
        .border_color(move || theme().border)
        .child(
            text(move || {
                let name = store.selected_agent_name();
                if name.is_empty() {
                    "No agent selected".to_owned()
                } else {
                    name
                }
            })
            .text_size(px(18.0))
            .font_weight(FontWeight::SEMIBOLD)
            .number_of_lines(1),
        )
        .child(
            text(move || {
                let label = store.selected_session_label();
                if label.is_empty() {
                    "No session".to_owned()
                } else {
                    label
                }
            })
            .text_size(px(13.0))
            .text_color(move || theme().muted_foreground)
            .number_of_lines(1),
        )
        .child(
            view()
                .flex_row()
                .gap_2()
                .child(ui::secondary_button(
                    "New session",
                    move || store.selected_agent.get().is_some() && !store.sending.get(),
                    move || store.create_session(),
                ))
                .child(ui::secondary_button(
                    "Configure agent",
                    move || store.selected_agent.get().is_some(),
                    move || store.open_edit_agent(),
                ))
                .child(ui::secondary_button(
                    "Reload",
                    move || store.selected_session.get().is_some(),
                    move || store.reload_transcript(),
                ))
                .child(when(
                    move || store.streaming(),
                    move || {
                        ui::secondary_button("Cancel reply", || true, move || store.cancel_reply())
                    },
                )),
        )
}

fn transcript(store: Store) -> impl IntoElement {
    view().flex_1().min_h(px(0.0)).child(
        when(
            move || store.selected_session.get().is_none() || store.messages.with(Vec::is_empty),
            move || empty_transcript(store),
        )
        .otherwise(move || message_list(store)),
    )
}

fn empty_transcript(store: Store) -> impl IntoElement {
    view()
        .flex_1()
        .items_center()
        .justify_center()
        .p_6()
        .gap_2()
        .child(
            text(move || {
                if store.selected_agent.get().is_none() {
                    "No agent selected".to_owned()
                } else if store.selected_session.get().is_none() {
                    "No session yet".to_owned()
                } else {
                    "No messages yet".to_owned()
                }
            })
            .text_size(px(16.0))
            .font_weight(FontWeight::SEMIBOLD),
        )
        .child(
            text(move || {
                if store.selected_agent.get().is_none() {
                    "Create an agent in the sidebar. Nothing here is a sample.".to_owned()
                } else if store.selected_session.get().is_none() {
                    "Start a session for this agent, then send a message.".to_owned()
                } else if let Some(err) = store.server_error.get().or(store.action_error.get()) {
                    err
                } else {
                    "The transcript is loaded from the server. A reload shows the saved messages."
                        .to_owned()
                }
            })
            .text_size(px(13.0))
            .text_color(move || theme().muted_foreground),
        )
}

fn message_list(store: Store) -> impl IntoElement {
    let messages = store.messages;
    virtual_list(
        move || messages.with(Vec::len),
        move |at| message_row(store, messages, at),
    )
    .keys(move || {
        messages.with(|list| {
            list.iter()
                .rev()
                .map(|message| message.id.clone())
                .collect::<Vec<_>>()
        })
    })
    .inverted()
    .estimated_item_size(px(84.0))
    .content_padding(px(16.0))
    .row_gap(px(8.0))
    .flex_1()
}

fn message_row(store: Store, messages: Signal<Vec<Message>>, at: Position) -> impl IntoElement {
    view().w_full().child(
        when(
            move || message_at(messages, at).is_some_and(|message| message.is_user()),
            move || user_bubble(messages, at),
        )
        .otherwise(move || assistant_bubble(store, messages, at)),
    )
}

fn user_bubble(messages: Signal<Vec<Message>>, at: Position) -> impl IntoElement {
    view().w_full().flex_row().justify_end().child(
        view()
            .max_w(relative(0.78))
            .px_3()
            .py_2()
            .rounded(px(16.0))
            .bg(move || theme().primary)
            .child(
                text(move || {
                    message_at(messages, at)
                        .map(|message| message.content)
                        .unwrap_or_default()
                })
                .text_size(px(15.0))
                .text_color(move || theme().primary_foreground),
            ),
    )
}

fn assistant_bubble(
    store: Store,
    messages: Signal<Vec<Message>>,
    at: Position,
) -> impl IntoElement {
    view().w_full().flex_row().justify_start().child(
        view()
            .max_w(relative(0.78))
            .gap_1()
            .px_3()
            .py_2()
            .rounded(px(16.0))
            .bg(move || theme().muted)
            .child(
                text(move || {
                    message_at(messages, at)
                        .map(|message| message.content)
                        .unwrap_or_default()
                })
                .text_size(px(15.0))
                .text_color(move || theme().foreground),
            )
            .child(assistant_status(messages, at))
            .child(when(
                move || message_at(messages, at).is_some_and(|message| message.can_retry()),
                move || {
                    ui::secondary_button(
                        "Retry",
                        || true,
                        move || {
                            if let Some(id) = message_at(messages, at).map(|message| message.id) {
                                store.retry(id);
                            }
                        },
                    )
                },
            )),
    )
}

fn assistant_status(messages: Signal<Vec<Message>>, at: Position) -> impl IntoElement {
    text(move || {
        let Some(message) = message_at(messages, at) else {
            return String::new();
        };
        if message.is_user() {
            return String::new();
        }
        let mut parts = Vec::new();
        match message.status.as_str() {
            "streaming" => parts.push("Streaming".to_owned()),
            "canceled" => parts.push("Canceled".to_owned()),
            "error" => parts.push({
                let text = message
                    .error
                    .as_ref()
                    .map(crate::model::StoredError::text)
                    .filter(|text| !text.is_empty());
                text.unwrap_or_else(|| "The reply failed.".to_owned())
            }),
            _ => {}
        }
        if let Some(adapter) = message.adapter() {
            parts.push(format!("{adapter} adapter"));
        }
        parts.join(" · ")
    })
    .text_size(px(12.0))
    .text_color(move || {
        let failed = message_at(messages, at).is_some_and(|message| message.status == "error");
        if failed {
            theme().destructive
        } else {
            theme().muted_foreground
        }
    })
}

fn approval_strip(store: Store) -> impl IntoElement {
    when(
        move || store.route.get() == Route::Chat && !store.approvals.with(Vec::is_empty),
        move || {
            view()
                .px_4()
                .py_2()
                .gap_2()
                .border_t_1()
                .border_color(move || theme().border)
                .child(ui::section_label("Approvals"))
                .child(each(
                    move || store.approvals.get(),
                    |approval| approval.id.clone(),
                    move |approval, _| approval_card(store, approval),
                ))
        },
    )
}

fn task_strip(store: Store) -> impl IntoElement {
    when(
        move || store.route.get() == Route::Chat && !store.tasks.with(Vec::is_empty),
        move || {
            view()
                .px_4()
                .py_2()
                .gap_2()
                .border_t_1()
                .border_color(move || theme().border)
                .child(ui::section_label("Tasks"))
                .child(each(
                    move || store.tasks.get(),
                    |task| {
                        if task.id.is_empty() {
                            format!("{}:{}:{}", task.session_id, task.status, task.created_at)
                        } else {
                            task.id.clone()
                        }
                    },
                    move |task, _| task_card(task),
                ))
        },
    )
}

fn approval_card(store: Store, approval: ReadSignal<Approval>) -> impl IntoElement {
    ui::card()
        .test_id("approval-card")
        .child(
            text("Approval")
                .font_weight(FontWeight::SEMIBOLD)
                .text_size(px(13.0)),
        )
        .child(
            text(move || {
                approval.with(|item| {
                    if item.kind.is_empty() {
                        "draft_action".to_owned()
                    } else {
                        item.kind.clone()
                    }
                })
            })
            .text_size(px(12.0))
            .text_color(move || theme().muted_foreground),
        )
        .child(
            text(move || approval.with(|item| item.summary.clone()))
                .text_size(px(15.0))
                .text_color(move || theme().card_foreground),
        )
        .child(
            text(move || {
                let status = approval.with(|item| {
                    if item.status.is_empty() {
                        "pending".to_owned()
                    } else {
                        item.status.clone()
                    }
                });
                format!("Status: {status}")
            })
            .text_size(px(12.0))
            .text_color(move || theme().muted_foreground),
        )
        .child(when(
            move || approval.with(Approval::is_pending),
            move || {
                view()
                    .flex_row()
                    .gap_2()
                    .pt_1()
                    .child(ui::primary_button(
                        "Approve",
                        || true,
                        move || {
                            let id = approval.with(|item| item.id.clone());
                            store.decide(id, "approve");
                        },
                    ))
                    .child(ui::secondary_button(
                        "Reject",
                        || true,
                        move || {
                            let id = approval.with(|item| item.id.clone());
                            store.decide(id, "reject");
                        },
                    ))
            },
        ))
}

fn task_card(task: ReadSignal<Task>) -> impl IntoElement {
    ui::card()
        .test_id("task-card")
        .child(
            text("Task")
                .font_weight(FontWeight::SEMIBOLD)
                .text_size(px(13.0)),
        )
        .child(
            text(move || {
                let source = task.with(|item| {
                    if item.source.is_empty() {
                        "philo-server".to_owned()
                    } else {
                        item.source.clone()
                    }
                });
                format!("Source: {source}")
            })
            .text_size(px(12.0))
            .text_color(move || theme().muted_foreground),
        )
        .child(
            text(move || {
                let status = task.with(|item| {
                    if item.status.is_empty() {
                        "unknown".to_owned()
                    } else {
                        item.status.clone()
                    }
                });
                format!("Status: {status}")
            })
            .text_size(px(15.0)),
        )
        .child(when(
            move || task.with(Task::has_result),
            move || {
                text(move || format!("Result: {}", task.with(|item| item.result_text())))
                    .text_size(px(14.0))
                    .text_color(move || theme().card_foreground)
            },
        ))
}

fn composer(store: Store) -> impl IntoElement {
    let dev = move || {
        store
            .health
            .with(|health| health.as_ref().is_some_and(|item| item.adapter == "dev"))
    };
    view()
        .px_4()
        .py_3()
        .gap_2()
        .border_t_1()
        .border_color(move || theme().border)
        .bg(move || theme().background)
        .child(when(dev, || {
            ui::muted(
                "A line that starts with /draft asks the development adapter for an approval.",
            )
        }))
        .child(
            view()
                .flex_row()
                .items_end()
                .gap_2()
                .child(
                    ui::field("Message")
                        .value(move || store.draft.get())
                        .on_change(move |text: &str| store.draft.set(text.to_owned()))
                        .on_submit(move |_: &str| store.send())
                        .multiline(true)
                        .submit_behavior(SubmitBehavior::Submit)
                        .return_key_type(ReturnKeyType::Send)
                        .flex_1()
                        .max_h(px(120.0))
                        .test_id("composer"),
                )
                .child(ui::primary_button(
                    "Send",
                    move || {
                        !store.sending.get()
                            && !store.streaming()
                            && store.selected_session.get().is_some()
                            && !store.draft.get().trim().is_empty()
                    },
                    move || store.send(),
                )),
        )
}

fn connector_list(store: Store) -> impl IntoElement {
    view().gap_2().child(
        when(
            move || store.connectors.with(Vec::is_empty),
            move || makez_fallback(store),
        )
        .otherwise(move || {
            each(
                move || store.connectors.get(),
                |connector| connector.label(),
                move |connector, _| {
                    ui::card()
                        .child(
                            text(move || connector.with(|item| item.label()))
                                .font_weight(FontWeight::SEMIBOLD)
                                .text_size(px(15.0)),
                        )
                        .child(
                            text(move || {
                                let connected = connector.with(|item| item.connected);
                                if connected {
                                    "Connected".to_owned()
                                } else {
                                    "Not connected".to_owned()
                                }
                            })
                            .text_size(px(14.0))
                            .text_color(move || theme().foreground),
                        )
                        .child(
                            text(move || {
                                let reason = connector.with(|item| item.reason.clone());
                                if reason.trim().is_empty() {
                                    "No reason reported.".to_owned()
                                } else {
                                    reason
                                }
                            })
                            .text_size(px(13.0))
                            .text_color(move || theme().muted_foreground),
                        )
                },
            )
        }),
    )
}

fn makez_fallback(store: Store) -> impl IntoElement {
    ui::card().child(
        text(move || match store.health.get() {
            Some(health) if health.makez == "connected" => {
                "The server reports Makez connected, and returned no connector list.".to_owned()
            }
            Some(_) => {
                "Makez is not connected. It is not configured, or its health check failed. The server returned no connector record with a reason."
                    .to_owned()
            }
            None => store
                .server_error
                .get()
                .unwrap_or_else(|| {
                    "Connector status is unavailable because the server has not responded."
                        .to_owned()
                }),
        })
        .text_size(px(14.0))
        .text_color(move || theme().foreground),
    )
}

fn appearance_button(
    store: Store,
    choice: crate::store::AppearanceChoice,
    label: &'static str,
) -> impl IntoElement {
    nsh_base::button()
        .px_3()
        .py_2()
        .rounded(move || theme().radius)
        .border_1()
        .border_color(move || theme().border)
        .bg(move || {
            if store.appearance.get() == choice {
                theme().primary
            } else {
                theme().secondary
            }
        })
        .on_press(move || store.set_appearance(choice))
        .child(text(label).text_size(px(14.0)).text_color(move || {
            if store.appearance.get() == choice {
                theme().primary_foreground
            } else {
                theme().secondary_foreground
            }
        }))
}

/// Newest message is list index 0 because the transcript list is inverted.
fn message_at(messages: Signal<Vec<Message>>, at: Position) -> Option<Message> {
    messages.with(|list| {
        let index = list.len().saturating_sub(1).saturating_sub(at.index());
        list.get(index).cloned()
    })
}
