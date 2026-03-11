# loop Agent Guide

## Project intent
- Build a local-first chat application.
- Current phase is `/init` only: keep implementation minimal and scaffold-oriented.
- Primary interface is a Textual TUI.
- Future GUI/API work will live behind FastAPI, but `server/` can stay empty until that work starts.

## Architecture
- `core/`: application logic, controllers, provider integrations, persistence, and shared models.
- `ui/`: Textual TUI code only.
- `server/`: future FastAPI wrapper around core controllers.
- `main.py`: local entrypoint that wires the TUI directly to `core`.

## Product constraints
- Keep the app bare bones at first. Prefer clear structure over premature features.
- Chat history should be stored locally on the machine under `~/.loop/` by default unless a later change intentionally moves persistence to SQLite.
- OpenRouter is the first provider target. Design provider boundaries so additional APIs can be added later without reshaping the app.
- For now, the TUI can import `core` modules directly. Do not add HTTP layers for local use.

## Python guidance
- Use plain Python modules with minimal dependencies.
- Prefer stdlib for config, filesystem, and serialization until there is a clear need for more.
- Keep models and storage formats simple and inspectable.
- Avoid adding async or concurrency complexity unless Textual integration requires it.

## Init-phase expectations
- Favor scaffolding, interfaces, and folder structure over full feature completion.
- Stub future extension points clearly, but do not overengineer abstractions.
- When forced to choose, optimize for a fast path to a working local TUI chat loop.
