# Install Deedglass

Deedglass is one binary, `deedglass`: the server, the CLI and the MCP bridge. Pick the way that suits
the machine. The releases are on the
[release page](https://github.com/deedglass/deedglass/releases); 0.1.0 is the first.

## npx (nothing to install)

```sh
npx -y deedglass version
```

npm installs one platform package (`@deedglass/cli-<os>-<arch>`) that carries the release binary,
with no install script. Node 18 or newer runs it. On Windows it runs the native Windows binary
(`@deedglass/cli-win32-x64`); WSL isn't needed. `deedglass app` (the local app in your browser)
needs no Docker on any of them. The Quickstart (`deedglass init`) also needs Docker, on macOS or
Linux.

## The local app (no Docker)

```sh
npx -y deedglass app
```

`deedglass app` (or plain `deedglass` in a terminal) opens Deedglass in your browser at
`http://127.0.0.1:4180`, already signed in. It needs no Docker and no database to set up: it runs
a private PostgreSQL in your user folder, listening on 127.0.0.1 only. It uses a PostgreSQL 16 or
newer that is already installed; otherwise the first run downloads PostgreSQL 18.6 for your
computer once (15 to 71 MB, checked against a pinned SHA-256; ADR-120). After that it works
offline. On an offline machine, point `DEEDGLASS_PG_BUNDLE` at the downloaded `.jar`.

| Command | What it does |
|---|---|
| `deedglass app` | Start it and open the browser (again: a new sign-in tab) |
| `deedglass app --stop` | Stop it and its database; your data stays |
| `deedglass app --status` | Say whether it runs, where, and where the data is |
| `deedglass app --reset` | Delete the app's data (asks first; `--yes` skips the question) |

The data lives in `~/Library/Application Support/Deedglass` (macOS), `~/.config/deedglass`
(Linux) or `%AppData%\Deedglass` (Windows); `DEEDGLASS_APP_DIR` moves it. While the app runs, CLI
commands such as `deedglass actions list` or `deedglass approvals list` talk to it with no flags.
Piped, in CI, with `--json` or with `DEEDGLASS_NO_APP=1`, plain `deedglass` prints the welcome
instead and starts nothing. The sign-in link works once and only on this computer; the app never
listens on your network.

## The install script (macOS, Linux)

```sh
curl -fsSL https://deedglass.com/install.sh | sh
```

It downloads two files from the latest release, the archive for this machine and `checksums.txt`,
checks the archive's sha256 against `checksums.txt`, and copies the one binary into `~/.local/bin`.
With cosign installed it also checks the signature.
It runs nothing it downloads, asks for no password and never edits your shell profile; if the
directory isn't on your PATH it prints the line to add. `sh -s -- --version 0.2.1 --dir /usr/local/bin`
picks a version and a directory. Read it first: [`scripts/install.sh`](../scripts/install.sh).

Where the files come from: `deedglass.com/dl/`, a byte-for-byte copy of each release's
`checksums.txt`, its signature and the macOS and Linux archives, served by Cloudflare; any file the
copy doesn't have comes from the GitHub release. The checks are the same either way: the sha256
against `checksums.txt`, and the signature, which only this repository's release workflow can make.
Each download gives up on a connection after 5 s and retries, because on one network GitHub's
download hosts took 50-75 s to connect. On that network the install took 6.6-7.3 s (three runs)
from deedglass.com, against 81-119 s when every file came from GitHub.
`DEEDGLASS_MIRROR_URL=none` uses GitHub only.

The same script is a file on each release since 0.9.0, if you'd rather fetch it from GitHub:

```sh
curl -fsSL https://github.com/deedglass/deedglass/releases/latest/download/install.sh | sh
```

## A binary

Download the archive for your platform from the release page:
`deedglass_<version>_<os>_<arch>.tar.gz` for linux and darwin on amd64 and arm64, or
`deedglass_<version>_windows_amd64.zip`. Check it (below), unpack it, and put `deedglass` on
your PATH. Each archive holds the binary, LICENSE, README.md, CHANGELOG.md and shell completions;
from 0.1.1 it also holds `ee/LICENSE`, the licence of the Pro code the binary includes.

## Docker

There's no public image yet: the image on ghcr.io is private for now, and a public one is coming.
Until it's published, build the image from a checkout:

```sh
git clone https://github.com/deedglass/deedglass && cd deedglass
make web-build docker        # deedglass:dev; make web-build needs Node 24 and embeds the Console
docker run --rm deedglass:dev version
```

The image is distroless and runs as user 65532. Without `make web-build` the binary carries the
Console's placeholder page, which is fine for `deedglass init` and dev mode, but a prod server
(`DEEDGLASS_ENV=prod`) refuses to start with it.

## Homebrew

Once the tap exists: `brew install deedglass/tap/deedglass`. The binaries aren't notarized yet, so
macOS asks on first run.

## go install

```sh
go install github.com/deedglass/deedglass/cmd/deedglass@latest
```

This builds the CLI without the Console. It runs every CLI command, and `serve` in dev mode; a prod
server needs a release binary or image, which carry the Console.

## Check what you downloaded

Each release lists sha256 checksums, and the checksum file is signed in the open Sigstore log by this
repository's release workflow (`checksums.txt.sigstore.json` on the release). Here is how to check
both before you run the binary.

```sh
shasum -a 256 --ignore-missing -c checksums.txt
```

Expected: `deedglass_<version>_<os>_<arch>.tar.gz: OK`. Then, with
[cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 3.x:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/deedglass/deedglass/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Expected: `Verified OK`. That says the checksum file was signed by this repository's release
workflow for a version tag; it says nothing about what the code does. Each archive also has an SPDX
SBOM next to it (`<archive>.sbom.json`) listing the Go modules inside.

## Shell completions

The archives carry them in `completions/`. Or generate them from any binary:

```sh
deedglass completion zsh > ~/.zfunc/_deedglass        # with fpath+=(~/.zfunc) before compinit
deedglass completion bash > ~/.local/share/bash-completion/completions/deedglass
deedglass completion fish > ~/.config/fish/completions/deedglass.fish
```

## Check for a newer version

```sh
deedglass version --check
```

It asks GitHub's releases API once (3 s, only a User-Agent) and never runs on its own. Exit 0 when
the check ran, 2 when it couldn't tell.

## Windows

The CLI, `verify` and the MCP bridge run on Windows. `serve` is untested there; run the server on
Linux or macOS, or in Docker.

## What it doesn't do

- There's no public container image yet (above), and macOS binaries aren't notarized.
- There are no OS packages (deb, rpm), Homebrew tap or Windows installer yet; the install script
  covers macOS and Linux only.
