# Start here: the Deedglass app

Run one command and Deedglass opens in your browser. It runs on your own computer, and you are signed
in as the owner. You need no account, no Docker and no database of your own.

## Open it

In a terminal (Terminal on a Mac, PowerShell on Windows), run:

```sh
npx -y deedglass
```

You need Node 18 or newer. `npx -y deedglass app` does the same thing. The app opens at
`http://127.0.0.1:4180` (or a free port if 4180 is busy). The terminal prints the address and a
sign-in link that works once, on this computer.

The first time, Deedglass needs PostgreSQL, the database it keeps its records in. If PostgreSQL 16
or newer is already on your computer, it uses that. If not, it downloads PostgreSQL 18.6 once (14 to
71 MB, depending on your system) and checks the file against a fixed fingerprint. After that it works
without the internet.

The first screens ask who will use Deedglass: a clinic, hospital IT, or people building with AI.
Your answer only changes what you see first. Everything stays available.

## Connect a system

Open **Connect**. Pick what you use:

- **AI tools:** Claude Desktop, Cursor, VS Code, coding agents in a terminal, ChatGPT or OpenAI agents.
- **Systems your agents change:** an AI scribe or a record system (FHIR), or a database (Postgres, MySQL).

Each one takes a click or one copied line, with the steps on screen. Its status goes from
**Not connected** to **Waiting for first action**, then to **Connected** when the tool does its first
thing. Ask it to do something small to check.

Prefer the terminal? `deedglass app connect` lists the same tools and their status, and
`deedglass app connect cursor` (or another tool's name) prints the link, command or settings to
finish with. Nothing it prints holds a key. It needs the app running.

## Simple and Detailed

The app has two views. Switch between them with **Simple** and **Detailed** at the top of the page.

- **Simple** shows three things in plain words: **What agents did**, what is **Waiting for you** to
  approve, and **Locked records** you can check later. Clinics and hospital IT start here.
- **Detailed** is the full Console: agents and their keys, rules (policies), connectors, every
  check and its record. People building with AI start here.

## Check, stop or start over

| Command | What it does |
|---|---|
| `npx deedglass app --status` | Says whether the app is running, at which address, and where its data is. |
| `npx deedglass app --stop` | Stops the app and its database from another terminal. Your data stays. Ctrl+C in the app's own terminal does the same. |
| `npx deedglass app --reset` | Deletes the app's data after asking you first. `--yes` skips the question. The PostgreSQL download is kept. |

While the app runs, other commands talk to it with no extra settings, for example
`deedglass approvals list` and `deedglass actions list`. `deedglass help` shows the eight everyday
commands, and `deedglass help all` shows every command.

## Where your data stays

Everything stays on your computer, in the app's folder:

| System | Folder |
|---|---|
| macOS | `~/Library/Application Support/Deedglass` |
| Linux | `~/.config/deedglass` |
| Windows | `%AppData%\Deedglass` |

`DEEDGLASS_APP_DIR` moves it. The app listens on 127.0.0.1 only, so other computers on your network
can't open it. Deedglass sends nothing to us. The only download is the one-time PostgreSQL file.

To share one Deedglass with a team, run it on a server instead: see
[Production](https://docs.deedglass.com/guides/production/). What doesn't work yet is in
[Known issues](https://github.com/deedglass/deedglass/blob/main/KNOWN_ISSUES.md).
