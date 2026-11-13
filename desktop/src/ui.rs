//! Philo's controls. `nsh/base` supplies the behavior; the look is the shell's.

use nsh_base::{button, input, theme};
use nsh_core::prelude::*;

use crate::local::{AppearanceChoice, Shell};

/// How a round control is filled. Send is lighter than a user bubble.
#[derive(Clone, Copy, PartialEq, Eq)]
pub enum Circle {
    White,
    Gray,
    Send,
    Bare,
}

pub fn blob(shape: u8, size: f32) -> impl IntoElement {
    image(blob_uri(shape))
        .size(px(size))
        .object_fit(ResizeMode::Contain)
        .flex_shrink_0()
}

pub fn blob_live(shape: impl Fn() -> u8 + 'static, size: f32) -> impl IntoElement {
    image(move || blob_uri(shape()))
        .size(px(size))
        .object_fit(ResizeMode::Contain)
        .flex_shrink_0()
}

/// A small generic portrait for the demo profile. Not a letter, and not a person from the references.
pub fn portrait(size: f32) -> impl IntoElement {
    image(portrait_uri())
        .size(px(size))
        .rounded(px(size / 2.0))
        .object_fit(ResizeMode::Cover)
        .flex_shrink_0()
}

pub fn icon_button(
    shell: Shell,
    label: &'static str,
    icon_name: &'static str,
    circle: Circle,
    enabled: impl Fn() -> bool + 'static,
    on_press: impl Fn() + 'static,
) -> impl IntoElement {
    let bare = circle == Circle::Bare;
    let send = circle == Circle::Send;
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
                .size(if bare { px(28.0) } else { px(34.0) })
                .rounded(px(17.0))
                .flex_row()
                .items_center()
                .justify_center()
                .cursor_pointer()
                .bg(move || match circle {
                    Circle::White => theme().card,
                    Circle::Send => rgb(0xe5358d).into(),
                    Circle::Gray => rgb(0xe6e6e8).into(),
                    Circle::Bare => rgba(0x00000000).into(),
                })
                .shadow_sm()
                .style_when(move || bare, |style| style.shadow_none())
                .hover(|style| {
                    style.bg(move || match circle {
                        Circle::White => theme().accent,
                        Circle::Send => rgb(0xd42e80).into(),
                        Circle::Gray => rgb(0xdadadc).into(),
                        Circle::Bare => rgba(0x00000000).into(),
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
                .child(glyph(shell, icon_name, send)),
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

pub fn tinted(name: &'static str, color: &'static str) -> impl IntoElement {
    tinted_size(name, color, 16.0)
}

pub fn tinted_size(name: &'static str, color: &'static str, size: f32) -> impl IntoElement {
    image(data_svg(&svg_body(name, color)))
        .size(px(size))
        .object_fit(ResizeMode::Contain)
        .flex_shrink_0()
}

/// The small send mark between "Messaged" and the bot name. It does not send.
pub fn plane_badge() -> impl IntoElement {
    image(data_svg(
        "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'><circle cx='12' cy='12' r='11' fill='#ececef'/><path fill='#3f3f46' d='M6.2 12.2l11.2-4.6-4.2 11.4-2.1-4.4-4.9-2.4z'/></svg>",
    ))
    .size(px(16.0))
    .object_fit(ResizeMode::Contain)
    .flex_shrink_0()
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
        "sparkle" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='{color}'><path d='M12 2l1.6 6.2L20 10l-6.4 1.8L12 18l-1.6-6.2L4 10l6.4-1.8L12 2z'/></svg>"
        ),
        "check" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2.4' stroke-linecap='round' stroke-linejoin='round'><circle cx='12' cy='12' r='9'/><path d='M8 12.2l2.4 2.4L16 9.2'/></svg>"
        ),
        "clock" => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none' stroke='{color}' stroke-width='2' stroke-linecap='round'><circle cx='12' cy='12' r='8'/><path d='M12 8v4.5l3 2'/></svg>"
        ),
        _ => format!(
            "<svg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24'><circle cx='12' cy='12' r='3' fill='{color}'/></svg>"
        ),
    }
}

fn blob_uri(shape: u8) -> String {
    let body = match shape % 8 {
        0 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#f93598' d='M18 30c0-8 6-14 14-14 6 0 10 3 12 7 6 1 10 6 10 12 0 7-6 12-13 12H20c-7 0-12-5-12-12 0-5 3-9 8-11 0-1 1-3 2-4z'/><circle cx='24' cy='32' r='4' fill='#fff'/><circle cx='36' cy='30' r='4' fill='#fff'/></svg>"
        }
        1 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#2f9d78' d='M14 36c0-12 8-22 18-22 8 0 14 4 18 10 6 2 10 8 10 14 0 10-8 16-18 16H28C18 54 14 46 14 36z'/></svg>"
        }
        2 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><circle cx='32' cy='32' r='22' fill='#e23b4a'/><circle cx='24' cy='28' r='5' fill='#fff'/><circle cx='38' cy='30' r='5' fill='#fff'/></svg>"
        }
        3 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><circle cx='30' cy='32' r='20' fill='#1a1a1a'/><path fill='#f5a524' d='M48 40l2.2 4.6 5 .6-3.7 3.4.9 5-4.4-2.4-4.4 2.4.9-5-3.7-3.4 5-.6z'/></svg>"
        }
        4 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#3b82c4' d='M32 8c10 12 18 20 18 30a18 18 0 1 1-36 0C14 28 22 20 32 8z'/></svg>"
        }
        5 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#e0a23a' d='M16 28c0-10 8-16 16-16 10 0 18 8 18 18 0 12-8 22-18 22-8 0-16-6-16-16 0-3 0-6 0-8z'/></svg>"
        }
        6 => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#7c6ad6' d='M32 8l8 14 16 2-12 11 3 16-15-8-15 8 3-16L8 24l16-2z'/></svg>"
        }
        _ => {
            "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><path fill='#4aa3a8' d='M32 6c2 10 8 16 16 18-10 2-16 8-18 18-2-10-8-16-18-18 10-2 16-8 20-18z'/></svg>"
        }
    };
    data_svg(body)
}

fn portrait_uri() -> String {
    data_svg(
        "<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'><circle cx='32' cy='32' r='32' fill='#efe6dc'/><ellipse cx='32' cy='26' rx='12' ry='13' fill='#e2b89a'/><path fill='#3d342c' d='M20 22c2-8 8-12 12-12s10 4 12 12c-4-3-8-4-12-4s-8 1-12 4z'/><path fill='#c9846a' d='M12 58c3-14 11-20 20-20s17 6 20 20'/></svg>",
    )
}
