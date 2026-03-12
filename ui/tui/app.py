import sys

from openai import APIStatusError
from textual.app import App, ComposeResult
from textual.containers import VerticalScroll
from textual.widgets import Static, TextArea

from core_service.chat import chat
from ui.tui.composer import Composer
from ui.tui.transcript import render_transcript


class ChatApp(App):
    SPINNER_FRAMES = ["·  ", "·· ", "···", " ··", "  ·"]

    CSS = """
    #messages {
        align-vertical: bottom;
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
        return render_transcript(
            messages=self.messages,
            pending_assistant=self.pending_assistant,
            waiting_for_first_chunk=self.waiting_for_first_chunk,
            spinner_frame=self.SPINNER_FRAMES[self.spinner_index],
        )

    def _resize_composer(self) -> None:
        composer = self.query_one("#composer", TextArea)
        line_count = max(1, composer.text.count("\n") + 1)
        composer.styles.height = min(6, line_count + 2)

    def _advance_spinner(self) -> None:
        if not self.waiting_for_first_chunk:
            return
        self.spinner_index = (self.spinner_index + 1) % len(self.SPINNER_FRAMES)
        self._refresh_transcript()

    def on_resize(self) -> None:
        self._refresh_transcript()
        self.query_one("#messages", VerticalScroll).scroll_end(animate=False)

    def _refresh_transcript(self) -> None:
        self.query_one("#transcript", Static).update(self._render_transcript())
        self.query_one("#messages", VerticalScroll).scroll_end(animate=False)
