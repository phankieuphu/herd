# herd

Run many Claude Code and Codex agents side by side in tmux, with one sidebar that tells you which one needs you.

```
 herd                       ◆1 ⠹2       ┃
▌◆ fix auth •                  2m       ┃  Do you want to proceed?
   claude · herd/fix-auth +42 −7        ┃  ❯ 1. Yes
 ⠹ docs                                 ┃    2. No
   codex · herd/docs +3 −1              ┃
 ▾ infra 2                              ┃      (the agent you picked
  ⠹ migrate db                          ┃       runs here, full size)
    claude · herd/migrate-db            ┃
  ☾ logs                                ┃
    shell · main                        ┃
── docs ─────────────────────────       ┃
 • Working (14s • esc to interrupt)     ┃
 Claude 5h ▰▰▱▱▱ 42% wk ▰▰▰▰▱ 85%       ┃
 n new ⏎ open tab next ? keys           ┃
```

herd is a small Go program. It runs inside any terminal (iTerm2, Terminal.app, Ghostty, Warp, Alacritty, or a terminal app on your phone over ssh). It only needs **tmux 3.2+** and **git**.

## What it does

- **Agent states at a glance.** Each row shows if the agent is working (spinner), **needs you** (◆), done (✓), has background work running (◐), exited (○) or is set aside (☾). A dot (•) means "you have not looked yet".
- **One key to answer.** When an agent asks for permission, press `a` to allow or `A` to deny, without leaving the sidebar. `m` sends a short message.
- **Live preview.** Move the cursor over an agent to see its last lines, without switching to it.
- **A git worktree per agent.** A new agent in a git repo gets its own branch and folder, so two agents never edit the same files. The row shows the branch and diff size. `d` opens a review popup, `D` removes the worktree when you are done.
- **Groups** that stay put. Order, names, groups and flags live in tmux, so they survive restarts.
- **Notifications** on your desktop, in the tmux status line, and on your phone (ntfy or Telegram) when you are away.
- **Status bar segment.** `herd status --tmux` prints `◆1 ●2 ✓1` for tmux's `status-right`.
- **Limit rings.** Usage of the 5-hour window and the weekly limit per provider, read from a JSON file.
- **Works over ssh.** Every agent is a normal tmux session. From your phone, `herd attach fix-auth` or `herd` gives you the same view.
- **Any agent.** Detection rules are regular expressions in the settings file, so you can add Gemini CLI, Aider, OpenCode or anything else.

## Install

With Go 1.24 or newer:

```sh
go install github.com/phankieuphu/herd/cmd/herd@latest
```

Or download a binary from [Releases](https://github.com/phankieuphu/herd/releases) and put it in your `PATH`.

You also need tmux (`brew install tmux` or `sudo apt install tmux`).

## Quick start

```sh
herd install-hooks     # once: lets Claude Code (and Codex) report their state
herd                   # opens the herd window
```

In the sidebar press `n`, choose the agent, folder, name and first prompt, then `⏎`. Start a few more. Press `tab` to jump to the next agent that needs you.

`q` detaches (everything keeps running). Run `herd` again to come back.

You can also start agents from any shell:

```sh
herd new -a claude -d ~/code/api -n "fix auth" -p "fix the login bug"
herd new -a codex -g docs "update the README for the new flags"
herd ls
```

## Keys

| Key | Action |
|---|---|
| `↑↓` / `j k` | move |
| `⏎` | open the agent (keyboard goes to it) |
| `space` / `l` | show the agent, stay in the sidebar |
| `tab` / `shift+tab` | next / previous agent that needs you |
| `n` / `N` | new agent / new agent from a preset |
| `tab` in the folder field | complete the folder name (press again to step through matches) |
| `a` / `A` | allow / deny (only when it needs you) |
| `m` | type a message to the agent |
| `r` | rename the agent or group |
| `g` | move to a group (`tab` completes) |
| `J` / `K` | move the agent or group down / up |
| `⏎` on a group | fold / unfold |
| `z` | set aside (moon): no notifications, stays in the list |
| `u` | mark read / unread |
| `d` | review the diff in a popup |
| `D` | remove the agent's worktree |
| `x` | close the agent |
| `/` | filter |
| `p` | show / hide the preview |
| `q` | detach |
| `Q` | close the herd window (agents keep running) |
| `?` | help |

Mouse works too: click a row to open it, scroll to move.

Two tmux keys are added (turn them off with `bind_keys = false`):

- `prefix C-h` — back to the sidebar
- `prefix C-j` — jump to the next agent that needs you

Change them with `focus_key` and `jump_key` in `[integrations.tmux]`. Write `"prefix C-h"` for a key pressed after the tmux prefix, or just `"M-h"` for a key that works on its own (`M-` is Option/Alt; on macOS turn on "Option as Meta" in your terminal). `""` turns a single key off. The new keys apply the next time you run `herd`.

## How it works

```
┌──────── tmux session "herd" ────────────────┐
│ sidebar (herd)  │  stage                     │      herd-fix-auth  (agent's own session)
│                 │  ← the agent you picked    │      herd-docs
│                 │    is swapped in here      │      herd-migrate-db
└─────────────────┴────────────────────────────┘      ...
```

- Every agent runs in its own tmux session (`herd-<name>`).
- To show an agent, herd swaps its pane into the stage (`tmux swap-pane`). A small placeholder takes its place in the agent's own session, so nothing is restarted and nothing is lost.
- Everything herd knows is stored in tmux pane options (`@herd_title`, `@herd_group`, `@herd_view`, ...). There is no database and no server. If the sidebar restarts, it continues where it was.

### How herd knows what an agent is doing

1. **Hooks (best).** `herd install-hooks` adds `herd hook claude` to `~/.claude/settings.json` for these events: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `Notification`, `Stop`, `SubagentStop`, `PreCompact`, `SessionEnd`. The hook finds its pane from `$TMUX_PANE` and writes the state there. It never fails the agent. On `Stop` it reads the transcript and counts background shells and subagents that are still running, so a row says "background" until that work is really done.
   For Codex, it sets `notify = ["herd", "hook", "codex"]` in `~/.codex/config.toml` (only if you have no other notify program).
2. **The screen.** herd also reads the visible text of each agent pane about once a second. "esc to interrupt" means working; "Do you want to proceed?" means it needs you. This works with no hooks at all, and it catches prompts before hooks do.
3. **The process.** When the agent quits and only the shell is left, the row shows "exited".

A short grace time stops spinner blinks from looking like "done".

Run `herd uninstall-hooks` to remove the Claude Code hooks again. Your other hooks are kept, and the old file is saved as `settings.json.herd-bak`.

## Use it from your phone

All agents are normal tmux sessions, so any ssh app works:

```sh
ssh my-mac
herd                  # the full herd window (it fits small screens: try width = 26)
herd attach fix-auth  # or just one agent, full screen
```

A session you start somewhere else can join the sidebar:

```sh
tmux new -s hotfix
tmux set-option @herd_adopt 1
```

Phone notifications: herd can push when an agent needs you and no tmux client was used for a while.

```toml
[notify.push]
enabled = true
provider = "ntfy"
ntfy_url = "https://ntfy.sh/pick-a-long-secret-topic"
away_after = "2m"
```

Install the ntfy app and subscribe to the same topic. For Telegram, set `provider = "telegram"`, `telegram_token` and `telegram_chat_id`.

## Status bar

```tmux
set -g status-right '#(herd status --tmux) %H:%M'
set -g status-interval 2
```

`herd status --json` prints every agent for scripts.

## Settings

herd reads `~/.herd/settings.toml` and reloads it when it changes. `herd init-config` writes a commented file. Everything is optional.

```toml
[sidebar]
session = "herd"     # name of the main tmux session
width = 34
poll = "1s"
preview = true
preview_lines = 8
compact = false      # one line per agent

[defaults]
agent = "claude"
worktree = true                  # new agents in a git repo get their own worktree
worktree_dir = "~/.herd/worktrees"
branch_prefix = "herd/"

[notify]
desktop = true                   # macOS notification center / notify-send
tmux = true                      # message in the tmux status line
on = ["needs_you", "done"]       # also: "exited"

[integrations.limit_rings]
enabled = true
file = "~/.cache/limit-rings.json"

[integrations.tmux]
bind_keys = true
focus_key = "prefix C-h"         # go to the sidebar; "M-h" = Option+h, no prefix
jump_key = "prefix C-j"          # go to the next agent that needs you
session_script = ""              # rename/group script: called as `rename <shell_pid> <group> <title>` and `group <session_id> <old> <new>`
```

### Add your own agent

```toml
[agents.gemini]
command = "gemini"
working = ["esc to cancel"]
needs_you = ["Allow execution\\?", "Apply this change\\?"]
approve_keys = ["Enter"]       # what `a` sends (tmux key names)
deny_keys = ["Escape"]         # what `A` sends
```

You can change the built-in ones the same way, for example `[agents.claude] command = "claude --model opus"`. Rules you do not set keep their built-in values.

### Presets

```toml
[presets.review]
agent = "claude"
dir = "~/code/api"
name = "review"
prompt = "Review the last commit and list problems."
group = "reviews"
worktree = false
```

Press `N` in the sidebar, or run `herd new --preset review`.

### Limit rings file

herd reads one JSON file: one object, one key per provider. herd never fetches numbers itself; another tool keeps the file up to date.

```json
{
  "claude": { "name": "Claude", "_ts": 1760000000, "used5h": 42.5, "reset5h": 1760010000,
              "usedWeek": 18, "resetWeek": 1760400000 },
  "codex":  { "_ts": 1760000000, "used5h": 90, "reset5h": 1760005000 }
}
```

`_ts` is when the numbers were read (epoch seconds). Readings older than 15 minutes, or with `"ok": false` or `"stale": true`, show as stale. A reset time in the past hides that number. From 80% the bar turns orange; at 100% it shows ●.

## Commands

```
herd                      open the herd window (start it if needed)
herd new [flags]          start an agent (-a agent -d dir -n name -p prompt -g group --preset --no-worktree --show)
herd ls                   list agents
herd attach [name]        attach to an agent's own session
herd show <name>          show an agent in the herd window
herd jump                 show the next agent that needs you
herd status [--tmux|--json]
herd install-hooks        [--claude] [--codex] [--dry-run]
herd uninstall-hooks
herd init-config
herd doctor               check your setup
herd quit                 close the herd window (agents keep running)
```

## Troubleshooting

- **A row never leaves "working".** Run `herd install-hooks` and restart that agent. Without hooks herd only reads the screen. You can also add your own `working` / `needs_you` patterns.
- **Hook problems.** Set `HERD_HOOK_LOG=/tmp/herd-hook.log` in your environment and look at the log.
- **Strange characters.** Use a UTF-8 locale and a font with the box drawing symbols.
- **Do not kill the `herd` session** with `tmux kill-session` while an agent is shown in it — that kills the shown agent too. Use `Q` or `herd quit`, which send it home first.

## Development

```sh
go test ./...        # unit tests plus integration tests against a private tmux server
go build ./cmd/herd
```

## License

MIT
