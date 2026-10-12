# supabase-mcp

Reads run. SQL, migrations, deploys, secrets and branch changes wait for a human. Project changes and branch deletes never run.

For: Supabase's MCP server, wrapped as connector supabase.

## Install

```sh
deedglass policy install supabase-mcp
```

This checks the pack against its signed index, compiles it, runs its test cases and shows the files and the diff before asking `Write 2 files? [y/N]`. It writes `policies/supabase-mcp.yaml` and `policies/supabase-mcp.cases.yaml`, and nothing else: it never pushes or activates a policy.

## Then

```sh
deedglass policy test policies/supabase-mcp.yaml
deedglass policy push policies/supabase-mcp.yaml
```

Activate it in shadow mode first (`deedglass policy activate supabase-mcp --version <n> --mode shadow --reason "…"`), compare with `deedglass policy replay`, then enforce.

## What it doesn't do

A pack is a starting point, not a review of your setup. It decides only the tools it names; everything else falls to its default, which is deny.
