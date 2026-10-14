# Recordings

Every demo clip comes from a real run (CLAUDE.md rule 12). Add one row per kept take: the date,
the clip, the commit it ran on (`git -C ~/txv/product rev-parse --short HEAD`), and the command.

| Date | Clip | Commit | Command | Notes |
|---|---|---|---|---|
| 2026-10-13 | hero-short (replay) | 511e7e4 | node scripts/short/capture.mjs && node scripts/short/short.mjs | cast sha256:e9520819f9b4, captions sha256:ab26398b74fa, output sha256:5b089f0d180a, speed 1, 19.03 s |
| 2026-10-14 | readme-hero (docs/assets/demo-hero.svg, the README recording) | 97a524c | (growth deploy/content) node scripts/short/capture.mjs --slug readme-hero --pace 1.5, then (product) node scripts/demo/cast2svg.mjs docs/assets/demo-hero.cast.json docs/assets/demo-hero.svg | cast log sha256:c081566f892f, svg sha256:3485264adb2e, 96 lines, each shown at its recorded time; 8.1 s of start-up before the first line skipped; window scrolls at reading pace |
