from __future__ import annotations

import asyncio
import os
import sys
from codecs import getincrementaldecoder
from threading import Event, Thread

from textual import constants
from textual._parser import ParseError
from textual._xterm_parser import XTermParser
from textual.drivers._input_reader import InputReader
from textual.drivers._writer_thread import WriterThread
from textual.drivers import win32
from textual.drivers.windows_driver import WindowsDriver
from textual.events import Resize
from textual.geometry import Size


class LoopWindowsDriver(WindowsDriver):
    def __init__(
        self,
        app,
        *,
        debug: bool = False,
        mouse: bool = True,
        size: tuple[int, int] | None = None,
    ) -> None:
        super().__init__(app, debug=debug, mouse=mouse, size=size)
        self._input_reader: InputReader | None = None
        self._input_thread: Thread | None = None
        self._resize_stop = Event()
        self._resize_thread: Thread | None = None

    def start_application_mode(self) -> None:
        self._restore_console = win32.enable_application_mode()

        self._writer_thread = WriterThread(sys.__stdout__)
        self._writer_thread.start()

        self.write("\x1b[?1049h")
        self._enable_mouse_support()
        self.write("\x1b[?25l")
        self.write("\x1b[?1004h")
        self.write("\x1b[>1u")
        self.flush()
        self._enable_bracketed_paste()

        self._input_reader = InputReader()
        self._input_thread = Thread(target=self._run_input_thread, name="loop-input")
        self._input_thread.start()

        self._resize_stop.clear()
        self._resize_thread = Thread(target=self._run_resize_thread, name="loop-resize", daemon=True)
        self._resize_thread.start()

    def _run_resize_thread(self) -> None:
        try:
            ts = os.get_terminal_size()
            current = (ts.columns, ts.lines)
        except OSError:
            return
        while not self._resize_stop.wait(0.1):
            try:
                ts = os.get_terminal_size()
                new = (ts.columns, ts.lines)
            except OSError:
                break
            if new != current:
                current = new
                size = Size(*new)
                self._app.post_message(Resize(size, size))

    def _run_input_thread(self) -> None:
        parser = XTermParser(debug=constants.DEBUG)
        decode = getincrementaldecoder("utf-8")().decode

        try:
            assert self._input_reader is not None
            for data in self._input_reader:
                unicode_data = decode(data)
                if not unicode_data:
                    continue
                for event in parser.feed(unicode_data):
                    self.process_message(event)
                for event in parser.tick():
                    self.process_message(event)
        except BaseException:
            import rich.traceback

            self._app.call_from_thread(
                self._app.panic,
                rich.traceback.Traceback(),
            )
        finally:
            try:
                decode(b"", final=True)
                for event in parser.feed(""):
                    self.process_message(event)
            except (EOFError, ParseError):
                pass

    def disable_input(self) -> None:
        try:
            self._disable_mouse_support()
            self._resize_stop.set()
            if self._input_reader is not None:
                self._input_reader.close()
                self._input_reader = None
            if self._input_thread is not None:
                self._input_thread.join(timeout=1.0)
                self._input_thread = None
        except Exception:
            pass

    def stop_application_mode(self) -> None:
        self._disable_bracketed_paste()
        self.disable_input()
        self.write("\x1b[<u")
        self.write("\x1b[?1049l" + "\x1b[?25h")
        self.write("\x1b[?1004l")
        self.flush()

    def close(self) -> None:
        if self._writer_thread is not None:
            self._writer_thread.stop()
        if self._restore_console:
            self._restore_console()
