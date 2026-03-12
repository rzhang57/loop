import sys

from rich.style import Style
from textual.events import Key
from textual.widgets import TextArea
from textual.widgets.text_area import TextAreaTheme

_THEME = TextAreaTheme(name="loop", base_style=Style(color="white", bgcolor="default"))


class Composer(TextArea):
    def on_mount(self) -> None:
        self.register_theme(_THEME)
        self.theme = "loop"

    async def _on_key(self, event: Key) -> None:
        if event.key == "ctrl+c":
            event.stop()
            event.prevent_default()
            self.app.exit()
            return

        if self._is_newline_key(event):
            event.stop()
            event.prevent_default()
            self.insert("\n")
            self.app._resize_composer()
            return

        if event.key == "enter":
            event.stop()
            event.prevent_default()
            await self.app._submit()
            return

        await super()._on_key(event)

    def _is_newline_key(self, event: Key) -> bool:
        newline_keys = {"shift+enter", "shift+return", "ctrl+j", "newline"}
        if event.key in newline_keys or any(alias in newline_keys for alias in event.aliases):
            return True

        if event.key == "enter" and sys.platform == "win32":
            import ctypes

            vk_shift = 0x10
            if ctypes.windll.user32.GetAsyncKeyState(vk_shift) & 0x8000:
                return True

        return False
