# Install Deedglass

Deedglass is one binary, `deedglass`: the server, the CLI and the MCP bridge. Pick the way that suits
the machine. The releases are on the
[release page](https://github.com/deedglass/deedglass/releases); 0.1.0 is the first.

## npx (nothing to install)

```sh
npx -y deedglass version
```

npm installs one platform package (`@deedglass/cli-<os>-<arch>`) that carries the release binary,
with no install script. Node 18 or newer runs it, on Windows too (no WSL). The Quickstart
(`deedglass init`) also needs Docker, on macOS or Linux.

## The local app (no Docker)

`npx -y deedglass app` (or plain `deedglass` in a terminal) opens Deedglass in your browser on
127.0.0.1, signed in, with no Docker. It runs its own PostgreSQL in your user folder: an installed
16+, or a one-time pinned download of 18.6 (ADR-120); then it works offline. `--stop`, `--status`
and `--reset` manage it; `deedglass help app` has the details. New to Deedglass? Read
[Start here: the Deedglass app](APP.md) for a short tour.

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

Files come from `deedglass.com/dl/`, a copy of each release served by Cloudflare, else the GitHub
release; the sha256 and signature checks are the same either way. `DEEDGLASS_MIRROR_URL=none` uses GitHub only.

The same script is a file on each release since 0.9.0, if you'd rather fetch it from GitHub:

```sh
curl -fsSL https://github.com/deedglass/deedglass/releases/latest/download/install.sh | sh
```

## A binary

Download the archive for your platform from the release page:
`deedglass_<version>_<os>_<arch>.tar.gz` for linux and darwin on amd64 and arm64, or
`deedglass_<version>_windows_amd64.zip`. Check it (below), unpack it, and put `deedglass` on
your PATH. Each archive also holds the licences (`LICENSE`, `ee/LICENSE`), the README, the
changelog and shell completions.

## Docker

There's no public image yet (a public one is coming); build one from a checkout:

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

It asks GitHub's releases API once and never runs on its own. Exit 2 when it couldn't tell.

## Windows

`npx deedglass` runs the native Windows binary. `deedglass app` runs the server and the Console
locally; for a shared server, use Linux, macOS or Docker.

## What it doesn't do

- There's no public container image yet (above), and macOS binaries aren't notarized.
- There are no OS packages (deb, rpm), Homebrew tap or Windows installer yet; the install script
  covers macOS and Linux only.
