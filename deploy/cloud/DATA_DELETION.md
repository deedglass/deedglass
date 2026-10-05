# Delete an org's data

When a beta org asks for its data to be deleted. One transaction deletes every row the org has in
every table, and returns the count per table. It can't be undone; back up first.

## 1. Confirm the request, in writing, from the org's owner

Note the org id (`org_…`) and slug.

## 2. Back up

```sh
sudo /opt/deedglass/deploy/cloud/scripts/backup.sh
```

## 3. Drop the org's replication slots

On our side: `sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml run --rm deedglass --org <slug> decoder drop --connector <name> --confirm <name>`
for each Postgres connector. If you can't, give the customer the SQL for their database:

```sql
SELECT pg_drop_replication_slot('deedglass_<connector id without con_>');
DROP PUBLICATION IF EXISTS deedglass_pub;
```

## 4. Purge

```sh
sudo docker compose -f /opt/deedglass/deploy/cloud/compose.yaml exec -T -u postgres postgres \
  psql -h /run/postgresql -d deedglass -c "SET ROLE deedglass_migrator; SELECT deedglass.purge_org('org_…', '<slug>');"
```

Expected: one JSON object with a count per table and `"orgs": 1`. A wrong slug deletes nothing
(`DG007`). Only the schema owner can run it.

## 5. Record it

```sh
echo '{"org":"org_…","counts":<the JSON above>,"at":"'"$(date -u +%FT%TZ)"'","operator":"arjun"}' | sudo tee -a /var/lib/deedglass/purge-log.jsonl
```

## 6. Tell the org

Their data is deleted from the live database now; backups that hold it age out within 35 days.
Deeds they exported before still verify offline against the published keys.

## What this doesn't cover

Deleting data inside the customer's own databases, and removing a person from one org while keeping
the org (use Console › Settings › Members).
