Sample built from the Acme demo data

Security review pack · Acme · 17 July to 15 October 2026
Built by Deedglass 0.1.0-dev on 15 October 2026 for org org_01m50d5zr94ahs13tkv9txwz2q.

What's here
  index.html             The document. Open it in a browser; print it or save it as PDF from there.
  answers.md             The same answers in Markdown.
  answers.json           The same answers as data (security_pack v1).
  records.tar.gz         The period's 626 sealed records (export bundle v1).
  pack.seal.json         The sealed records over the files above, and the public keys that check them.

How to check it (offline, no account needed)
  deedglass security-pack verify <this folder>
  One changed byte in any file fails the check and names the file. To be sure the records
  were sealed by this org's own server, ask it for its public keys and add --keys keys.json.

What to send
  The whole folder.
