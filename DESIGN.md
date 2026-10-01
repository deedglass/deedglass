---
# Derived from DEEDGLASS_BRAND.md (binding). If they disagree, DEEDGLASS_BRAND.md wins; fix this file.
version: alpha
name: Deedglass
description: "The execution and accountability layer for AI agents, dressed as a quiet, exact, expensive-looking product. A near-black ink canvas (#07080C) in dark mode and warm paper (#F7F7F4) in light mode carry a system that is about 90% neutral. One interactive accent (#2F5BEA light, #6F8DFF dark) marks links, primary buttons and focus. The signature aurora gradient (#7C6CFF to #4F8CFF to #2FE6D1, 135deg) is scarce: the Split Orb logo, one hero moment per page, the Sealed animation, the marketing CTA border glow, and video or social backgrounds. Never in UI chrome, tables, charts or body text. Inter Variable for everything, Geist Mono for ids, hashes, SQL, policies and CLI. Glass is an accent material for floating layers only. Verdicts (VERIFIED, MISMATCH, UNVERIFIABLE) and levels (L0 Reported, L1 Confirmed, L2 Witnessed) have their own calm, AA-checked colour pairs. Stripe's precision, Linear's calm, Apple's restraint; it never shouts."

colors:
  # Neutrals, dark ramp (brand doc 6.1)
  ink-950: "#07080C"            # dark canvas: site hero, Console dark, video
  ink-900: "#0E1017"            # dark surface, cards, code blocks (both themes)
  ink-850: "#141722"            # raised dark surface, row hover
  ink-800: "#1C1F2A"            # hairlines and borders on dark
  ink-700: "#2A2E3B"            # strong borders on dark, disabled fills
  snow-text: "#F5F6FA"          # primary text on dark (18.5:1 on ink-950)
  slate-400: "#8B90A0"          # secondary text on dark (6.3:1 on ink-950)
  slate-450: "#7D8293"          # proposed: tertiary text on dark (5.2:1 on ink-950, 4.96:1 on ink-900)
  # Neutrals, light ramp (brand doc 6.1)
  paper-50: "#F7F7F4"           # light canvas, warm paper, not pure white
  white: "#FFFFFF"              # light surface, cards
  mist-100: "#EDEEF2"           # light surface alt
  hairline-light: "#E4E5EA"     # 1px borders on light (brand doc 8)
  slate-300: "#B9BDC8"          # strong borders on light, input borders
  slate-500: "#6B7080"          # tertiary text on light (4.6:1 on paper-50; fails on mist-100 and on dark)
  slate-600: "#4A4F5E"          # proposed: secondary text on light (7.6:1 on paper-50)
  ink-text: "#0D0F14"           # primary text on light (17.9:1 on paper-50)

  # Aurora (brand doc 6.2): logo, one hero moment, Sealed, marketing CTA border glow, video/social only
  aurora-violet: "#7C6CFF"
  aurora-blue: "#4F8CFF"
  aurora-teal: "#2FE6D1"

  # Accent (brand doc 6.3): interactive elements only
  accent-light: "#2F5BEA"       # links, primary button fill, focus ring on light (5.1:1 on paper-50)
  on-accent-light: "#FFFFFF"    # 5.5:1 on accent-light
  accent-light-hover: "#2449C9" # proposed: hover/pressed fill on light (white text 7.3:1)
  accent-dark: "#6F8DFF"        # links, primary button fill, focus ring on dark (6.6:1 on ink-950)
  on-accent-dark: "#07080C"     # proposed: ink-950 text on accent-dark (6.6:1). White fails at 3.0:1.
  accent-dark-hover: "#8AA2FF"  # proposed: hover fill on dark (ink-950 text 8.3:1)

  # Verdicts and states (brand doc 6.3). Text on fill, light then dark.
  verified-text-light: "#0F766E"
  verified-bg-light: "#E6F6F3"   # 4.9:1
  verified-text-dark: "#34E3C9"
  verified-bg-dark: "#0F2A28"    # 9.4:1
  mismatch-text-light: "#C8322B"
  mismatch-bg-light: "#FDECEA"   # 4.65:1
  mismatch-text-dark: "#FF7A70"
  mismatch-bg-dark: "#3A1512"    # 6.4:1
  denied-light: "#C8322B"        # outline chip only, never filled
  denied-dark: "#FF7A70"         # outline chip only, never filled
  pending-text-light: "#5B4BDB"
  pending-bg-light: "#EFEDFF"    # 5.25:1
  pending-text-dark: "#A99CFF"
  pending-bg-dark: "#211C45"     # 6.75:1
  unverifiable-text-light: "#9A5B00"
  unverifiable-bg-light: "#FFF4E0" # 5.0:1
  unverifiable-text-dark: "#F7B955"
  unverifiable-bg-dark: "#352407"  # 8.55:1
  allowed-text-light: "#3F4453"
  allowed-bg-light: "#EDEEF2"    # 8.4:1, neutral on purpose
  allowed-text-dark: "#C9CCD6"
  allowed-bg-dark: "#1C1F2A"     # 10.2:1
  danger-light: "#C8322B"        # Stop, Stop all, delete (white text 5.3:1)
  danger-dark: "#FF7A70"         # Stop, Stop all, delete (ink-950 text 7.9:1)

  # Glass (brand doc 9)
  glass-dark: "rgba(14,16,23,.72)"
  glass-light: "rgba(255,255,255,.72)"
  glass-highlight: "rgba(255,255,255,.06)"

  # CLI and logo frost (brand doc 5, 13.3)
  frost-dark: "#E6EAF5"          # flat logo right half on dark, at ~85%
  frost-light: "#D9DEEA"         # flat logo right half on light
  cli-frost: "#C9CCD6"           # inline mark right half, dark terminals
  cli-frost-light: "#9AA3B8"     # inline mark right half, light terminals

  # Code syntax on ink-900 (brand doc silent on syntax colours)
  code-keyword: "#6F8DFF"        # proposed: reuses accent-dark
  code-string: "#9FD8CE"         # proposed: desaturated, distinct from verified teal (11.9:1)
  code-number: "#E0C48A"         # proposed: desaturated, distinct from unverifiable amber (11.3:1)
  code-comment: "#8B90A0"        # proposed: slate-400 (6.0:1)
  code-plain: "#F5F6FA"
  code-punct: "#B9BDC8"          # proposed: slate-300 (10.1:1)

typography:
  # Sizes reuse site/src/styles/global.css; weights and tracking follow brand doc 7.
  display-xl:
    fontFamily: Inter Variable
    fontSize: "clamp(48px, 4vw + 32px, 80px)"
    fontWeight: 650
    lineHeight: 1.05
    letterSpacing: -0.045em
  display-m:
    fontFamily: Inter Variable
    fontSize: "clamp(32px, 2vw + 24px, 48px)"
    fontWeight: 600
    lineHeight: 1.08
    letterSpacing: -0.03em        # proposed within brand range -0.02 to -0.045em
  title-2:
    fontFamily: Inter Variable
    fontSize: 28px
    fontWeight: 600
    lineHeight: 34px
    letterSpacing: -0.02em
  title-3:
    fontFamily: Inter Variable
    fontSize: 22px
    fontWeight: 600
    lineHeight: 28px
    letterSpacing: -0.015em       # proposed
  subhead-l:
    fontFamily: Inter Variable
    fontSize: 21px
    fontWeight: 400
    lineHeight: 30px
    letterSpacing: -0.01em        # proposed
  headline:
    fontFamily: Inter Variable
    fontSize: 17px
    fontWeight: 600
    lineHeight: 24px
    letterSpacing: 0
  body:
    fontFamily: Inter Variable
    fontSize: 17px
    fontWeight: 400
    lineHeight: 25px
    letterSpacing: 0
  body-console:
    fontFamily: Inter Variable
    fontSize: 14px                # brand doc 8: Console text 13 to 14px
    fontWeight: 430               # proposed within brand body range 400 to 450
    lineHeight: 20px              # proposed
    letterSpacing: 0
  body-console-s:
    fontFamily: Inter Variable
    fontSize: 13px
    fontWeight: 430               # proposed
    lineHeight: 18px              # proposed
    letterSpacing: 0
  footnote:
    fontFamily: Inter Variable
    fontSize: 13px
    fontWeight: 400
    lineHeight: 18px
    letterSpacing: 0.005em
  caption:
    fontFamily: Inter Variable
    fontSize: 12px
    fontWeight: 450
    lineHeight: 16px
    letterSpacing: 0
  chip:
    fontFamily: Inter Variable
    fontSize: 12px
    fontWeight: 600
    lineHeight: 16px
    letterSpacing: 0.04em
    fontVariant: small-caps        # brand doc 12: verdict chip in small caps
  button:
    fontFamily: Inter Variable
    fontSize: 14px                 # proposed (Console); website buttons use headline 17px
    fontWeight: 550                # proposed
    lineHeight: 20px
    letterSpacing: 0
  wordmark:
    fontFamily: Inter Variable
    fontWeight: 600
    letterSpacing: -0.04em
    textTransform: lowercase
  mono-s:
    fontFamily: Geist Mono
    fontSize: 13px
    fontWeight: 400
    lineHeight: 19px
    letterSpacing: 0
  mono-xs:
    fontFamily: Geist Mono
    fontSize: 12px                 # proposed: ids inside 36px table rows
    fontWeight: 400
    lineHeight: 16px
    letterSpacing: 0
  mono-video:
    fontFamily: Geist Mono
    fontSize: 19px                 # brand doc 13.6: 18 to 20px in video terminals
    fontWeight: 400
    lineHeight: 28px
    letterSpacing: 0
  og-title:
    fontFamily: Inter Variable
    fontSize: 64px
    fontWeight: 600
    lineHeight: 1.05
    letterSpacing: -0.035em        # proposed

spacing:
  base: 4px
  xxs: 4px
  xs: 8px
  sm: 12px
  md: 16px
  lg: 24px                         # Console section gap (brand doc 8)
  xl: 32px
  xxl: 48px
  xxxl: 64px                       # proposed
  section: 96px                    # proposed: website section rhythm
  section-lg: 128px                # proposed: hero to first section
  row-console: 36px                # brand doc 8
  sidebar-console: 248px           # brand doc 8
  content-marketing: 1120px
  content-docs: 760px
  content-console-max: 1440px
  email-width: 600px

rounded:
  chip: 6px
  input: 6px
  button: 10px
  card-sm: 10px
  card: 14px
  sheet: 20px
  hero: 28px
  app-icon: 28px                   # continuous corner squircle
  pill: 9999px                     # proposed: status pills and avatars only
  full: 50%

elevation:
  flat: "none"
  hairline-dark: "inset 0 0 0 1px #1C1F2A"
  hairline-light: "inset 0 0 0 1px #E4E5EA"
  glass: "inset 0 1px 0 rgba(255,255,255,.06), 0 0 0 1px var(--hairline)"
  glass-blur: "blur(20px) saturate(140%)"
  overlay-dark: "0 12px 32px rgba(0,0,0,.36)"     # proposed: popovers, palette, sheets on dark
  overlay-light: "0 12px 32px rgba(13,15,20,.10)" # proposed: popovers, palette, sheets on light
  aurora-glow: "blur(40px) ellipse, aurora stops, opacity .30 to .40, behind hero marks only"
  focus-ring: "0 0 0 2px var(--bg), 0 0 0 4px var(--accent)"  # 2px ring, 2px offset

motion:
  duration-micro: 120ms
  duration-component: 180ms
  duration-sheet: 240ms
  duration-reveal: 400ms
  ease-enter: "cubic-bezier(.2,.8,.2,1)"
  ease-exit: "cubic-bezier(.4,0,1,1)"
  sealed-close: "240ms ease-enter, halves slide together"
  sealed-pulse: "400ms, single teal ripple at 20% opacity"
  sealed-cli: "300ms, TTY only, off with DEEDGLASS_NO_ANIM=1"
  mismatch: "halves stop short, hairline red gap, no shake"
  toast-dwell: 5000ms              # proposed: info and success only; errors stay until dismissed
  reduced-motion: "instant state change plus a colour fade (120ms opacity/colour only)"

components:
  button-primary:
    backgroundColor: "{colors.accent-light} | {colors.accent-dark}"
    textColor: "{colors.on-accent-light} | {colors.on-accent-dark}"
    typography: "{typography.button}"
    rounded: "{rounded.button}"
    height: 32px                   # proposed: Console default; 44px on website
    padding: 0 14px
  button-primary-hover:
    backgroundColor: "{colors.accent-light-hover} | {colors.accent-dark-hover}"
  button-secondary:
    backgroundColor: "{colors.white} | {colors.ink-900}"
    textColor: "{colors.ink-text} | {colors.snow-text}"
    border: "1px {colors.hairline-light} | 1px {colors.ink-800}"
    rounded: "{rounded.button}"
    height: 32px
    padding: 0 14px
  button-ghost:
    backgroundColor: transparent
    textColor: "{colors.ink-text} | {colors.snow-text}"
    hoverBackground: "{colors.mist-100} | {colors.ink-850}"
    rounded: "{rounded.button}"
    height: 32px
    padding: 0 10px
  button-danger:
    backgroundColor: "{colors.danger-light} | {colors.danger-dark}"
    textColor: "#FFFFFF | {colors.ink-950}"
    rounded: "{rounded.button}"
    height: 32px
    padding: 0 14px
  button-danger-outline:
    backgroundColor: transparent
    textColor: "{colors.danger-light} | {colors.danger-dark}"
    border: "1px currentColor"
    rounded: "{rounded.button}"
    height: 32px
    padding: 0 12px
  button-marketing-cta:
    backgroundColor: "{colors.ink-900}"
    textColor: "{colors.snow-text}"
    border: "1px aurora gradient (135deg) with soft outer glow"
    typography: "{typography.headline}"
    rounded: "{rounded.button}"
    height: 44px
    padding: 0 20px
  text-input:
    backgroundColor: "{colors.white} | {colors.ink-900}"
    textColor: "{colors.ink-text} | {colors.snow-text}"
    placeholderColor: "{colors.slate-500} | {colors.slate-450}"
    border: "1px {colors.slate-300} | 1px {colors.ink-700}"
    typography: "{typography.body-console}"
    rounded: "{rounded.input}"
    height: 32px
    padding: 0 10px
  table-row:
    backgroundColor: transparent
    hoverBackground: "{colors.mist-100} | {colors.ink-850}"
    selectedBackground: "accent at 8% over surface"  # proposed
    divider: "1px {colors.hairline-light} | 1px {colors.ink-800}"
    typography: "{typography.body-console-s}"
    height: "{spacing.row-console}"
    padding: 0 12px
  table-header:
    textColor: "{colors.slate-500} | {colors.slate-400}"
    typography: "{typography.caption}"
    height: 32px                   # proposed
  deed-card:
    backgroundColor: "{colors.white} | {colors.ink-900}"
    border: "1px {colors.hairline-light} | 1px {colors.ink-800}"
    rounded: "{rounded.card}"
    padding: 20px                  # proposed
  verdict-badge:
    typography: "{typography.chip}"
    rounded: "{rounded.chip}"
    height: 22px                   # proposed
    padding: 0 8px
    layout: "[icon 14px] L2 · VERIFIED"
  status-pill:
    typography: "{typography.caption}"
    rounded: "{rounded.pill}"
    height: 20px                   # proposed
    padding: 0 8px
    layout: "[6px dot] Active"
  command-palette:
    backgroundColor: "{colors.glass-light} | {colors.glass-dark}"
    backdropFilter: "{elevation.glass-blur}"
    shadow: "{elevation.glass}, {elevation.overlay-*}"
    rounded: "{rounded.sheet}"
    width: 640px                   # proposed
    rowHeight: 36px
  toast:
    backgroundColor: "{colors.glass-light} | {colors.glass-dark}"
    backdropFilter: "{elevation.glass-blur}"
    rounded: "{rounded.card-sm}"
    width: 360px                   # proposed
    padding: 12px 14px
  empty-state:
    textColor: "{colors.ink-text} | {colors.snow-text}"
    typography: "{typography.headline} + {typography.body-console}"
    maxWidth: 420px                # proposed
    padding: 48px 24px
  code-block:
    backgroundColor: "{colors.ink-900}"      # both themes (brand doc 13.2)
    textColor: "{colors.code-plain}"
    border: "1px {colors.ink-800}"
    typography: "{typography.mono-s}"
    rounded: "{rounded.button}"
    padding: 16px
  top-bar-console:
    backgroundColor: "{colors.paper-50} | {colors.ink-950}; glass on scroll"
    height: 52px                   # proposed
  sidebar-console:
    backgroundColor: "{colors.paper-50} | {colors.ink-950}"
    width: "{spacing.sidebar-console}"
    itemHeight: 32px               # proposed
  email-layout:
    width: "{spacing.email-width}"
    backgroundColor: "{colors.paper-50}"
    cardBackground: "{colors.white}"
    structure: "nested <table role=presentation>, inline styles"
  cli-mark:
    glyphs: "◖│◗"
    left: "{colors.aurora-blue}"
    pane: "#FFFFFF (foreground on light terminals)"
    right: "{colors.cli-frost} | {colors.cli-frost-light}"
    asciiFallback: "(|)"
---

# Deedglass Design System

> Derived from DEEDGLASS_BRAND.md (binding). If they disagree, DEEDGLASS_BRAND.md wins; fix this file.

Values marked `# proposed` in the front matter are not in the brand doc. They were chosen to fit it and may change without a DECISION_LOG entry. Everything else is quoted from the brand doc and changes only through one.

## Overview

Deedglass sits between AI agents and the tools they use. It decides what each agent may do, runs it under control, and records what actually happened as a signed **Deed**. The design has one job: let a careful engineer see exactly what happened, at a glance, and trust the layout enough to stop reading once they have the answer.

The system reads as **quiet, exact and expensive-looking**. About 90% of every screen is neutral. Dark mode sits on `{colors.ink-950}` (#07080C), an ink with a faint blue cast, never pure black. Light mode sits on `{colors.paper-50}` (#F7F7F4), warm paper, never pure white. Cards are one step up (`{colors.ink-900}` or `{colors.white}`) with a 1px hairline. There are no heavy shadows.

Colour is spent in three tight budgets:

1. **Accent** (`{colors.accent-light}` #2F5BEA / `{colors.accent-dark}` #6F8DFF): interactive elements only. Links, primary buttons, focus rings.
2. **Verdict and state colours**: VERIFIED teal, MISMATCH red, UNVERIFIABLE amber, pending violet, allowed neutral. Each always paired with an icon and a word.
3. **Aurora** (#7C6CFF to #4F8CFF to #2FE6D1, 135deg): the brand signature. Logo, one hero moment per page, the Sealed animation, the marketing CTA border glow, video and social backgrounds. Nowhere else.

Type is **Inter Variable** for everything and **Geist Mono** for ids, hashes, code, SQL, policies and CLI samples. Numbers that line up use tabular figures.

**Glass** is a material for floating layers (top bar on scroll, sheets, command palette, toasts). Base surfaces are opaque.

**Key characteristics:**
- Dark mode is the Console default; light mode is fully supported and equally tokenised.
- One accent hue, one signature gradient, one set of verdict colours. Nothing decorative beyond that.
- Hairlines, not shadows. Surface steps, not drop shadows.
- Mono for every machine value: `deed_01j9…`, `sha256:3f9a…c21e`, `agt_01j9…`.
- Calm motion with one signature moment: **Sealed**.
- Every screen has loading, empty (that teaches), error and permission-denied states.

**Taste dials** (for Stitch-style generators): Density 6 in the Console ("compact but calm"), 3 on the website ("generous"). Variance 3 (precise, mostly symmetric, product screenshots lead). Motion 3 (restrained, one signature moment). The brand doc overrides generic taste rules: Inter is mandated, and the violet-to-teal aurora is the brand signature, used only where listed.

## Colors

### Neutrals: dark ramp
- **Ink 950** ({colors.ink-950}): dark canvas. Site hero, Console dark, video.
- **Ink 900** ({colors.ink-900}): cards, panels, code blocks in both themes.
- **Ink 850** ({colors.ink-850}): raised surface, table row hover, active nav item.
- **Ink 800** ({colors.ink-800}): every 1px hairline on dark.
- **Ink 700** ({colors.ink-700}): strong borders, input borders, disabled fills.
- **Snow text** ({colors.snow-text}): primary text on dark.
- **Slate 400** ({colors.slate-400}): secondary text on dark (labels, meta, timestamps).
- **Slate 450** ({colors.slate-450}, proposed): tertiary text on dark. Do not use `slate-500` on dark; it measures 4.06:1 on ink-950 and fails AA.

### Neutrals: light ramp
- **Paper 50** ({colors.paper-50}): light canvas, reading sections of the website, email background.
- **White** ({colors.white}): cards and inputs on light.
- **Mist 100** ({colors.mist-100}): alternate surface, row hover, allowed-chip fill.
- **Hairline light** ({colors.hairline-light}): every 1px hairline on light.
- **Slate 300** ({colors.slate-300}): input borders, strong dividers on light.
- **Ink text** ({colors.ink-text}): primary text on light.
- **Slate 600** ({colors.slate-600}, proposed): secondary text on light.
- **Slate 500** ({colors.slate-500}): tertiary text on paper-50 or white only (4.6:1). It fails on mist-100 (4.26:1); use slate-600 there.

### Accent (interactive only)
| Role | Light | Dark |
|---|---|---|
| Accent fill, link, focus ring | `#2F5BEA` | `#6F8DFF` |
| Text on accent | `#FFFFFF` (5.5:1) | `#07080C` (6.6:1) |
| Hover / pressed (proposed) | `#2449C9` | `#8AA2FF` |

White text on `#6F8DFF` measures 3.0:1 and fails. Dark-mode primary buttons therefore use ink-950 text.

### Verdicts and states
| State | Light (text on fill) | Dark (text on fill) | Used for |
|---|---|---|---|
| **VERIFIED** | `#0F766E` on `#E6F6F3` | `#34E3C9` on `#0F2A28` | L0/L1/L2 VERIFIED chips; the seal pulse |
| **MISMATCH** | `#C8322B` on `#FDECEA` | `#FF7A70` on `#3A1512` | MISMATCH only |
| **Denied** | `#C8322B` outline | `#FF7A70` outline | Policy deny. Outline, never filled, so it reads differently from a mismatch |
| **Pending / approval** | `#5B4BDB` on `#EFEDFF` | `#A99CFF` on `#211C45` | Waiting for a human to approve or countersign |
| **UNVERIFIABLE** | `#9A5B00` on `#FFF4E0` | `#F7B955` on `#352407` | Evidence gap |
| **Allowed / executed** | `#3F4453` on `#EDEEF2` | `#C9CCD6` on `#1C1F2A` | Neutral, on purpose |
| **Stop / danger** | `#C8322B` | `#FF7A70` | Stop agent, Stop all agents, delete |

Rules:
- Colour never carries meaning alone. Every chip has an icon and a word.
- Red is reserved for deny, mismatch and destructive actions. Never for decoration, never for "new".
- Diffs highlight changed cells with the state colour at 12% opacity.

### Verification levels
| Level | Label | Meaning |
|---|---|---|
| L0 | **Reported** | The tool's own response |
| L1 | **Confirmed** | A read-back from the target |
| L2 | **Witnessed** | The system of record (the Postgres change log) |

The level is printed in the chip before the verdict: `L2 · VERIFIED`. Levels carry no colour of their own; the verdict owns the colour.

### Aurora (signature, scarce)
- Stops: `#7C6CFF` → `#4F8CFF` → `#2FE6D1`, linear at 135deg.
- **Allowed only in:** the logo; one hero moment per page; the Sealed animation; the primary marketing CTA as a 1px border glow (not a fill); video and social backgrounds.
- **Aurora glow:** a 40px-blurred aurora ellipse at 30 to 40% opacity, behind hero marks only.
- **Never in:** UI chrome, nav, sidebars, tables, charts, badges, body text, gradient headline text, button fills.

### Contrast
WCAG 2.2 AA (4.5:1) for every text pair, checked by script in the token build (S24). Ratios quoted in the front matter were measured with the WCAG relative-luminance formula. Large text (24px+, or 18.66px+ at 600) may use 3:1, but no token in this file relies on that.

## Typography

### Font family
- **Inter Variable**, self-hosted, with `'Inter Fallback', ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto` behind it. Everything: display, UI, body, buttons, chips, the wordmark.
- **Geist Mono**, with `ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`. Ids, hashes, code, SQL, policies, CLI samples, request ids.
- `font-variant-numeric: tabular-nums` wherever numbers line up: tables, metrics, timestamps, counts, money.

### Hierarchy
| Token | Size | Weight | Line height | Tracking | Use |
|---|---|---|---|---|---|
| `{typography.display-xl}` | 48 to 80px (clamp) | 650 | 1.05 | -0.045em | Website hero headline |
| `{typography.display-m}` | 32 to 48px (clamp) | 600 | 1.08 | -0.03em | Section openers |
| `{typography.title-2}` | 28px | 600 | 34px | -0.02em | Page titles in Console, docs H1 |
| `{typography.title-3}` | 22px | 600 | 28px | -0.015em | Card titles, docs H2 |
| `{typography.subhead-l}` | 21px | 400 | 30px | -0.01em | Hero lead paragraph |
| `{typography.headline}` | 17px | 600 | 24px | 0 | Emphasised body, website buttons |
| `{typography.body}` | 17px | 400 | 25px | 0 | Website and docs body |
| `{typography.body-console}` | 14px | 430 | 20px | 0 | Console default text |
| `{typography.body-console-s}` | 13px | 430 | 18px | 0 | Table cells, dense lists |
| `{typography.footnote}` | 13px | 400 | 18px | 0.005em | Footnotes, legal, captions on site |
| `{typography.caption}` | 12px | 450 | 16px | 0 | Console meta, table headers |
| `{typography.chip}` | 12px | 600 | 16px | +0.04em, small caps | Verdict chips |
| `{typography.mono-s}` | 13px | 400 | 19px | 0 | Code blocks, ids in body |
| `{typography.mono-xs}` | 12px | 400 | 16px | 0 | Ids in table rows |

### Principles
- Display: weights 600 to 650, tracking -0.02 to -0.045em. Tighter as it grows.
- Body: weights 400 to 450, normal tracking. Hierarchy through weight and colour before size.
- Website body line length: about 65 characters; docs column 760px.
- Machine values are mono, human words are Inter. Never set a sentence in mono to look technical.
- The wordmark is always lowercase `deedglass`, Inter Semibold, tracking -0.04em.

## Layout

### Spacing
- 4pt base grid; 8pt for layout.
- Tokens: `{spacing.xxs}` 4 · `{spacing.xs}` 8 · `{spacing.sm}` 12 · `{spacing.md}` 16 · `{spacing.lg}` 24 · `{spacing.xl}` 32 · `{spacing.xxl}` 48 · `{spacing.xxxl}` 64 · `{spacing.section}` 96.
- Console: 36px table rows, 13 to 14px text, 24px section gaps, 16 to 20px card padding.
- Website: generous. 96px between sections, 128px after the hero, one idea per section.

### Grid and containers
| Surface | Width | Notes |
|---|---|---|
| Marketing | 1120px content | 12-col grid, 24px gutters (proposed) |
| Docs | 760px text column | Sidebar nav left, on-page TOC right at 1440px+ |
| Console | Fluid, 1440px max | 248px sidebar, content padding 24px (proposed) |
| Email | 600px | Single column, table-based |

### Whitespace philosophy
The dark canvas and the paper canvas are the whitespace. Separate sections with space and a surface step, not with boxes, dividers and colour blocks. In the Console, density comes from tight rows, not from cramming panels together.

## Elevation & Depth

| Level | Treatment | Use |
|---|---|---|
| 0 Flat | Canvas, no border | Page background, hero text |
| 1 Surface | `ink-900` / `white` + 1px hairline | Cards, Deed cards, tables, code blocks |
| 2 Raised | `ink-850` / `mist-100` | Hover rows, active nav, nested panels |
| 3 Glass | `{colors.glass-dark}` / `{colors.glass-light}`, `blur(20px) saturate(140%)`, inner top highlight `inset 0 1px 0 rgba(255,255,255,.06)`, hairline border, `{elevation.overlay-*}` (proposed) | Top bar on scroll, sheets, command palette, toasts, popovers |
| Focus | 2px accent ring, 2px offset | Every focusable element |

**Shadow philosophy.** Depth comes from surface steps and hairlines. The only shadow in the system is the soft overlay under glass layers (proposed values in front matter). Never stack shadows, never shadow buttons or chips, never put a shadow on the logo.

**Glass fallback.** Where `backdrop-filter` is unsupported, or under `prefers-reduced-transparency`, glass becomes the opaque surface (`ink-900` / `white`).

### Decorative depth
- Aurora glow behind hero marks only (40px blur, 30 to 40% opacity).
- Real product screenshots and real terminal recordings carry the website. No mock "AI" illustrations.

## Shapes

### Radius scale
| Token | Value | Use |
|---|---|---|
| `{rounded.chip}` | 6px | Verdict chips, inputs, tags, kbd hints |
| `{rounded.button}` | 10px | Buttons, small cards, code blocks, toasts |
| `{rounded.card}` | 14px | Cards, Deed cards, tables in a frame |
| `{rounded.sheet}` | 20px | Sheets, large panels, command palette, modals |
| `{rounded.hero}` | 28px | Hero cards, the app icon squircle |
| `{rounded.pill}` | 9999px | Status pills and avatars only (proposed) |

Nested radii: inner radius = outer radius minus padding, floor 6px.

### Logo: the Split Orb
One perfectly round glass orb cut vertically into two half-discs, separated by a narrow gap. A free-standing pane of white light stands in the gap.
- **Left half:** aurora glass, violet to blue to teal top to bottom. What the agent claimed.
- **Right half:** clear glass, faintly refracting the left. What actually happened.
- **The pane:** the glass you see through. When claim and reality match, the orb closes and the deed is sealed.

**Renditions:**
1. **Glass**: hero, app icon, video, social. Soft specular highlights, gentle refraction, faint aurora bloom beneath.
2. **Flat** (favicon, CLI, docs, 48px and below): left half solid aurora gradient; right half solid frost (`#E6EAF5` at about 85% on dark, `#D9DEEA` on light), **no outline**. Seam is a 4 to 5% gap. At 16px the seam may close to a 1px gap.
3. **Mono**: single colour, right half at 40% tint.

**Construction:** perfect circle, seam exactly centred. Glass: each gap about 1.2% of diameter, pane about 1%. Flat: gaps and pane each about 3% (at least 1px at 32px).

**Clear space:** at least half the orb's radius on every side. **Minimum size:** 16px (flat).

**Lockups:** horizontal (orb + `deedglass`, x-height centred on the orb), stacked, mark only, app icon (orb at about 50% width on an `ink-900` continuous-corner squircle with a soft rim highlight and top specular edge).

**Never:** an outline-only right half; recolour, rotate or split horizontally; eyes, pyramids, rays, compasses, crowns, Latin mottos; busy photo backgrounds; heavy shadows.

### Iconography
- Lucide, 1.5px stroke, 20px grid, rounded joins. 16px inside chips and dense rows (proposed).
- Custom icons in the same style: **Deed** (folded-corner page with a small seal), **Witness** (half-circle facing a line), **Gate** (an arch), **Stop** (a rounded square).
- No emoji in product UI.

## Components

### Buttons
All buttons: `{typography.button}`, `{rounded.button}`, 32px tall in the Console (28px compact, 44px on website and touch; proposed). Icon + label gap 6px. Pressed state: `transform: scale(.98)` over 120ms (proposed). Disabled: `ink-700` / `mist-100` fill, tertiary text, no hover.

**`button-primary`**: accent fill. One per view. Light: white on `#2F5BEA`. Dark: ink-950 on `#6F8DFF`. Labels are verbs: `Connect agent`, `Approve`, `Create policy`.

**`button-secondary`**: surface fill with hairline border. The default for most actions: `Export`, `Copy deed`, `View in Ledger`.

**`button-ghost`**: no fill until hover. Toolbars, table row actions, "Cancel".

**`button-danger`**: red fill. Destructive confirmations only: `Stop agent`, `Delete key`. Always behind a typed confirmation for Stop.

**`button-danger-outline`**: red text and 1px red border on transparent. Used by **Stop all agents** in the Console top bar, always visible.

**`button-marketing-cta`**: website only, one per page. `ink-900` fill, snow text, a 1px aurora border with a soft glow. The aurora is the border, never the fill. Label: `Start free` or `Read the docs`.

### Input
**`text-input`**: 32px Console (44px website forms), `{rounded.input}` 6px, 1px `slate-300` / `ink-700` border, `{typography.body-console}`.
- Label above (caption, secondary text). Helper text below in tertiary. Error text below in mismatch red with an icon, plus `aria-describedby`.
- Focus: border becomes accent, plus the 2px ring with 2px offset.
- Mono inputs for ids, keys and SQL (`{typography.mono-s}`).
- Never use floating labels. Never rely on placeholder as a label.

### Table and row
**`table-header`**: caption size, secondary text, sentence case, sticky under the top bar. Sort indicator is a 12px chevron.

**`table-row`**: 36px tall, transparent, 1px hairline divider below, 12px horizontal cell padding.
- Hover: `ink-850` / `mist-100`. Selected: accent at 8% over the surface plus a 2px accent bar on the leading edge (proposed).
- Ids in `{typography.mono-xs}`, truncated (`agt_01j9…`), full value on hover with a copy button.
- Hashes as `sha256:3f9a…c21e` (first 4 + last 4).
- Time as relative (`2m ago`) with an absolute tooltip in the org's time zone.
- Numbers right-aligned, tabular.
- The verdict column holds a `verdict-badge`; nothing else in the row is coloured.
- Live feeds announce new rows through an ARIA live region (polite). A new VERIFIED row may play the subtle seal pulse once.

### Deed card
**`deed-card`**: the canonical view of one Deed. `ink-900` / `white`, hairline border, `{rounded.card}`, 20px padding.

Anatomy, top to bottom:
1. Header row: Deed icon, `deed_01j9…` in mono with copy, and the `verdict-badge` aligned right.
2. Title: the action in plain words, `{typography.headline}`. Example: `UPDATE public.orders by agent billing-sync`.
3. Meta grid (2 columns, caption labels, mono values): Agent, Connector, Policy, Gate decision, Sealed at, Seal key, Chain position.
4. Verdict sentence in body text, exact and numeric: `MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.`
5. Diff (when present): expected vs observed in two aligned mono columns; changed cells tinted with the state colour at 12%.
6. Footer: `Sealed · sha256:3f9a…c21e`, `Countersigned by Priya` when approved, and secondary actions (`Copy deed`, `Verify offline`, `View in Ledger`).

The Split Orb at 20px may sit beside "Sealed" and runs the Sealed animation on the detail page.

### Verdict badge
**`verdict-badge`**: `[icon] L2 · VERIFIED`. `{typography.chip}` (12px, 600, +0.04em, small caps), `{rounded.chip}` 6px, 22px tall, 8px side padding, 14px icon.

| Verdict | Icon (Lucide, proposed) | CLI glyph | Fill |
|---|---|---|---|
| VERIFIED | `circle-check` | `✓` | verified pair |
| MISMATCH | `circle-x` | `✕` | mismatch pair |
| UNVERIFIABLE | `circle-dashed` | `○` | unverifiable pair |
| Denied | `ban` | `✕` | transparent, 1px red border, red text |
| Pending | `circle-dot-dashed` | `◐` | pending pair |
| Allowed | `circle` | `·` | allowed pair |

The separator is a middle dot `·` with a thin space on each side. Never abbreviate the verdict word.

### Status pill
**`status-pill`**: lifecycle status of an Agent, Connector or key (not a verdict). 20px tall, `{rounded.pill}`, caption type, a 6px dot plus the word. Examples: `Active` (verified dot), `Stopped` (danger dot), `Paused` (unverifiable dot), `Revoked` (neutral dot). Fill is the matching state background; text stays primary neutral for readability.

### Command palette
**`command-palette`**: opened with ⌘K / Ctrl K. Glass layer, `{rounded.sheet}`, 640px wide, centred at 20vh from the top (proposed). Input row 48px with a search icon; results in 36px rows grouped by caption headers (Go to, Agents, Deeds, Actions). Selected row: `ink-850` / `mist-100` with accent leading bar. Shortcuts shown right-aligned in 6px-radius kbd chips. Enters with opacity and 4px rise over 180ms; exits at 120ms. Search accepts ids (`deed_…`, `agt_…`) and jumps directly. **Stop all agents** is reachable here but still requires the typed confirmation.

### Toast
**`toast`**: glass layer, `{rounded.card-sm}` 10px, 360px wide, bottom-right on desktop, bottom-centre on mobile, stacked with 8px gaps, max 3 visible. Leading state icon, one sentence, optional single action. Info and success dismiss after 5s (proposed); errors stay until dismissed. `role="status"` for info, `role="alert"` for errors. Copy is calm and exact: `Deed sealed. deed_01j9…c21e copied.` Never `Success` alone, never an exclamation mark.

### Empty state
**`empty-state`**: centred in the content area, max 420px. A 20px Lucide or custom icon in a 40px `ink-850` / `mist-100` circle, a headline, one teaching sentence, one primary action, one secondary link to docs.
- `No agents yet. Connect your first agent in about two minutes.` [Connect agent] · Read the quickstart
- `No deeds match these filters.` [Clear filters]
- `No approvals waiting. Risky actions will appear here for a human to countersign.`

No illustrations of robots, no "nothing to see here".

### Code block
**`code-block`**: always `ink-900` with an `ink-800` hairline, in both themes. `{typography.mono-s}`, 16px padding, `{rounded.button}`. Header strip (32px) with the language or filename in caption type and a copy button. Syntax colours are muted and proposed (see front matter); verdict colours are never used for syntax. Terminal samples show the prompt `$` in slate-400 and output exactly as the CLI prints it.

```
$ deedglass verify deeds.jsonl --keys https://deedglass.com/.well-known/deedglass/keys.json
✓ 1,204 deeds · chain intact · 2 seal keys · 2026-10-01 → 2026-10-02
```

### Email layout
**`email-layout`**: plain and fast.
- 600px wide, nested `<table role="presentation">` with inline styles. No flexbox, no grid, no web fonts required.
- `paper-50` outer background, `white` 600px card with a 1px `#E4E5EA` border and 14px radius (square corners degrade gracefully in clients that ignore radius), 32px padding (proposed).
- Header: the black `deedglass` wordmark (PNG at 2x with a transparent background, 24px tall, proposed). The only image allowed.
- One accent link or one table-cell button (`#2F5BEA` fill, white text, 10px radius, 44px tall).
- Font stack: `Inter, -apple-system, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif`; mono values in `'Geist Mono', Menlo, Consolas, monospace`.
- Always ship a text-only alternative part.
- **Dark-safe** (proposed): declare `<meta name="color-scheme" content="light dark">` and `supported-color-schemes`; add a `prefers-color-scheme: dark` block that swaps to `ink-950` / `ink-900` / `snow-text` and a light wordmark where supported. Avoid pure `#000` and `#FFF` text so forced inversion (Gmail, Outlook) stays legible. Give the wordmark PNG a faint `paper-50` halo so it survives inversion.
- Copy example: subject `Approval waiting: DELETE on public.users`; body `billing-sync wants to run DELETE on public.users (14 rows). Policy "require-approval-destructive" needs a human. Approve or deny in the Console.`

### CLI mark
**`cli-mark`**: `◖│◗`. Left half in aurora blue `#4F8CFF`, pane in white, right half in frost `#C9CCD6`, followed by **deedglass** (bold) and the command name. It heads `init`, `doctor`, `help` and long-running commands. ASCII fallback: `(|)`. Light terminals: right half `#9AA3B8`, pane uses the foreground colour.

```
◖│◗ deedglass doctor
✓ Gate reachable · 42 ms
✓ Seal key active · key_01j9…7a2c
○ Witness not configured for connector pg-main · run deedglass witness add pg-main
```

## Motion

| Token | Value | Use |
|---|---|---|
| `{motion.duration-micro}` | 120ms | Hover, press, checkbox, colour change |
| `{motion.duration-component}` | 180ms | Popovers, palette, toasts, chips |
| `{motion.duration-sheet}` | 240ms | Sheets, side panels, Sealed close |
| `{motion.duration-reveal}` | 400ms | Page reveal, Sealed pulse |
| `{motion.ease-enter}` | `cubic-bezier(.2,.8,.2,1)` | Everything entering |
| `{motion.ease-exit}` | `cubic-bezier(.4,0,1,1)` | Everything leaving |

**Sealed (the signature moment).** When a deed is sealed with a VERIFIED verdict, the two halves of the Split Orb slide together (240ms, enter easing) and a single soft teal pulse ripples out (400ms, 20% opacity). Used in the Console action detail, the live feed (subtle, once per row), the website demo and videos. A MISMATCH shows the halves stopping short with a hairline red gap. No shaking, ever.

**Rules:**
- Animate only `transform` and `opacity` (and colour for state changes).
- No perpetual loops, no shimmer on idle data, no bouncing. Skeleton loaders may pulse opacity slowly (1.2s, proposed) and stop as soon as data arrives.
- No staggered cascades longer than 120ms total in the Console.
- Social and video outro: 1.5s. The orb closes, then the wordmark, then `deedglass.com`.

**Reduced motion.** Under `prefers-reduced-motion: reduce`, every movement becomes an instant state change plus a colour or opacity fade (120ms). Sealed shows the closed orb and the teal state directly. In the CLI, `DEEDGLASS_NO_ANIM=1` disables the 300ms close.

## Do's and Don'ts

### Do
- Keep about 90% of every screen neutral.
- Use the accent only on things you can click or focus.
- Put a word and an icon on every verdict and state.
- Show machine values in Geist Mono with copy, and truncate them the documented way.
- Use exact numbers: `Postgres committed 301 rows; the agent reported 1.`
- Write calm, second-person, plain English. Every error ends with a next step.
- Give every Console screen loading, empty, error and permission-denied states.
- Use the brand vocabulary everywhere: **Deed**, **Seal** / sealed, **Deed chain**, **Witness**, **Verdict**, **Gate**, **Policies**, **Approval** / countersign, **Ledger**, **Insights**, **Console**, **Agent**, **Connector**, **Stop**, **Org**.
- Say "inline" for in-path execution.
- Say what a feature does not do, on the page that sells it.

### Don't
- Don't put aurora in UI chrome, tables, charts, badges, button fills or body text. No gradient headline text.
- Don't make the whole UI glass. Base surfaces are opaque.
- Don't use red for anything except deny, mismatch and destructive actions.
- Don't fill a Denied chip; it is an outline, so it never reads as a MISMATCH.
- Don't use pure black or pure white canvases.
- Don't use exclamation marks or emoji in product UI.
- Don't use em-dashes in UI copy; use a full stop, colon or middle dot.
- Don't call the self-improvement layer "self-learning" or "autonomous". It is **Insights**, and humans approve every suggestion.
- Don't say "tenant" in the UI; say **Org**.
- Don't use conspiracy-coded imagery, stock photos, 3D robots, neon cyberpunk, or AI-generated "product" shots presented as real.
- Don't spell the name any way except **Deedglass** (wordmark and CLI: `deedglass`).
- Don't make claims without a source in CLAIMS_REGISTER.

### Banned words (copied from the brand doc; CI enforces `product/scripts/banned_words.txt`)
This is the only place in this file where these words appear.

- trustless, irrefutable, unbreakable, tamper-proof, guaranteed, proves, proof of execution
- receipt, compliance (as positioning), certified / certificate, attests / attestation
- patented, "synchronous" (say "inline")
- Style bans: "revolutionary", "world-class", "cutting-edge", "seamless", "next-gen", "military-grade", "bank-grade", "unhackable", "100%", "AI-powered" as a crutch
- Workspace rules (founder): alloy (the old name; always write Deedglass), "open source" (say "open core")

### Copy examples
| Surface | Say this | Not this |
|---|---|---|
| Deny message | `Blocked by policy "block-destructive-sql": DROP is never allowed for agents.` | `Oops, action not permitted` |
| Empty state | `No agents yet. Connect your first agent in about two minutes.` | `Nothing to see here` |
| Verdict | `MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.` | `Verification failed` |
| API error | `The API key has been revoked. Create a new key in Console › Agents.` | `Unauthorized` |
| Stop confirm | `Type billing-sync to stop this agent. It will finish no further actions until you start it again.` | `Are you sure?` |
| Toast | `Deed sealed. Chain intact.` | `Success` |
| Marketing | `See what your agents actually did.` | `The most advanced AI platform` |

## Responsive Behavior

### Breakpoints
| Name | Width | Key changes |
|---|---|---|
| Mobile | < 734px | Single column. Console sidebar becomes a sheet. Tables become stacked rows (id, verdict, time). Display-xl clamps to 48px. |
| Tablet | 734 to 1067px | Website 2-column sections. Console sidebar collapses to a 56px icon rail (proposed). |
| Desktop | 1068 to 1439px | Full sidebar (248px), full tables. |
| Wide | 1440px+ | Console content stops at 1440px; docs show the on-page TOC. |

Breakpoints reuse `site/src/styles/global.css` (734, 1068, 1440).

### Touch targets
- At least 24px on pointer devices, 44px on touch (WCAG 2.2 AA). Console 32px buttons grow to 44px under `pointer: coarse`.

### Collapsing strategy
- Top bar keeps the org switcher, ⌘K and **Stop all agents** at every width; account and theme move into a menu below 734px.
- Deed card meta grid goes from 2 columns to 1; the diff becomes expected-then-observed stacked, each cell labelled.
- Command palette goes full-width with 16px margins on mobile.
- No horizontal page scroll. Wide tables scroll inside their own frame with a sticky first column.

### Image behavior
- Product screenshots keep their aspect ratio and never crop the data.
- The logo never shrinks below 16px and keeps its clear space at every size.

## Surfaces

### Console (app.deedglass.com)
Dark by default, light available. Sidebar: Overview, Actions, Approvals, Agents, Policies, Connectors, Insights, Deeds, Settings. Top bar: org switcher, search / command (⌘K), **Stop all agents** (red outline), theme, account. Compact but calm: 36px rows, 13 to 14px text, 24px section gaps. No aurora except the Sealed moment on deed detail.

### Website (deedglass.com)
The hero is on `ink-950` with the Split Orb and an aurora glow, then the page moves to `paper-50` for reading sections. One idea per section, generous whitespace, 1120px content. Real product screenshots and real terminal recordings. One aurora-bordered CTA per page. Every feature page states its limits.

### Docs (docs.deedglass.com)
Starlight themed with these tokens. 760px text column, `{typography.body}`, code blocks in Geist Mono on `ink-900` in both themes. Flat logo in the header. Callouts use neutral surfaces with a state icon, not coloured slabs.

### Email
600px, table-based, `paper-50`, black wordmark, one accent link or button, no images other than the logo, a text-only alternative, and the dark-safe rules in Components.

### CLI (`deedglass`)
Colour only on a TTY; respect `NO_COLOR`. Symbols: `✓` verified (teal), `✕` mismatch or denied (red), `◐` pending (violet), `○` unverifiable (amber), `·` separators. `--json` on every command. Every error ends with a next step. Inline mark `◖│◗` heads `init`, `doctor`, `help` and long-running commands. The half-block banner shows only for bare `deedglass`, `deedglass init` and `--version`, on an interactive TTY at least 60 columns wide; never under `--json`, when piped, in CI, with `DEEDGLASS_NO_BANNER=1` or `-q`. Colour depth: true colour, then xterm-256, then 16 colours, then none.

### Social and video
`ink-950` background, aurora glow behind the mark, Inter titles, burned-in captions. Terminals in Geist Mono 18 to 20px on `ink-900`. 1.5s outro: orb closes, wordmark, `deedglass.com`. Open Graph: 1200×630, `ink-950`, mark top-left, title Inter 64/600 `snow-text`, subtitle `slate-400`, aurora glow bottom-right at 30%. Emoji allowed sparingly in posts, never in images of the product.

## Iteration Guide

1. Read DEEDGLASS_BRAND.md first. If this file disagrees with it, the brand doc wins; fix this file.
2. Work one component at a time and reference it by its `components:` token name (`deed-card`, `verdict-badge`).
3. Pick the theme first. Every colour token has a light and a dark value; never hard-code one theme's hex into a shared component.
4. Before adding colour, ask which budget it spends: accent (interactive), state (verdict), or aurora (signature list). If none, use a neutral.
5. Default Console text to `{typography.body-console}`; machine values to `{typography.mono-xs}` or `{typography.mono-s}`.
6. Write the copy in brand vocabulary, run it against the banned list, and remove exclamation marks and em-dashes.
7. Add the four states (loading, empty, error, permission-denied) before calling a screen done.
8. Run the contrast script and `npx @google/design.md lint DESIGN.md` after token edits. Lighthouse accessibility and best practices must stay at 95 or above.
9. Mark any new value not in the brand doc `# proposed`, and list it under Known Gaps.

## Known Gaps

- **Proposed values** (brand doc silent): `slate-450` tertiary text on dark; `slate-600` secondary text on light; accent hover colours; `on-accent-dark` (ink-950 text, because white fails on #6F8DFF); syntax colours; overlay shadows under glass; button, input, chip, pill and row heights other than the 36px table row; Console body weight 430; display-m, title-3, subhead-l and OG title tracking; spacing tokens above 48px; command palette, toast and empty-state widths; toast dwell time; skeleton pulse timing; tablet icon rail; Lucide icon choices for verdicts; email card padding, wordmark size and dark-safe technique; pill radius for status pills.
- **Type scale weights.** `site/src/styles/global.css` sets display-xl at weight 700 and tracking -0.015em. The brand doc asks for 600 to 650 and -0.02 to -0.045em. This file follows the brand doc; the site CSS needs updating.
- **Site colour tokens predate the brand doc.** `global.css` uses `#000000` dark background and `#1f4fd8` accent. The token build (S24) should replace them with `ink-950` and `#2F5BEA` / `#6F8DFF`.
- **Charts.** The brand doc bans aurora in charts but defines no chart palette. Until one exists, charts use neutrals plus verdict colours for verdict series only (see the dataviz skill for a validated method).
- **Final logo vectors** are pending (S36). Until then use `~/txv/site/brand/deedglass/reference/final-orb.png` and `reference/chosen-split-orb-icon.png` as reference only.
- **Light-mode code blocks** stay dark (`ink-900`) per brand doc 13.2; confirm this holds for inline code (proposed: inline code on light uses `mist-100` fill and `ink-text`).
- **Forced-colors mode** (Windows High Contrast) is not specified; chips must keep their border and icon so meaning survives.
- **Contrast script** (S24) has not run yet; ratios here were computed by hand with the WCAG formula and should be re-checked by CI.
