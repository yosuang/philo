//! Philo's controls. `nsh/base` supplies the behavior; the look is the shell's.

use nsh_base::{button, input, theme};
use nsh_core::prelude::*;

use crate::local::{AppearanceChoice, Shell};

pub fn mark(letter: char, color: u32, size: f32) -> impl IntoElement {
    let font = (size * 0.42).clamp(11.0, 28.0);
    view()
        .size(px(size))
        .rounded(px(size / 2.0))
        .items_center()
        .justify_center()
        .flex_shrink_0()
        .bg(rgb(color))
        .child(
            text(letter.to_string())
                .text_size(px(font))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(white()),
        )
}

pub fn icon_button(
    shell: Shell,
    label: &'static str,
    icon_name: &'static str,
    filled: bool,
    enabled: impl Fn() -> bool + 'static,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    view()
        .tooltip(
            view()
                .px(px(8.0))
                .py(px(4.0))
                .rounded(px(8.0))
                .bg(move || theme().popover)
                .child(
                    text(label)
                        .text_size(px(12.0))
                        .text_color(move || theme().popover_foreground),
                ),
        )
        .child(
            button()
                .accessibility_label(label)
                .size(px(34.0))
                .rounded(px(17.0))
                .flex_row()
                .items_center()
                .justify_center()
                .cursor_pointer()
                .bg(move || {
                    if filled {
                        theme().primary
                    } else {
                        theme().card
                    }
                })
                .shadow_sm()
                .hover(|style| {
                    style.bg(move || {
                        if filled {
                            theme().primary.opacity(0.88)
                        } else {
                            theme().accent
                        }
                    })
                })
                .focus(|style| {
                    style
                        .outline(px(2.0))
                        .outline_offset(px(2.0))
                        .outline_color(move || theme().ring)
                })
                .disabled(move || !enabled())
                .disabled_style(|style| style.opacity(0.4))
                .on_press(on_press)
                .child(glyph(shell, icon_name, filled)),
        )
}

pub fn text_button(
    label: &'static str,
    filled: bool,
    enabled: impl Fn() -> bool + 'static,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    button()
        .accessibility_label(label)
        .px(px(16.0))
        .py(px(8.0))
        .rounded(px(18.0))
        .cursor_pointer()
        .bg(move || {
            if filled {
                theme().primary
            } else {
                theme().secondary
            }
        })
        .hover(|style| style.opacity(0.92))
        .focus(|style| {
            style
                .outline(px(2.0))
                .outline_offset(px(2.0))
                .outline_color(move || theme().ring)
        })
        .disabled(move || !enabled())
        .disabled_style(|style| style.opacity(0.45))
        .on_press(on_press)
        .child(
            text(label)
                .text_size(px(14.0))
                .font_weight(FontWeight::SEMIBOLD)
                .text_color(move || {
                    if filled {
                        theme().primary_foreground
                    } else {
                        theme().secondary_foreground
                    }
                }),
        )
}

pub fn field(placeholder: &'static str) -> Input {
    input()
        .placeholder(placeholder)
        .text_size(px(15.0))
        .text_color(move || theme().foreground)
        .px(px(12.0))
        .py(px(8.0))
        .rounded(px(12.0))
        .border_1()
        .border_color(move || theme().input)
        .bg(move || theme().card)
        .focus(|style| style.border_color(move || theme().ring))
}

pub fn section_label(label: &'static str) -> Text {
    text(label)
        .text_size(px(13.0))
        .text_color(move || theme().muted_foreground)
}

pub fn quiet(label: impl IntoReactive<SharedString>) -> Text {
    text(label)
        .text_size(px(12.0))
        .text_color(move || theme().muted_foreground)
}

pub fn glyph(shell: Shell, name: &'static str, on_primary: bool) -> impl IntoElement {
    image(move || icon_uri(shell, name, on_primary))
        .size(px(16.0))
        .object_fit(ResizeMode::Contain)
}

fn icon_uri(shell: Shell, name: &str, on_primary: bool) -> String {
    let color = if on_primary {
        "#ffffff"
    } else if shell.appearance.get() == AppearanceChoice::Dark {
        "#f4f4f5"
    } else {
        "#2a2a2a"
    };
    data_svg(&svg_body(name, color))
}

fn data_svg(body: &str) -> String {
    let mut uri = String::from("data:image/svg+xml,");
    for byte in body.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                uri.push(byte as char);
            }
            _ => uri.push_str(&format!("%{byte:02X}")),
        }
    }
    uri
}

fn svg_body(name: &str, color: &str) -> String {
    match name {
        "search" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round'><circle cx='11' cy='11' r='6.5'/><path d='M16 16.5L20 20.5'/></svg>"
        ),
        "plus" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round'><path d='M12 5.5v13M5.5 12h13'/></svg>"
        ),
        "close" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round'><path d='M7 7l10 10M17 7L7 17'/></svg>"
        ),
        "up" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2.4' stroke-linecap='round' stroke-linejoin='round'><path d='M12 18V7M7 11.5L12 6.5l5 5'/></svg>"
        ),
        "down" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2.2' stroke-linecap='round' stroke-linejoin='round'><path d='M6 9.5l6 6 6-6'/></svg>"
        ),
        "mic" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round'><rect x='9' y='3.5' width='6' height='10' rx='3'/><path d='M6.5 11a5.5 5.5 0 0 0 11 0M12 16.5V20'/></svg>"
        ),
        "grid" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='{color}'><rect x='4' y='4' width='6.2' height='6.2' rx='1.4'/><rect x='13.8' y='4' width='6.2' height='6.2' rx='1.4'/><rect x='4' y='13.8' width='6.2' height='6.2' rx='1.4'/><rect x='13.8' y='13.8' width='6.2' height='6.2' rx='1.4'/></svg>"
        ),
        "chevron-right" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'><path d='M9 6l6 6-6 6'/></svg>"
        ),
        "chevron-down" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'><path d='M6 9l6 6 6-6'/></svg>"
        ),
        _ => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24'><circle cx='12' cy='12' r='3' fill='{color}'/></svg>"
        ),
    }
}
