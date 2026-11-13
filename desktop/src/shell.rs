//! Three columns: bot list, chat, and a details drawer that can close.

use nsh_base::{button, theme};
use nsh_core::element::desktop::title_bar;
use nsh_core::prelude::*;

use crate::local::Shell;
use crate::pages;
use crate::ui;

const SIDEBAR: f32 = 276.0;
const RAIL: f32 = 76.0;
const DETAILS: f32 = 300.0;

pub fn shell(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .flex_row()
        .overflow_hidden()
        .bg(move || theme().background)
        .text_color(move || theme().foreground)
        .child(sidebar(shell))
        .child(pages::column(shell))
        .child(details(shell))
}

fn bar_height() -> Points {
    let top = title_bar().top.0;
    px(top.max(52.0))
}

fn sidebar(shell: Shell) -> impl IntoElement {
    view()
        .w(move || {
            if shell.sidebar_collapsed.get() {
                px(RAIL)
            } else {
                px(SIDEBAR)
            }
        })
        .flex_shrink_0()
        .h_full()
        .overflow_hidden()
        .bg(move || theme().sidebar)
        .border_r_1()
        .border_color(move || theme().sidebar_border)
        .child(
            when(move || shell.sidebar_collapsed.get(), move || rail(shell))
                .otherwise(move || expanded(shell)),
        )
}

fn expanded(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .min_h(px(0.0))
        .child(sidebar_header(shell))
        .child(when(
            move || shell.search_open.get(),
            move || search_field(shell),
        ))
        .child(bot_scroll(shell))
        .child(user_row(shell))
}

fn rail(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .min_h(px(0.0))
        .items_center()
        .pt(move || title_bar().top)
        .gap(px(8.0))
        .child(ui::icon_button(
            shell,
            "Search",
            "search",
            ui::Circle::White,
            || true,
            move || shell.open_search(),
        ))
        .child(ui::icon_button(
            shell,
            "New bot",
            "plus",
            ui::Circle::White,
            || true,
            move || shell.open_new(),
        ))
        .child(
            scroll().flex_1().min_h(px(0.0)).w_full().child(
                view()
                    .w_full()
                    .items_center()
                    .gap(px(8.0))
                    .py(px(8.0))
                    .child(each(
                        move || shell.bots.get(),
                        |bot| bot.id.clone(),
                        move |bot, _| {
                            let id = bot.with(|item| item.id.clone());
                            let shape = bot.with(|item| item.shape);
                            let name = bot.with(|item| item.name.clone());
                            button()
                                .accessibility_label(name)
                                .rounded(px(18.0))
                                .p(px(2.0))
                                .cursor_pointer()
                                .bg(move || {
                                    let selected = shell.selected.get();
                                    let mine = bot.with(|item| Some(&item.id) == selected.as_ref());
                                    if mine {
                                        theme().sidebar_accent
                                    } else {
                                        theme().sidebar
                                    }
                                })
                                .focus(|style| {
                                    style.outline(px(2.0)).outline_color(move || theme().ring)
                                })
                                .on_press(move || shell.select(id.clone()))
                                .child(ui::blob(shape, 32.0))
                        },
                    )),
            ),
        )
        .child(ui::icon_button(
            shell,
            "Expand sidebar",
            "grid",
            ui::Circle::White,
            || true,
            move || shell.toggle_sidebar(),
        ))
        .child(view().h(px(8.0)))
}

fn sidebar_header(shell: Shell) -> impl IntoElement {
    view()
        .h(move || bar_height())
        .flex_row()
        .items_center()
        .pl(move || title_bar().left + px(8.0))
        .pr(px(10.0))
        .gap(px(8.0))
        .window_drag(true)
        .child(view().flex_1().window_drag(true))
        .child(ui::icon_button(
            shell,
            "Search",
            "search",
            ui::Circle::White,
            || true,
            move || shell.open_search(),
        ))
        .child(ui::icon_button(
            shell,
            "New bot",
            "plus",
            ui::Circle::White,
            || true,
            move || shell.open_new(),
        ))
}

fn search_field(shell: Shell) -> impl IntoElement {
    view().px(px(12.0)).pb(px(6.0)).child(
        ui::field("Search")
            .element_ref(&shell.search_field)
            .test_id("search")
            .value(move || shell.query.get())
            .on_change(move |text: &str| shell.query.set(text.to_owned()))
            .w_full()
            .h(px(34.0))
            .rounded(px(17.0))
            .border(px(0.0))
            .shadow_sm()
            .bg(move || theme().card)
            .text_size(px(13.0))
            // The field's border width is 0, so a focus color on that border
            // never draws. The outline is the ring.
            .focus(|style| {
                style
                    .outline(px(2.0))
                    .outline_offset(px(2.0))
                    .outline_color(move || theme().ring)
            }),
    )
}

fn bot_scroll(shell: Shell) -> impl IntoElement {
    scroll().flex_1().min_h(px(0.0)).child(
        view()
            .px(px(8.0))
            .pt(px(4.0))
            .pb(px(8.0))
            .gap(px(4.0))
            .child(group(shell, "demo", "Demo"))
            .child(group(shell, "unassigned", "Unassigned"))
            .child(when(
                move || {
                    shell.search_open.get()
                        && !shell.query.get().trim().is_empty()
                        && !shell.any_match()
                },
                || {
                    view().px(px(12.0)).py(px(16.0)).child(ui::quiet(
                        "No bots match. Demo rows are the only ones here.",
                    ))
                },
            )),
    )
}

fn group(shell: Shell, id: &'static str, title: &'static str) -> impl IntoElement {
    view()
        .gap(px(2.0))
        .child(group_header(shell, id, title))
        .child(when(
            move || shell.group_open(id) && !shell.bots_in(id).is_empty(),
            move || {
                view().gap(px(2.0)).child(each(
                    move || shell.bots_in(id),
                    |bot| bot.id.clone(),
                    move |bot, _| bot_row(shell, bot),
                ))
            },
        ))
}

fn group_header(shell: Shell, id: &'static str, title: &'static str) -> impl IntoElement {
    button()
        .w_full()
        .flex_row()
        .items_center()
        .px(px(10.0))
        .h(move || {
            if shell.group_open(id) {
                px(22.0)
            } else {
                px(32.0)
            }
        })
        .rounded(px(10.0))
        .cursor_pointer()
        .bg(move || {
            if shell.group_open(id) {
                theme().sidebar
            } else {
                rgb(0xe9e9e9).into()
            }
        })
        .hover(|style| style.bg(move || theme().sidebar_accent))
        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
        .accessibility_label(title)
        .on_press(move || shell.toggle_group(id))
        .child(
            text(title)
                .flex_1()
                .text_size(px(13.0))
                .text_color(move || theme().muted_foreground),
        )
        .child(when(
            move || !shell.group_open(id),
            move || ui::glyph(shell, "chevron-right", false),
        ))
}

fn bot_row(shell: Shell, bot: ReadSignal<crate::local::Bot>) -> impl IntoElement {
    let id = bot.with(|item| item.id.clone());
    button()
        .w_full()
        .flex_row()
        .items_center()
        .gap(px(10.0))
        .px(px(8.0))
        .py(px(9.0))
        .rounded(px(14.0))
        .cursor_pointer()
        .bg(move || {
            let selected = shell.selected.get();
            let mine = bot.with(|item| Some(&item.id) == selected.as_ref());
            if mine {
                theme().sidebar_accent
            } else {
                theme().sidebar
            }
        })
        .hover(|style| style.bg(move || theme().sidebar_accent))
        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
        .on_press(move || shell.select(id.clone()))
        .child(ui::blob(bot.with(|item| item.shape), 36.0))
        .child(
            view()
                .flex_1()
                .min_w(px(0.0))
                .gap(px(2.0))
                .child(
                    text(move || bot.with(|item| item.name.clone()))
                        .number_of_lines(1)
                        .text_size(px(14.0))
                        .font_weight(FontWeight::SEMIBOLD)
                        .text_color(move || theme().sidebar_foreground),
                )
                .child(
                    text(move || bot.with(|item| item.summary.clone()))
                        .number_of_lines(1)
                        .text_size(px(12.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
}

fn user_row(shell: Shell) -> impl IntoElement {
    view()
        .flex_row()
        .items_center()
        .gap(px(8.0))
        .px(px(10.0))
        .py(px(8.0))
        .border_t_1()
        .border_color(move || theme().sidebar_border)
        .child(
            button()
                .flex_1()
                .min_w(px(0.0))
                .flex_row()
                .items_center()
                .gap(px(10.0))
                .cursor_pointer()
                .rounded(px(10.0))
                .px(px(4.0))
                .py(px(4.0))
                .hover(|style| style.bg(move || theme().sidebar_accent))
                .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
                .accessibility_label("Demo account")
                .on_press(move || {
                    shell.notice("This is a demo profile. Accounts are not available.");
                })
                .child(ui::portrait(26.0))
                .child(
                    view().flex_1().min_w(px(0.0)).child(
                        text("Demo")
                            .number_of_lines(1)
                            .text_size(px(13.0))
                            .text_color(move || theme().sidebar_foreground),
                    ),
                ),
        )
        .child(ui::icon_button(
            shell,
            "Collapse sidebar",
            "grid",
            ui::Circle::White,
            || true,
            move || shell.toggle_sidebar(),
        ))
}

fn details(shell: Shell) -> impl IntoElement {
    view()
        .w(px(DETAILS))
        .flex_shrink_0()
        .h_full()
        .overflow_hidden()
        .bg(move || theme().background)
        .border_l_1()
        .border_color(move || theme().border)
        .style_when(move || !shell.details_open.get(), |style| style.hidden())
        .child(details_bar(shell))
        .child(
            scroll().flex_1().min_h(px(0.0)).child(
                view()
                    .px(px(16.0))
                    .pb(px(24.0))
                    .gap(px(14.0))
                    .child(identity(shell))
                    .child(tabs(shell))
                    .child(
                        when(move || shell.library.get(), move || library(shell))
                            .otherwise(move || activity(shell)),
                    ),
            ),
        )
}

fn details_bar(shell: Shell) -> impl IntoElement {
    view()
        .h(move || bar_height())
        .flex_row()
        .items_center()
        .justify_end()
        .pr(move || title_bar().right + px(10.0))
        .window_drag(true)
        .child(ui::icon_button(
            shell,
            "Close details",
            "close",
            ui::Circle::White,
            || true,
            move || shell.close_details(),
        ))
}

fn identity(shell: Shell) -> impl IntoElement {
    view()
        .items_center()
        .gap(px(8.0))
        .pt(px(4.0))
        .child(ui::blob_live(
            move || shell.selected_bot().map(|bot| bot.shape).unwrap_or(0),
            64.0,
        ))
        .child(
            text(move || {
                shell
                    .selected_bot()
                    .map(|bot| bot.name)
                    .unwrap_or_else(|| "Philo".to_owned())
            })
            .text_size(px(18.0))
            .font_weight(FontWeight::SEMIBOLD)
            .number_of_lines(1),
        )
        .child(ui::quiet(move || {
            if shell.selected_bot().is_some_and(|bot| bot.demo) {
                "Demo".to_owned()
            } else {
                "Local".to_owned()
            }
        }))
}

fn tabs(shell: Shell) -> impl IntoElement {
    view()
        .self_center()
        .flex_row()
        .p(px(3.0))
        .gap(px(2.0))
        .rounded(px(18.0))
        .bg(move || theme().muted)
        .child(tab(shell, "Activity", false))
        .child(tab(shell, "Library", true))
}

fn tab(shell: Shell, label: &'static str, library: bool) -> impl IntoElement {
    button()
        .px(px(16.0))
        .py(px(6.0))
        .rounded(px(14.0))
        .cursor_pointer()
        .bg(move || {
            if shell.library.get() == library {
                theme().card
            } else {
                theme().muted
            }
        })
        .shadow_sm()
        .style_when(
            move || shell.library.get() != library,
            |style| style.shadow_none(),
        )
        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
        .accessibility_label(label)
        .on_press(move || shell.show_library(library))
        .child(
            text(label)
                .text_size(px(14.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(move || theme().foreground),
        )
}

fn activity(shell: Shell) -> impl IntoElement {
    view()
        .gap(px(10.0))
        .child(ui::section_label("Demo"))
        .child(activity_card(shell))
        .child(suggested(shell))
        .child(computer(shell))
}

fn activity_card(shell: Shell) -> impl IntoElement {
    view().gap(px(8.0)).child(
        when(
            move || shell.activity().is_empty(),
            || {
                card().child(ui::quiet(
                    "No activity. Demo rows are samples, and nothing here is a run.",
                ))
            },
        )
        .otherwise(move || {
            view()
                .gap(px(8.0))
                .child(card().child(each(
                    move || shell.activity(),
                    |row| row.title.clone(),
                    move |row, _| activity_row(shell, row),
                )))
                .child(when(
                    move || {
                        !shell.activity_more.get()
                            && shell.selected_bot().is_some_and(|bot| bot.demo)
                    },
                    move || {
                        button()
                            .self_center()
                            .cursor_pointer()
                            .px(px(8.0))
                            .py(px(4.0))
                            .rounded(px(8.0))
                            .focus(|style| {
                                style.outline(px(2.0)).outline_color(move || theme().ring)
                            })
                            .accessibility_label("Show more")
                            .on_press(move || shell.activity_more.set(true))
                            .child(ui::quiet("Show more"))
                    },
                ))
        }),
    )
}

fn activity_row(shell: Shell, row: ReadSignal<crate::local::Activity>) -> impl IntoElement {
    button()
        .w_full()
        .flex_row()
        .items_center()
        .gap(px(10.0))
        .py(px(8.0))
        .cursor_pointer()
        .rounded(px(8.0))
        .hover(|style| style.bg(move || theme().accent))
        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
        .on_press(move || {
            shell.notice("Demo sample. This is not a run, and no model was asked.");
        })
        .child(ui::tinted(
            if row.with(|item| item.title.contains("sample")) {
                "sparkle"
            } else {
                "check"
            },
            if row.with(|item| item.title.contains("sample")) {
                "#f93598"
            } else {
                "#2f9d78"
            },
        ))
        .child(
            view()
                .flex_1()
                .min_w(px(0.0))
                .child(
                    text(move || row.with(|item| item.title.clone()))
                        .number_of_lines(1)
                        .text_size(px(14.0)),
                )
                .child(
                    text(move || row.with(|item| item.detail.clone()))
                        .number_of_lines(1)
                        .text_size(px(12.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
        .child(ui::glyph(shell, "chevron-right", false))
}

fn suggested(shell: Shell) -> impl IntoElement {
    view()
        .gap(px(8.0))
        .child(
            view()
                .flex_row()
                .items_center()
                .child(ui::section_label("Suggested").flex_1())
                .child(
                    button()
                        .cursor_pointer()
                        .px(px(6.0))
                        .py(px(2.0))
                        .rounded(px(8.0))
                        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
                        .accessibility_label("Hide suggested")
                        .on_press(move || {
                            shell.suggested_hidden.update(|hidden| *hidden = !*hidden);
                        })
                        .child(ui::quiet(move || {
                            if shell.suggested_hidden.get() {
                                "Show".to_owned()
                            } else {
                                "Hide".to_owned()
                            }
                        })),
                ),
        )
        .child(when(
            move || !shell.suggested_hidden.get(),
            move || {
                card()
                    .child(suggest_row(shell, "Share", "Not available", move || {
                        shell.notice("Share is not available.");
                    }))
                    .child(suggest_row(shell, "Voice", "Not available", move || {
                        shell.notice("Voice is not available. The microphone stays off.");
                    }))
            },
        ))
}

fn suggest_row(
    shell: Shell,
    title: &'static str,
    detail: &'static str,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    button()
        .w_full()
        .flex_row()
        .items_center()
        .gap(px(10.0))
        .py(px(8.0))
        .cursor_pointer()
        .rounded(px(8.0))
        .hover(|style| style.bg(move || theme().accent))
        .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
        .accessibility_label(title)
        .on_press(on_press)
        .child(ui::tinted(
            if title == "Voice" { "mic" } else { "sparkle" },
            if title == "Voice" {
                "#6f6f6f"
            } else {
                "#7c6ad6"
            },
        ))
        .child(
            view()
                .flex_1()
                .min_w(px(0.0))
                .child(text(title).text_size(px(14.0)))
                .child(
                    text(detail)
                        .text_size(px(12.0))
                        .text_color(move || theme().muted_foreground),
                ),
        )
        .child(ui::glyph(shell, "chevron-right", false))
}

fn library(shell: Shell) -> impl IntoElement {
    view()
        .gap(px(8.0))
        .child(ui::section_label("Files"))
        .child(ui::quiet(
            "Local names only. Nothing is uploaded, and Connect is not available.",
        ))
        .child(file_list(shell))
}

fn file_list(shell: Shell) -> impl IntoElement {
    when(
        move || shell.files().is_empty(),
        || card().child(ui::quiet("No files in this window.")),
    )
    .otherwise(move || {
        card().child(each(
            move || shell.files(),
            |name| name.clone(),
            move |name, _| {
                button()
                    .w_full()
                    .flex_row()
                    .items_center()
                    .py(px(8.0))
                    .cursor_pointer()
                    .rounded(px(8.0))
                    .hover(|style| style.bg(move || theme().accent))
                    .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
                    .on_press(move || {
                        shell.notice("Demo file. It is not opened, and it is not sent.");
                    })
                    .child(
                        view()
                            .flex_1()
                            .min_w(px(0.0))
                            .child(
                                text(move || name.get())
                                    .number_of_lines(1)
                                    .text_size(px(14.0)),
                            )
                            .child(ui::quiet("Demo file")),
                    )
                    .child(ui::glyph(shell, "chevron-right", false))
            },
        ))
    })
}

fn computer(shell: Shell) -> impl IntoElement {
    view()
        .gap(px(8.0))
        .child(ui::section_label("Computer"))
        .child(
            button()
                .w_full()
                .flex_row()
                .items_center()
                .gap(px(10.0))
                .p(px(12.0))
                .rounded(px(16.0))
                .border_1()
                .border_color(move || theme().border)
                .bg(move || theme().card)
                .cursor_pointer()
                .hover(|style| style.bg(move || theme().accent))
                .focus(|style| style.outline(px(2.0)).outline_color(move || theme().ring))
                .accessibility_label("Connect")
                .on_press(move || shell.notice("Connect is not available."))
                .child(
                    view()
                        .flex_1()
                        .min_w(px(0.0))
                        .gap(px(2.0))
                        .child(
                            text("This computer")
                                .text_size(px(14.0))
                                .font_weight(FontWeight::SEMIBOLD),
                        )
                        .child(ui::quiet("Connect is not available.")),
                )
                .child(
                    view()
                        .w(px(72.0))
                        .h(px(48.0))
                        .rounded(px(10.0))
                        .items_center()
                        .justify_center()
                        .bg(move || theme().muted)
                        .child(ui::quiet("Not connected")),
                ),
        )
}

fn card() -> View {
    view()
        .p(px(12.0))
        .gap(px(2.0))
        .rounded(px(16.0))
        .border_1()
        .border_color(move || theme().border)
        .bg(move || theme().card)
}
