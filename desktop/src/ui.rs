//! Styled controls. `nsh/base` supplies behavior; these supply Philo's look
//! from the theme tokens.

use nsh_base::{button, input, theme};
use nsh_core::prelude::*;

pub fn page_title(label: impl Into<SharedString>) -> Text {
    let label = label.into();
    text(label)
        .text_size(px(22.0))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(move || theme().foreground)
}

pub fn section_label(label: impl Into<SharedString>) -> Text {
    let label = label.into();
    text(label)
        .text_size(px(12.0))
        .font_weight(FontWeight::SEMIBOLD)
        .text_color(move || theme().muted_foreground)
}

pub fn muted(label: impl Into<SharedString>) -> Text {
    let label = label.into();
    text(label)
        .text_size(px(13.0))
        .text_color(move || theme().muted_foreground)
}

pub fn body(label: impl Into<SharedString>) -> Text {
    let label = label.into();
    text(label)
        .text_size(px(15.0))
        .text_color(move || theme().foreground)
}

pub fn field(placeholder: &'static str) -> Input {
    input()
        .placeholder(placeholder)
        .text_size(px(15.0))
        .text_color(move || theme().foreground)
        .px_3()
        .py_2()
        .rounded(move || theme().radius)
        .border_1()
        .border_color(move || theme().input)
        .bg(move || theme().card)
}

pub fn primary_button(
    label: &'static str,
    enabled: impl Fn() -> bool + 'static,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    button()
        .flex_row()
        .items_center()
        .justify_center()
        .px_4()
        .py_2()
        .rounded(move || theme().radius)
        .bg(move || theme().primary)
        .hover(|style| style.opacity(0.92))
        .active(|style| style.opacity(0.8))
        .disabled(move || !enabled())
        .disabled_style(|style| style.opacity(0.45))
        .on_press(on_press)
        .child(
            text(label)
                .text_size(px(14.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || theme().primary_foreground),
        )
}

pub fn secondary_button(
    label: &'static str,
    enabled: impl Fn() -> bool + 'static,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    button()
        .flex_row()
        .items_center()
        .justify_center()
        .px_4()
        .py_2()
        .rounded(move || theme().radius)
        .bg(move || theme().secondary)
        .border_1()
        .border_color(move || theme().border)
        .hover(|style| style.bg(move || theme().accent))
        .active(|style| style.opacity(0.8))
        .disabled(move || !enabled())
        .disabled_style(|style| style.opacity(0.45))
        .on_press(on_press)
        .child(
            text(label)
                .text_size(px(14.0))
                .text_color(move || theme().secondary_foreground),
        )
}

pub fn card() -> View {
    view()
        .p_3()
        .gap_1()
        .rounded(move || theme().radius)
        .border_1()
        .border_color(move || theme().border)
        .bg(move || theme().card)
}
