# Network egress: the one door

Every connection Deedglass makes to a destination someone configured goes through one package,
`internal/connector/egress` (S16): the REST connector's `base_url`, the MCP HTTP gateway's
upstreams, webhooks (S22), the SaaS connectors (S29–S30), the Postgres connector's dial (S17),
`deedglass verify --keys`, and head timestamps (TY-03: the TSAs and transparency service listed in
`timestamps.*`, asked by `deedglass deeds timestamp` and by exports; nothing is asked when none is listed). `TestSingleEgressDoor` fails the build if any other package builds an
`http.Client` or an `http.Transport`, dials on its own, or uses `http.DefaultClient`, `http.Get`,
`http.Post`, `http.Head` or `http.PostForm`.

## Classification

Each address is classified; the first match wins.

| # | Ranges | Class | Allowed |
|---|---|---|---|
| 1 | `169.254.169.254`, `169.254.170.2`, `100.100.100.200`, `fd00:ec2::254`, `fd20:ce::254`, `169.254/16`, `fe80::/10`, the host `metadata.google.internal` | cloud metadata, link-local | **never**, even with the dev override |
| 2 | `0/8`, `::/128`, the rest of `::/96`, `224/4`, `ff00::/8`, `240/4` (with `255.255.255.255`), `192.0.0/24`, `192.0.2/24`, `198.18/15`, `198.51.100/24`, `203.0.113/24`, `100::/64`, `100:0:0:1::/64`, `2001::/23` (Teredo included), `2001:db8::/32`, `3fff::/20`, `5f00::/16`, `fec0::/10`, `::ffff:0:0:0/96` | reserved | never |
| 3 | `::ffff:0:0/96`, `64:ff9b::/96`, `64:ff9b:1::/48`, `2002::/16` | embeds IPv4 | the embedded IPv4 is classified instead (for `64:ff9b:1::/48` both readings, and the stricter wins) |
| 4 | the resolved `ip:port` of `DEEDGLASS_DATABASE_URL` | Deedglass's own database | prod: only if that exact address (a `/32` or `/128`) is in `DEEDGLASS_EGRESS_ALLOW_CIDRS`; dev override: allowed, with a warning |
| 5 | `127/8`, `::1`, `localhost`, `*.localhost` | loopback | dev with `DEEDGLASS_ALLOW_PRIVATE_NETWORKS=1` only |
| 6 | `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `fc00::/7` | private | the dev override, or inside `DEEDGLASS_EGRESS_ALLOW_CIDRS` |
| 7 | everything else | public | yes |

Other ports on the database's host are classified normally (class 5 or 6).

## The two settings

- **`DEEDGLASS_ALLOW_PRIVATE_NETWORKS`** (dev only; prod refuses to start with it): lets
  connectors reach loopback and private networks, so the mock orders API on `127.0.0.1:8090`
  works. It never reaches classes 1 to 3. **Why the dev override still can't reach metadata:** a dev
  laptop on a cloud VM, or a dev server in a cloud account, has the same metadata endpoint as prod.
  An agent that talks a connector into `169.254.169.254` would read that machine's cloud
  credentials. No dev convenience is worth that.
- **`DEEDGLASS_EGRESS_ALLOW_CIDRS`** (any env): comma-separated CIDRs of private networks
  connectors may reach, for a self-hosted Deedglass next to a private database or internal API:

  ```sh
  DEEDGLASS_EGRESS_ALLOW_CIDRS=10.20.0.0/16,192.168.4.7/32
  ```

  The config refuses an entry that overlaps classes 1 to 3 or loopback (so `0.0.0.0/0` is refused).
  To let a connector reach Deedglass's own database on purpose, list its exact address.

## URL rules, checked before DNS

- `https`; `http` only in dev.
- No user name or password in the URL (`https://example.com@169.254.169.254/` is refused here).
- An ASCII host, lowercased, with one trailing dot stripped. Write an international domain name in
  its punycode form (`xn--…`).
- No non-canonical numeric host: `2852039166`, `0xA9FEA9FE`, `0251.0376.0251.0376`, `127.1` and `0`
  are refused, because a URL parser reads them as `169.254.169.254` or `127.0.0.1`. Write
  `93.184.216.34`, or use the name.
- No IPv6 zone ids (`[fe80::1%25en0]`).

## Dialing: how rebinding is defeated

1. Deedglass resolves the host itself (A and AAAA).
2. It classifies every answer. If **any** answer is blocked, the destination is refused (fail
   closed on mixed answers: one public and one metadata address is refused).
3. It dials the checked address, not the name. TLS still verifies the original host name.
4. The dialer's `ControlContext` checks the address again at connect time, so even a path that
   skipped resolution can't connect to a blocked address.
5. Environment proxies (`HTTPS_PROXY`, …) are ignored for connector traffic: proxy support is v2.

A name that resolves to a public address when the connector is created and to `127.0.0.1` at call
time is refused at the dial, and the action fails as `destination_blocked` with nothing sent.

## Redirects

`none` (the default, and always for writes) returns the redirect as the answer.
`same_host` follows at most 3 hops to the same scheme, host and port; https to http, another host
and a fourth hop are refused, and every hop goes through the same dialer and its checks.

## Client defaults

| | |
|---|---|
| Connect | 5 s per address |
| TLS handshake | 5 s, TLS 1.2 or newer, always verified |
| Response headers | 10 s (an MCP upstream uses its own timeout) |
| Whole call | the connector's `timeout_ms` and the action's deadline |
| Body | read through a cap (`max_response_bytes`) counted after gzip decoding, so a decompression bomb stops at the cap; one byte over is `response_too_large`, never a silent truncation |
| User-Agent | `deedglass/<version> (+https://docs.deedglass.com/connectors)` |

## What admins and agents see

A refused destination is `destination_blocked`. The admin reads the address and its class:
`base_url resolves to 169.254.169.254, a cloud metadata address. Deedglass never connects to cloud
metadata addresses, even in dev. Use the service's public address.` The agent reads
only: `The connector's destination is blocked by Deedglass's network rules. An admin can see why
in Console › Connectors.`, so it can't map the network by probing.

## First-party destinations

The CLI, `deedglass mcp serve`, the MCP wrapper and the Go client talk to the Deedglass server the
person configured, and the mailer to the fixed Resend API. They use `egress.FirstParty`: the same
TLS floor and timeouts, no address classification (talking to Deedglass on localhost is the normal
dev case), and the CLI may honour a proxy. Nothing an org or an agent configures uses it.

## Testing

`internal/connector/egress/testdata/corpus.json` holds 59 destinations, each decided in prod and in
dev with the override (`go test ./internal/connector/egress -run TestCorpus -v`): metadata, numeric
tricks, embedded IPv4, loopback, private, reserved, URL shapes, and resolver tricks
(`egress.StaticResolver` answers from fixed data, including answers that change between lookups).
`FuzzCheckURL` checks that an accepted URL never resolves to a refused class.

## What egress doesn't do

- **It sees addresses, not intent.** An allowed API can still be misused; that is policy's job.
- **No proxy support** yet (v2), and no per-connector trust roots.
- **DNS is the system resolver's.** Deedglass doesn't run its own resolver or pin answers across
  calls; it re-resolves and re-checks on every connection.
- **It doesn't inspect payloads** or rate-limit destinations.
