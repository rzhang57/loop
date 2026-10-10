# Architecture

How the `loop` harness is put together, for anyone about to change it. Every package's main file starts with a header comment that goes into more detail; `go doc ./internal/<package>` prints it.

## The flow of a task

```
cmd/loop ──► tui (chat)  ─┐
         └─► headless    ─┴─► agent.Agent.Send ──► llm.Model.Respond   (llm.OpenAI, tokens from auth)
                                    │
                                    └─► agent.Tool.Run ──► tools.Bash   (local.bash)
after each turn: agent history ──► session.Save ──► store (SQLite at ~/.loop/loop.db)
```

1. The user types a task in the chat (`tui`) or passes one to `loop run` (`headless`).
2. `agent.Agent.Send` adds it to the history and asks the model for a turn.
3. The model answers with text, tool calls, or both. The agent runs each call through its `Tool`, appends the output, and asks again, until the model answers without calling a tool or 50 turns pass.
4. Progress streams back as `agent.Event` values, which the chat draws and `headless` folds into a JSON report.
5. When the turn ends, the history is saved through `session` into `store`, so `/resume`, `loop resume` and `loop run --session` can continue it.

## Packages

| Package | Job |
|---|---|
| `cmd/loop` | The binary. `main.go` dispatches commands, `wiring.go` builds the agent with its tools and system prompt, `run.go` is `loop run`, `account.go` is login, logout and status. |
| `internal/agent` | The model-and-tools loop for one conversation and its events. |
| `internal/llm` | Provider-neutral conversation types (`Item`, `Request`, `Response`, `Delta`) and the `Model` interface, plus the OpenAI Responses API client paid for by a ChatGPT plan. |
| `internal/auth` | ChatGPT sign-in (browser OAuth with PKCE), token refresh shared safely across processes, and sign-out. |
| `internal/tools` | The agent's tools. Today that is `local.bash`. |
| `internal/headless` | `loop run`: one prompt to completion and a JSON report of every step. |
| `internal/session` | Saving, loading and listing conversations. |
| `internal/store` | A small SQLite key-value store shared by every `loop` process. |
| `internal/tui` | The full-screen chat, built on Bubble Tea. |
| `internal/mock` | A scripted model and fake tools, for `loop --mock` and tests. |

## The seams

These interfaces are where new work plugs in:

- **`llm.Model`** (`internal/llm/llm.go`): one method, `Respond`. `llm.OpenAI` and `mock.Model` implement it.
- **`agent.Tool`** (`internal/agent/agent.go`): `Spec`, `Summary` and `Run`. `tools.Bash` and `mock.Tool` implement it.

## Common changes

- **Add a tool:** implement `agent.Tool`, register it in `newAgent` in `cmd/loop/wiring.go`, and update the system prompt in the same file, which names the tools.
- **Add a model provider:** implement `llm.Model` (and `tui.ModelLister` for the `/model` picker), then use it in `newOpenAI` and `realDeps` in `wiring.go`. Sign-in is ChatGPT only today.
- **Change the chat UI:** try it with `loop --mock`, which needs no sign-in and runs no real tools. Model-level tests live in `internal/tui/*_test.go`.

## Where things live on disk

- `~/.loop/loop.db`: sign-in tokens, the chosen model, and saved conversations as `session.<id>` keys.

Set `LOOP_HOME` to keep it somewhere else, for example a temporary folder for an experiment. `loop help` lists the other environment variables.
