# Approvals in Slack: the example app

`manifest.yaml` is the Slack app Deedglass approvals use. Print it with your server's public HTTPS
address filled in, then create the app from it:

```sh
deedglass slack manifest --base-url https://your-host > slack-manifest.yaml
```

1. At api.slack.com/apps, choose Create New App › From a manifest, pick your workspace and paste the
   manifest. Install it to the workspace.
2. Copy the Bot User OAuth Token (`xoxb-…`) and the Signing Secret into your shell, then store them
   (Deedglass never reads secrets from flags):

   ```sh
   export SLACK_BOT_TOKEN=… SLACK_SIGNING_SECRET=…
   deedglass connector create --config examples/connectors/slack.yaml
   deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET
   ```
3. Invite `@deedglass` to `#deedglass-approvals` and `#agent-updates`, then run
   `deedglass connector test slack`.

On a laptop, Slack needs a public HTTPS URL for the Interactivity Request URL. A Cloudflare quick
tunnel works (`cloudflared tunnel --url http://127.0.0.1:8080`); its address changes every run, so
update the app's Interactivity URL when it does.

Why each scope is there: [docs/connectors/slack.md](../../docs/connectors/slack.md). How approving
from Slack works and its limits: [docs/APPROVALS.md](../../docs/APPROVALS.md).
