from openai import APIStatusError
from textual.app import App, ComposeResult
from textual.containers import VerticalScroll
from textual.widgets import Input, Static

from core.chat import chat


class ChatApp(App):
    def __init__(self) -> None:
        super().__init__()
        self.messages: list[dict[str, str]] = []
        self.transcript = ""

    def compose(self) -> ComposeResult:
        with VerticalScroll(id="messages"):
            yield Static(self.transcript, id="transcript")
        yield Input(placeholder="> ", id="composer")

    async def on_input_submitted(self, event: Input.Submitted) -> None:
        prompt = event.value.strip()
        if not prompt:
            return

        composer = self.query_one("#composer", Input)
        composer.value = ""
        composer.disabled = True

        self.messages.append({"role": "user", "content": prompt})
        self.transcript += f"\nYou: {prompt}\nAssistant: "
        self._refresh_transcript()

        assistant_response = ""

        try:
            async for chunk in chat(self.messages):
                assistant_response += chunk
                self.transcript += chunk
                self._refresh_transcript()
            self.messages.append({"role": "assistant", "content": assistant_response})
        except APIStatusError as error:
            self.transcript += f"\n[error {error.status_code}] {error.message}"
            self._refresh_transcript()
        except Exception as error:
            self.transcript += f"\n[error] {error}"
            self._refresh_transcript()
        finally:
            self.transcript += "\n"
            self._refresh_transcript()
            composer.disabled = False
            composer.focus()

    def _refresh_transcript(self) -> None:
        self.query_one("#transcript", Static).update(self.transcript)
        self.query_one("#messages", VerticalScroll).scroll_end(animate=False)
