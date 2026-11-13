//! The center column: the bot's local transcript and the composer.
//! A message stays in the window. It is not sent, and no model replies.

use nsh_base::{button, input, theme};
use nsh_core::element::desktop::title_bar;
use nsh_core::prelude::*;

use crate::local::Shell;
use crate::ui;

pub fn column(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .min_w(px(0.0))
        .h_full()
        .relative()
        .overflow_hidden()
        .bg(move || theme().background)
        .child(header(shell))
        .child(notice(shell))
        .child(new_bot(shell))
        .child(transcript(shell))
        .child(composer(shell))
}

fn header(shell: Shell) -> impl IntoElement {
    // Clear row, painted above the thread. The transcript tucks up under it
    // so the name pill rests on a bubble's top edge, not on its glyphs.
    view()
        .h(move || {
            let top = title_bar().top.0;
            px(top.max(52.0))
        })
        .mb(px(-28.0))
        .flex_shrink_0()
        .flex_row()
        .items_center()
        .px(px(12.0))
        .pr(move || title_bar().right + px(12.0))
        .z_index(2)
        .pointer_events(PointerEvents::BoxNone)
        .child(view().flex_1())
        .child(bot_pill(shell))
        .child(view().flex_1())
        .child(share_button(shell))
}

fn bot_pill(shell: Shell) -> impl IntoElement {
    button()
        .flex_row()
        .items_center()
        .gap(px(6.0))
        .px(px(8.0))
        .py(px(2.0))
        .rounded(px(16.0))
        .bg(move || theme().card)
        .shadow_sm()
        .cursor_pointer()
        .max_w(px(280.0))
        .hover(|style| style.bg(move || theme().accent))
        .focus(|style| {
            style
                .outline(px(2.0))
                .outline_offset(px(2.0))
                .outline_color(move || theme().ring)
        })
        .accessibility_label("Details")
        .on_press(move || shell.toggle_details())
        .child(header_mark(shell))
        .child(
            text(move || {
                if shell.creating.get() {
                    "New bot".to_owned()
                } else {
                    shell
                        .selected_bot()
                        .map(|bot| bot.name)
                        .unwrap_or_else(|| "Philo".to_owned())
                }
            })
            .number_of_lines(1)
            .text_size(px(13.0))
            .font_weight(FontWeight::MEDIUM),
        )
}

fn header_mark(shell: Shell) -> impl IntoElement {
    ui::blob_live(
        move || shell.selected_bot().map(|bot| bot.shape).unwrap_or(0),
        18.0,
    )
}

fn share_button(shell: Shell) -> impl IntoElement {
    button()
        .accessibility_label("Share")
        .px(px(14.0))
        .py(px(5.0))
        .rounded(px(16.0))
        .bg(move || theme().card)
        .shadow_sm()
        .cursor_pointer()
        .hover(|style| style.bg(move || theme().accent))
        .focus(|style| {
            style
                .outline(px(2.0))
                .outline_offset(px(2.0))
                .outline_color(move || theme().ring)
        })
        .on_press(move || shell.notice("Share is not available."))
        .child(
            text("Share")
                .text_size(px(13.0))
                .font_weight(FontWeight::MEDIUM)
                .text_color(move || theme().foreground),
        )
}

fn notice(shell: Shell) -> impl IntoElement {
    when(
        move || shell.notice.get().is_some(),
        move || {
            view()
                .mx(px(16.0))
                .mb(px(4.0))
                .px(px(12.0))
                .py(px(8.0))
                .rounded(px(12.0))
                .bg(move || theme().muted)
                .flex_row()
                .items_center()
                .gap(px(8.0))
                .child(
                    text(move || shell.notice.get().unwrap_or_default())
                        .flex_1()
                        .text_size(px(13.0))
                        .text_color(move || theme().foreground),
                )
                .child(ui::icon_button(
                    shell,
                    "Dismiss",
                    "close",
                    ui::Circle::White,
                    || true,
                    move || shell.dismiss_notice(),
                ))
        },
    )
}

fn new_bot(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .min_h(px(0.0))
        .p(px(24.0))
        .gap(px(12.0))
        .style_when(move || !shell.creating.get(), |style| style.hidden())
        .child(
            text("New bot")
                .text_size(px(22.0))
                .font_weight(FontWeight::SEMIBOLD),
        )
        .child(ui::quiet(
            "The name stays in this window. Philo does not save it to a server or a model.",
        ))
        .child(ui::section_label("Name"))
        .child(
            ui::field("Name")
                .element_ref(&shell.name_field)
                .test_id("bot-name")
                .value(move || shell.new_name.get())
                .on_change(move |text: &str| shell.new_name.set(text.to_owned()))
                .on_submit(move |_: &str| shell.create_bot()),
        )
        .child(
            view()
                .flex_row()
                .gap(px(8.0))
                .child(ui::text_button(
                    "Create bot",
                    true,
                    || true,
                    move || shell.create_bot(),
                ))
                .child(ui::text_button(
                    "Cancel",
                    false,
                    || true,
                    move || shell.cancel_new(),
                )),
        )
}

fn transcript(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .min_h(px(0.0))
        .overflow_hidden()
        .relative()
        .style_when(move || shell.creating.get(), |style| style.hidden())
        .child(message_list(shell))
        .child(jump(shell))
}

fn message_list(shell: Shell) -> impl IntoElement {
    virtual_list(move || shell.line_count(), move |at| message_row(shell, at))
        .keys(move || shell.line_ids())
        .inverted()
        .estimated_item_size(px(84.0))
        .content_padding(px(12.0))
        .row_gap(px(3.0))
        .flex_1()
        .element_ref(&shell.transcript)
        .empty(empty_transcript(shell))
        .on_scroll(move |event: &ScrollEvent| {
            shell.note_scroll(
                event.offset.y.0,
                event.content_size.height.0,
                event.size.height.0,
            );
        })
}

fn empty_transcript(shell: Shell) -> impl IntoElement {
    view()
        .flex_1()
        .items_center()
        .justify_center()
        .p(px(24.0))
        .gap(px(6.0))
        .child(
            text(move || {
                if shell.selected_bot().is_some_and(|bot| bot.demo) {
                    "No messages yet".to_owned()
                } else {
                    "No messages yet".to_owned()
                }
            })
            .text_size(px(16.0))
            .font_weight(FontWeight::SEMIBOLD),
        )
        .child(ui::quiet(
            "What you type stays in this window. Nothing is sent, and there is no model reply.",
        ))
}

fn time_mark(shell: Shell) -> impl IntoElement {
    ui::blob_live(
        move || shell.selected_bot().map(|bot| bot.shape).unwrap_or(0),
        14.0,
    )
}

fn message_row(shell: Shell, at: Position) -> impl IntoElement {
    let mine = move || shell.line_at(at.index()).is_some_and(|line| line.mine);
    let cap = under_pill(shell, at);
    // Right inset keeps bubble glyphs clear of the Share pill.
    // The tucked row leads with its bubble, so the pill rests on that cap.
    view()
        .w_full()
        .pr(px(120.0))
        .gap(px(4.0))
        .child(when(move || cap && mine(), move || user_bubble(shell, at)))
        .child(time_line(shell, at))
        .child(when(
            move || !(cap && mine()),
            move || {
                when(mine, move || user_bubble(shell, at)).otherwise(move || note_bubble(shell, at))
            },
        ))
}

fn time_line(shell: Shell, at: Position) -> impl IntoElement {
    view()
        .w_full()
        .flex_row()
        .justify_center()
        .items_center()
        .gap(px(6.0))
        .py(px(2.0))
        .style_when(
            move || !shell.line_at(at.index()).is_some_and(|line| line.mine),
            |style| style.hidden(),
        )
        .child(time_mark(shell))
        .child(
            text("Local · not sent")
                .text_size(px(11.0))
                .text_color(move || theme().muted_foreground),
        )
}

/// The row tucked under the name pill. Its bubble keeps the tall cap.
fn under_pill(shell: Shell, at: Position) -> bool {
    let count = shell.line_count();
    count > 1 && at.index() + 2 == count
}

fn user_bubble(shell: Shell, at: Position) -> impl IntoElement {
    let top = if under_pill(shell, at) { 52.0 } else { 8.0 };
    view().w_full().flex_row().justify_end().child(
        view()
            .max_w(relative(0.92))
            .gap(px(6.0))
            .px(px(12.0))
            .pt(px(top))
            .pb(px(8.0))
            .rounded(px(18.0))
            .bg(rgb(0xca317e))
            .child(previews(shell, at))
            .child(
                text(move || {
                    shell
                        .line_at(at.index())
                        .map(|line| line.body)
                        .unwrap_or_default()
                })
                .text_size(px(14.0))
                .text_color(move || theme().primary_foreground),
            ),
    )
}

fn note_bubble(shell: Shell, at: Position) -> impl IntoElement {
    let top = if under_pill(shell, at) { 34.0 } else { 8.0 };
    view().w_full().flex_row().justify_start().child(
        view()
            .max_w(relative(0.92))
            .px(px(12.0))
            .pt(px(top))
            .pb(px(8.0))
            .rounded(px(18.0))
            .bg(move || theme().muted)
            .child(
                text(move || {
                    shell
                        .line_at(at.index())
                        .map(|line| line.body)
                        .unwrap_or_default()
                })
                .text_size(px(14.0))
                .text_color(move || theme().foreground),
            ),
    )
}

fn previews(shell: Shell, at: Position) -> impl IntoElement {
    when(
        move || {
            shell
                .line_at(at.index())
                .is_some_and(|line| !line.previews.is_empty())
        },
        move || {
            view().flex_row().gap(px(6.0)).child(each(
                move || {
                    shell
                        .line_at(at.index())
                        .map(|line| line.previews)
                        .unwrap_or_default()
                },
                |label| label.clone(),
                move |label, _| {
                    view()
                        .px(px(8.0))
                        .py(px(6.0))
                        .rounded(px(8.0))
                        .bg(move || theme().card)
                        .child(
                            text(move || label.get())
                                .text_size(px(12.0))
                                .text_color(move || theme().foreground),
                        )
                },
            ))
        },
    )
}

fn jump(shell: Shell) -> impl IntoElement {
    when(
        move || shell.away_from_bottom.get() && !shell.creating.get() && shell.line_count() > 0,
        move || {
            view()
                .absolute()
                .left(px(0.0))
                .right(px(0.0))
                .bottom(px(12.0))
                .items_center()
                .pointer_events(PointerEvents::BoxNone)
                .child(ui::icon_button(
                    shell,
                    "Jump to bottom",
                    "down",
                    ui::Circle::White,
                    || true,
                    move || shell.jump_bottom(),
                ))
        },
    )
}

fn composer(shell: Shell) -> impl IntoElement {
    let can_send =
        move || !shell.draft.get().trim().is_empty() || !shell.previews.with(Vec::is_empty);
    view()
        .px(px(12.0))
        .pb(px(12.0))
        .pt(px(2.0))
        .style_when(move || shell.creating.get(), |style| style.hidden())
        .child(
            view()
                .gap(px(8.0))
                .px(px(12.0))
                .pt(px(10.0))
                .pb(px(8.0))
                .rounded(px(24.0))
                .bg(move || theme().card)
                .shadow_lg()
                .focus_within(|style| {
                    style
                        .outline(px(2.0))
                        .outline_offset(px(2.0))
                        .outline_color(move || theme().ring)
                })
                .child(attachment_row(shell))
                .child(
                    input()
                        .element_ref(&shell.composer)
                        .placeholder("Message")
                        .value(move || shell.draft.get())
                        .on_change(move |text: &str| shell.draft.set(text.to_owned()))
                        .on_submit(move |_: &str| shell.send())
                        .multiline(true)
                        .submit_behavior(SubmitBehavior::Submit)
                        .return_key_type(ReturnKeyType::Send)
                        .flex_1()
                        .max_h(px(120.0))
                        .border(px(0.0))
                        .bg(move || theme().card)
                        .text_size(px(15.0))
                        .text_color(move || theme().foreground)
                        .test_id("composer"),
                )
                .child(
                    view()
                        .flex_row()
                        .items_center()
                        .child(ui::icon_button(
                            shell,
                            "Attach a local preview",
                            "plus",
                            ui::Circle::Bare,
                            || true,
                            move || shell.add_preview(),
                        ))
                        .child(view().flex_1())
                        .child(ui::icon_button(
                            shell,
                            "Voice is not available",
                            "mic",
                            ui::Circle::Gray,
                            || true,
                            move || {
                                shell.notice("Voice is not available. The microphone stays off.");
                            },
                        ))
                        .child(view().w(px(8.0)))
                        .child(ui::icon_button(
                            shell,
                            "Show in this window",
                            "up",
                            ui::Circle::Send,
                            can_send,
                            move || shell.send(),
                        )),
                ),
        )
}

fn attachment_row(shell: Shell) -> impl IntoElement {
    when(
        move || !shell.previews.with(Vec::is_empty),
        move || {
            view().flex_row().gap(px(8.0)).child(each(
                move || shell.previews.get(),
                |preview| preview.id.clone(),
                move |preview, _| {
                    let id = preview.with(|item| item.id.clone());
                    view()
                        .relative()
                        .size(px(64.0))
                        .rounded(px(10.0))
                        .overflow_hidden()
                        .bg(move || theme().muted)
                        .border_1()
                        .border_color(move || theme().border)
                        .items_center()
                        .justify_center()
                        .child(
                            text(move || preview.with(|item| item.label.clone()))
                                .text_size(px(10.0))
                                .text_center()
                                .number_of_lines(2)
                                .text_color(move || theme().muted_foreground),
                        )
                        .child(
                            button()
                                .absolute()
                                .top(px(2.0))
                                .right(px(2.0))
                                .size(px(18.0))
                                .rounded(px(9.0))
                                .items_center()
                                .justify_center()
                                .bg(move || theme().card)
                                .shadow_sm()
                                .cursor_pointer()
                                .accessibility_label("Remove preview")
                                .focus(|style| {
                                    style.outline(px(2.0)).outline_color(move || theme().ring)
                                })
                                .on_press(move || shell.remove_preview(id.clone()))
                                .child(ui::glyph(shell, "close", false)),
                        )
                },
            ))
        },
    )
}
