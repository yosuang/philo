//! Philo desktop. The window is a local three-column shell.
//! The server client stays in the crate and is not started.

#[allow(dead_code)]
mod client;
mod local;
#[allow(dead_code)]
mod model;
mod pages;
mod shell;
#[allow(dead_code)]
mod store;
mod ui;

use nsh_core::desktop::{MenuRole, TitleBarStyle, WindowFrame};
use nsh_core::element::desktop::{
    MenuItem, set_application_menu, set_frame, set_title, set_title_bar,
};
use nsh_core::prelude::*;

use crate::local::{AppearanceChoice, Shell};

/// The crate's UI set (`nsh.json` `"rust"`).
#[native_module("philo/native")]
fn native(registry: &mut Registry) {
    registry.register::<App>();
}

/// Philo's root window.
#[component]
pub fn App() -> impl IntoElement {
    let _ = env_logger::try_init();
    // `nsh run` reads `nsh.json`'s theme. The binary applies the same file
    // itself, so a direct launch is not stuck on the default neutral theme.
    if let Ok(theme) = nsh_base::Theme::from_json(include_str!("../theme.json")) {
        set_tokens(theme.into_tokens());
    }
    let shell = Shell::boot();
    effect(|| {
        set_title("Philo");
        // One window, the size `main` already asked for. Outer frame on
        // macOS and Windows, GTK's default size on Linux. Not a new size.
        set_frame(WindowFrame {
            width: Some(px(1024.0)),
            height: Some(px(748.0)),
            ..WindowFrame::default()
        });
        set_title_bar(TitleBarStyle::Hidden, Some(px(52.0)));
    });
    install_menu(shell);
    on_key("escape", move |_| shell.dismiss_top());
    on_key("mod+f", move |_| shell.open_search());
    view()
        .flex_1()
        .bg(move || nsh_base::theme().background)
        .text_color(move || nsh_base::theme().foreground)
        .child(shell::shell(shell))
}

fn install_menu(shell: Shell) {
    effect(move || {
        set_application_menu(vec![
            MenuItem::submenu(
                "File",
                [
                    MenuItem::new("New bot")
                        .shortcut("mod+n")
                        .on_select(move || shell.open_new()),
                    MenuItem::separator(),
                    MenuItem::role(MenuRole::Quit).label("Quit"),
                ],
            ),
            MenuItem::submenu(
                "Edit",
                [
                    MenuItem::role(MenuRole::Undo),
                    MenuItem::role(MenuRole::Redo),
                    MenuItem::separator(),
                    MenuItem::role(MenuRole::Cut),
                    MenuItem::role(MenuRole::Copy),
                    MenuItem::role(MenuRole::Paste),
                    MenuItem::role(MenuRole::SelectAll),
                ],
            ),
            MenuItem::submenu(
                "View",
                [
                    MenuItem::new("Light appearance")
                        .on_select(move || shell.set_appearance(AppearanceChoice::Light)),
                    MenuItem::new("Dark appearance")
                        .on_select(move || shell.set_appearance(AppearanceChoice::Dark)),
                    MenuItem::new("System appearance")
                        .on_select(move || shell.set_appearance(AppearanceChoice::System)),
                    MenuItem::separator(),
                    MenuItem::new("Sidebar")
                        .shortcut("mod+shift+b")
                        .on_select(move || shell.toggle_sidebar()),
                    MenuItem::new("Details")
                        .shortcut("mod+shift+d")
                        .on_select(move || shell.toggle_details()),
                ],
            ),
        ]);
    });
}
