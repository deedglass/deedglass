# Brand marks on the Connect screen

The Connect screen (`/app/connect`) shows the real mark of each tool a person can connect, so a
clinic's IT lead recognises Claude Desktop, Cursor or PostgreSQL at a glance. Every mark is one
inline SVG path bundled with the Console (`web/src/icons/brand/marks.json` →
`web/src/icons/brandmarks.gen.ts` by `web/scripts/brandmarks.mjs`). Nothing is fetched at run time:
the app is loopback-only and its CSP allows no remote image.

**The marks and names belong to their owners.** They are trademarks of the companies and projects
named below, shown only to identify the tool being connected. No endorsement, sponsorship or
affiliation is implied (DL-108 accepted this use). A mark is never redrawn by hand: where no vector
of the real mark is published under a licence we can bundle, the tile shows the name as text instead
(FHIR, whose flame HL7 publishes only as a raster under its own trademark policy).

| Id | Mark | Colour | Vector source (CC0 1.0) | Owner's brand page |
|---|---|---|---|---|
| `claude` | Claude | `#D97757` | Simple Icons 16.36.0, `icons/claude.svg` | https://claude.ai |
| `openai` | OpenAI (also used for Codex) | black → text colour | Simple Icons 16.36.0, `icons/openai.svg` | https://openai.com/brand |
| `cursor` | Cursor | black → text colour | Simple Icons 16.35.0, `icons/cursor.svg` | https://cursor.com/brand |
| `vscode` | Visual Studio Code | `#007ACC` | Simple Icons 12.4.0, `icons/visualstudiocode.svg` (later Simple Icons releases removed it; the vector derives from the Wikimedia Commons file of the 1.35 icon) | https://code.visualstudio.com/brand |
| `gemini` | Google Gemini | `#8E75B2` | Simple Icons 16.36.0, `icons/googlegemini.svg` | https://gemini.google.com |
| `postgresql` | PostgreSQL | `#4169E1` | Simple Icons 16.36.0, `icons/postgresql.svg` | https://wiki.postgresql.org/wiki/Logo |
| `mysql` | MySQL | `#4479A1` | Simple Icons 16.36.0, `icons/mysql.svg` | https://www.mysql.com/about/legal/logos.html |
| `copilot` | GitHub Copilot | black → text colour | Simple Icons 16.36.0, `icons/githubcopilot.svg` (from Primer Octicons, MIT) | https://primer.style/foundations/icons/copilot-24 |

Simple Icons: https://github.com/simple-icons/simple-icons, CC0 1.0 Universal for the vector data
(the project's own disclaimer: the brands' trademarks remain their owners').

**Dark mode.** A black mark takes `currentColor` (the text colour), so it stays visible on the dark
surface; coloured marks keep the owner's colour in both themes. Colours live only in `marks.json`
(the Console's no-hex-outside-tokens test exempts generated files).

**Changing a mark.** Edit `marks.json` (path, hex, source, licence), run `node web/scripts/brandmarks.mjs`,
and update this table. `web/test/connect.test.ts` checks that every tile's mark exists, carries a
source and licence, is named here, and that the generated file is current.
