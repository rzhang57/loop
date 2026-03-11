import re
import sys
from pathlib import Path

from openai import APIStatusError
from textual.app import App, ComposeResult
from textual.containers import VerticalScroll
from textual.events import Key
from textual.widgets import Static, TextArea

from core.chat import chat

DEBUG_LOG = Path("shift_enter_debug.log")


class Composer(TextArea):
    def _debug(self, message: str) -> None:
        with DEBUG_LOG.open("a", encoding="utf-8") as handle:
            handle.write(message + "\n")

    async def _on_key(self, event: Key) -> None:
        if "enter" in event.key or "return" in event.key or "newline" in event.key:
            self._debug(f"composer event: key={event.key!r} aliases={event.aliases!r}")

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
        # On Windows, shift+enter arrives as plain enter because the Win32 input
        # path strips modifier state. Check shift directly while the key is still held.
        if event.key == "enter" and sys.platform == "win32":
            import ctypes
            VK_SHIFT = 0x10
            if ctypes.windll.user32.GetAsyncKeyState(VK_SHIFT) & 0x8000:
                return True
        return False


class ChatApp(App):
    SPINNER_FRAMES = ["·  ", "·· ", "···", " ··", "  ·"]

    CSS = """
    VerticalScroll,
    Static {
        background: transparent;
    }

    #messages {
        scrollbar-background: transparent;
        scrollbar-background-hover: transparent;
        scrollbar-background-active: transparent;
        scrollbar-color: white;
        scrollbar-color-hover: white;
        scrollbar-color-active: white;
        scrollbar-corner-color: transparent;
    }

    TextArea {
        height: 3;
        background: transparent;
        color: white;
        border: solid gray;
    }

    TextArea:focus {
        border: solid white;
    }
    """

    def __init__(self) -> None:
        if sys.platform == "win32":
            from ui.tui.windows_driver import LoopWindowsDriver

            super().__init__(driver_class=LoopWindowsDriver)
        else:
            super().__init__()
        self.messages: list[dict[str, str]] = []
        self.pending_assistant = ""
        self.waiting_for_first_chunk = False
        self.spinner_index = 0
        self.spinner_timer = None

    def compose(self) -> ComposeResult:
        with VerticalScroll(id="messages"):
            yield Static("", id="transcript")
        yield Composer(placeholder="> ", id="composer")

    def on_text_area_changed(self, _: TextArea.Changed) -> None:
        self._resize_composer()

    def on_mount(self) -> None:
        self.spinner_timer = self.set_interval(0.3, self._advance_spinner, pause=True)

    async def _submit(self) -> None:
        composer = self.query_one("#composer", TextArea)
        if composer.disabled:
            return

        prompt = composer.text.strip()
        if not prompt:
            return

        composer.clear()
        composer.disabled = True
        self._resize_composer()

        self.messages.append({"role": "user", "content": prompt})
        self.pending_assistant = ""
        self.waiting_for_first_chunk = True
        self.spinner_index = 0
        if self.spinner_timer is not None:
            self.spinner_timer.resume()
        self._refresh_transcript()

        assistant_response = ""

        try:
            async for chunk in chat(self.messages):
                if self.waiting_for_first_chunk:
                    self.waiting_for_first_chunk = False
                    if self.spinner_timer is not None:
                        self.spinner_timer.pause()
                assistant_response += chunk
                self.pending_assistant = assistant_response
                self._refresh_transcript()
            self.messages.append({"role": "assistant", "content": assistant_response})
            self.pending_assistant = ""
        except APIStatusError as error:
            self.waiting_for_first_chunk = False
            self.pending_assistant = f"[error {error.status_code}] {error.message}"
            self._refresh_transcript()
        except Exception as error:
            self.waiting_for_first_chunk = False
            self.pending_assistant = f"[error] {error}"
            self._refresh_transcript()
        finally:
            self.waiting_for_first_chunk = False
            if self.spinner_timer is not None:
                self.spinner_timer.pause()
            self._refresh_transcript()
            composer.disabled = False
            composer.focus()
            self._resize_composer()

    def _render_transcript(self) -> str:
        blocks: list[str] = []

        for message in self.messages:
            role = message["role"]
            content = message["content"]
            if role == "user":
                blocks.append(self._format_user_block(content))
            elif role == "assistant":
                blocks.append(content)

        if self.waiting_for_first_chunk:
            blocks.append(f"{self.SPINNER_FRAMES[self.spinner_index]} Thinking")

        if self.pending_assistant:
            blocks.append(self.pending_assistant)

        return "\n\n".join(blocks)

    def _format_code_block(self, content: str) -> str:
        lines = content.strip("\n").splitlines()
        if not lines:
            return ""
        return "\n".join(f"    {line}" for line in lines)

    def _format_user_block(self, content: str) -> str:
        lines = content.splitlines() or [content]
        return "\n".join(f"> {line}" if line else ">" for line in lines)

    def _resize_composer(self) -> None:
        composer = self.query_one("#composer", TextArea)
        line_count = max(1, composer.text.count("\n") + 1)
        composer.styles.height = min(6, line_count + 2)

    def _advance_spinner(self) -> None:
        if not self.waiting_for_first_chunk:
            return
        self.spinner_index = (self.spinner_index + 1) % len(self.SPINNER_FRAMES)
        self._refresh_transcript()

    def _refresh_transcript(self) -> None:
        self.query_one("#transcript", Static).update(self._render_transcript())
        self.query_one("#messages", VerticalScroll).scroll_end(animate=False)
