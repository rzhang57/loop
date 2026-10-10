# loop

loop is a bare agent harness: a terminal chat where a model works through a single bash tool. It is a clean base to build other harnesses on.

## Setup

You need Go 1.27 or newer and a paid ChatGPT plan.

```sh
git clone https://github.com/rzhang57/loop.git
cd loop
make install        # builds loop into ~/.local/bin; make sure that is on your PATH
loop login          # signs in with your ChatGPT account in the browser
loop status         # confirms who is signed in
```

Model requests are paid for by your ChatGPT subscription through this OAuth sign-in. There are no API keys to set up.

## Usage

```sh
loop                       # open the chat in the current directory
loop resume [ID]           # reopen a saved conversation, picking from a list without an ID
loop run "task"            # run one task without the interface and print a JSON report
loop run --session ID "…"  # continue a saved conversation headlessly
loop --mock                # open the chat with a scripted model and pretend tools
loop logout
```

In the chat, `/help` lists the slash commands (`/model`, `/resume`, `/clear` and others).

The agent runs real shell commands in the current directory, so watch what you ask it to do.

Sign-in, settings and saved conversations live in `~/.loop/loop.db`. Delete `~/.loop` to start fresh.

## Development

```sh
make build      # go build ./...
make test       # go vet ./... and go test ./...
make install    # rebuild loop into ~/.local/bin
loop --mock     # try UI changes without a model or real tools
```

- [docs/architecture.md](docs/architecture.md) maps the packages and the flow of a task through them.
- [AGENTS.md](AGENTS.md) holds the working rules for this repo (branches, commits, code style), for people and coding agents alike.

## Known limitations

- The only model provider is a ChatGPT plan.
- Quitting in the middle of a turn loses that turn; conversations are saved when a turn ends.
