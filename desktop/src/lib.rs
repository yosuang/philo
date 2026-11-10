//! Philo desktop client. The window is Native Shell. A bot's chat is restored
//! from the Go server; the user does not pick a session.

mod client;
mod model;
mod pages;
mod shell;
mod store;
mod ui;

use nsh_base::theme;
use nsh_core::desktop::{MenuRole, WindowFrame};
use nsh_core::element::desktop::{MenuItem, set_application_menu, set_frame, set_title};
use nsh_core::prelude::*;

use crate::store::{AppearanceChoice, Route, Store};

/// The crate's UI set (`nsh.json` `"rust"`).
#[native_module("philo/native")]
fn native(registry: &mut Registry) {
    registry.register::<App>();
}

/// Philo's root window.
#[component]
pub fn App() -> impl IntoElement {
    let store = Store::boot();
    set_title("Philo");
    set_frame(WindowFrame {
        width: Some(px(1024.0)),
        height: Some(px(748.0)),
        ..WindowFrame::default()
    });
    install_menu(store);
    view()
        .flex_1()
        .bg(move || theme().background)
        .text_color(move || theme().foreground)
        .child(shell::shell(store))
}

fn install_menu(store: Store) {
    effect(move || {
        set_application_menu(vec![
            MenuItem::submenu(
                "File",
                [
                    MenuItem::new("New bot")
                        .shortcut("mod+n")
                        .on_select(move || store.open_new_agent()),
                    MenuItem::separator(),
                    MenuItem::new("Settings").on_select(move || store.show(Route::Settings)),
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
                        .on_select(move || store.set_appearance(AppearanceChoice::Light)),
                    MenuItem::new("Dark appearance")
                        .on_select(move || store.set_appearance(AppearanceChoice::Dark)),
                    MenuItem::new("System appearance")
                        .on_select(move || store.set_appearance(AppearanceChoice::System)),
                ],
            ),
        ]);
    });
}
