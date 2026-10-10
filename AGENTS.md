# Working in this repo

What the project is and how to install it: see `README.md`. How the packages fit together and where to make common changes: see `docs/architecture.md`.

## Getting set up

- Check sign-in with `loop status`. If it is not signed in, ask the user to run `! loop login`: it opens a browser, so an agent cannot finish it alone. Never sign out the user's account to test something; point `LOOP_HOME` at a temporary folder instead.
- `loop --mock` and `go test ./...` need no sign-in and run no real tools. Prefer them for UI and logic changes.
- `loop run` runs real shell commands, and its sessions land in the user's database. Keep live tasks harmless and say which session IDs you created.

## Commands

- Build: `go build ./...`
- Check: `go vet ./...`
- Test: `go test ./...`
- Install: `make install` puts the `loop` binary in `~/.local/bin`.

Live tests against a real model use `gpt-5.6-luna` unless a task says otherwise, through `loop run --model gpt-5.6-luna`, and check outcomes with your own commands rather than the model's claims.

**After every finished iteration, run `make install`**, so the latest build can be tried by typing `loop` in any terminal.

## Commits

- Each feature or fix is built on its own branch (`feat/<slug>`, `fix/<slug>`) in small commits, each about one thing, so any step can be reviewed or reverted while the work is in flight.
- When it is finished and validated, it is squash-merged into `main` as a single commit whose title describes the whole change (`git merge --squash`, then commit). `main` gets one commit per feature, not one per step. There are no pull requests.
- Push the branch before squashing if its step-by-step history is worth keeping; delete it once it is no longer needed.
- Every commit builds and passes `go vet` and `go test`.
- Conventional commit titles: `type(scope): what changed`, lowercase, short and descriptive enough to stand alone. Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `infra`. Add a body only when the title can't carry the reason.
- Commits are authored under the repo's configured git identity, with a `Co-Authored-By` trailer for the agent that helped.
- No em dashes in commit messages, docs or code.

## Code

- Idiomatic Go: `gofmt`, standard naming, small packages under `internal/`.
- Names carry the meaning. Prefer a clear name over a comment.
- Doc comments on functions say plainly what the function does, in one or two lines.
- Inline comments only where the code can't be made self-explanatory: a workaround, a protocol quirk, a deliberate non-obvious value.
- No defensive code for conditions that can't happen, and no abstraction the current change doesn't need.

### One job per file

Every file, and every type and function, has a single responsibility you can state in one sentence. When a file starts doing several jobs (for example sign-in, token refresh and logout in one file), split it by job into separate files in the same package before adding more; within a package this costs nothing, since no API or import changes.

- Check this as you write, not only in review: if you can't describe a new file's purpose without "and", it is probably two files.
- Split by responsibility, never by line count alone. About 400 lines is a signal to look at whether a file has taken on a second job, not a limit to cut at.
- No grab-bag files like `utils.go` or `helpers.go`; a helper lives in the file whose job it serves.

### File headers

Every non-test Go file of about 80 lines or more, and any shorter file that holds a flow or several responsibilities, starts with a header comment saying what it is. Tiny files that only define types, constants or lookup tables, platform stubs, and `*_test.go` files don't need one. Every new file that meets this bar gets its header when it is added.

- Format: a `//` block of 3 to 8 lines directly above the `package` clause, in plain English: what the file is for, its main types and functions and how the flow runs through them, and which neighboring packages call it or are called by it.
- One file per package, its main file, carries the package doc, written as `// Package name ...` with no blank line before `package`, so `go doc` shows it. Every other file's header is separated from `package` by a blank line, so it does not become a second package doc. In a file with a `//go:build` line, the header goes after that line's blank line.
- Keep headers current: when a file's purpose or flow changes, update its header in the same commit. Describe what the file does now, never its history or the ticket that changed it.

## Worktrees

Sequential work happens on a branch in the main checkout. Use a git worktree under `.claude/worktrees/` (gitignored) only when several unrelated pieces of work, such as separate UI and backend changes, are in flight at once. Remove worktrees and their branches once they are squash-merged.
