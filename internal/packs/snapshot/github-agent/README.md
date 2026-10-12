# github-agent

Reads, issues and feature branches run. Protected branches wait for a human. Merges never run.

For: the GitHub connector.

## Install

```sh
deedglass policy install github-agent
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/github-agent.yaml` and `policies/github-agent.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/github-agent.yaml
deedglass policy push policies/github-agent.yaml
```

Activate it in shadow mode first (`deedglass policy activate github-agent --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## What it doesn't do

A pack is a starting point, not a review of your setup. It decides only the tools it names; everything else falls to its default, which is deny.
