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
  danger-light-hover: "#A92820"  # proposed (S24): hover on filled danger, light (white text 6.97:1)
  danger-dark-hover: "#FF968E"   # proposed (S24): hover on filled danger, dark (ink-950 text 9.54:1)

  # Glass (brand doc 9)
  glass-dark: "rgba(14,16,23,.72)"
  glass-light: "rgba(255,255,255,.72)"
  glass-highlight: "rgba(255,255,255,.06)"
  scrim-dark: "rgba(7,8,12,.64)"     # proposed (S24): behind sheets, dialogs and the palette, dark
  scrim-light: "rgba(13,15,20,.32)"  # proposed (S24): the same, light

  # CLI and logo frost (brand doc 5, 13.3)
  frost-dark: "#E6EAF5"          # flat logo right half on dark, at ~85%
  frost-light: "#D9DEEA"         # Console flat mark on light; the kit draws #C3CADB (DL-057, token follow-up)
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
  # The Console's compact scale (S24 §3.3; web/tokens/tokens.json typography, in lockstep).
  console-title-2:
    fontFamily: Inter Variable
    fontSize: 24px
    fontWeight: 600
    lineHeight: 30px
    letterSpacing: -0.02em
  console-title-3:
    fontFamily: Inter Variable
    fontSize: 20px
    fontWeight: 600
    lineHeight: 26px
    letterSpacing: -0.015em
  console-headline:
    fontFamily: Inter Variable
    fontSize: 15px
    fontWeight: 600
    lineHeight: 22px
    letterSpacing: 0
  console-body:
    fontFamily: Inter Variable
    fontSize: 14px
    fontWeight: 400
    lineHeight: 20px
    letterSpacing: 0
  console-body-dense:
    fontFamily: Inter Variable
    fontSize: 13px
    fontWeight: 400
    lineHeight: 18px
    letterSpacing: 0
  console-footnote:
    fontFamily: Inter Variable
    fontSize: 13px
    fontWeight: 450
    lineHeight: 18px
    letterSpacing: 0
  console-caption:
    fontFamily: Inter Variable
    fontSize: 12px
    fontWeight: 600
    lineHeight: 16px
    letterSpacing: 0.04em
    fontVariant: small-caps
  console-metric:
    fontFamily: Inter Variable
    fontSize: 28px
    fontWeight: 600
    lineHeight: 32px
    letterSpacing: -0.02em
  console-mono-s:
    fontFamily: Geist Mono
    fontSize: 12.5px
    fontWeight: 400
    lineHeight: 18px
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
    border: "1px {colors.slate-400} | 1px {colors.slate-500}"   # S24 contrast: slate-300 on white is 1.88 (fails WCAG 1.4.11); slate-400 on white 3.18, slate-500 on ink-900 3.85; light inputs sit on white, never paper-50
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
    width: 560px                   # S24 §3.8; full screen under 640px
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
    height: 56px                   # S24 §3.3
  sidebar-console:
    backgroundColor: "{colors.paper-50} | {colors.ink-950}"
    width: "{spacing.sidebar-console}"
    itemHeight: 36px               # S24 §3.7 (rail 64px from 768 to 1023px; bottom bar 64px + safe area below 768px)
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
2. **Flat** (favicon, CLI, docs, 48px and below): left half solid aurora gradient; right half solid frost (`#E6EAF5` at about 85% on dark, `#C3CADB` on light, as the kit draws it), **no outline**. Seam is a 4 to 5% gap. At 16px the seam may close to a 1px gap.
3. **Mono**: single colour, right half at 40% tint.

**Construction:** perfect circle, seam exactly centred. Glass: each gap about 2.1% of diameter, pane about 1.4% (the shipped kit, DL-057). Flat: gaps and pane each about 3% (at least 1px at 32px).

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
- Copy example (S22, as built): subject `claude-desktop is waiting for your countersign`; heading `claude-desktop is waiting for you`; body `It wants to run postgres.execute on Acme Demo. Policy "orders-writes-need-approval" asks a person to countersign: …`; button `Review and countersign`.
- **`email-context-panel`** (S22): the approval email's facts, read like a `deed-card`. A `paper-50` (dark: `ink-900`) table cell with a `#E4E5EA` (dark: `ink-800`) hairline and 10px radius; the summary first at 16px/600, then caption labels (Agent, Rule, Needs, Expires) in a 96px column, mono for the rule id, tabular numerals for times. The SQL, when an org chose `email_detail: full`, is a small `code-block` (always dark). The button stays the only coloured element.

### Approval page
**`approval-page`** (S22, `/approve`): one person countersigns one action. A single 400px card (mobile: full width minus 32px, 28px/24px padding; ≥ 441px: 36px padding) on `--bg`, centred when the viewport is tall. Top to bottom: the wordmark, a status chip (`verdict-badge` metrics; sentence-case labels render uppercase like the verdicts: `Waiting for you`, `Waiting for others`, `executing`, `L2 · VERIFIED`), the title (22px/600, balanced), the summary lede (16px), the context as a `deed-card`-style fact grid (caption labels, mono values, the rule's reason in secondary text), the SQL as a `code-block`, then the decision: **Approve and run** (`button-primary`, 44px) and **Deny…** (`button-secondary`), above the fold at 360×800. Fine print and the agent's last five actions (tool, time, verdict chip) sit below the decision. Deny opens an inline sheet (a required reason, 280 characters, the error under the field), never a modal. After a decision the context collapses and the page follows the action to the seal: the orb's halves close (240ms) with one teal pulse for VERIFIED, or stop short with a red hairline for MISMATCH; reduced motion shows the end state at once. Every terminal state offers **Open in Console**. Tokens come from the shared `internal/identity/pages/tokens.css` (also used by the sign-in pages), so the auth and approval pages can't drift.

### Scan report page (S32)
**`scan-report`** (`deedglass-scan.html`, `internal/scan/render_html.go`): a self-contained local page, no script, no font file, no external request (CSP meta `default-src 'none'`). Tokens are inlined from this file: light canvas by default, the dark ramp under `prefers-color-scheme: dark`, `theme-color` per scheme. One 880px column (16px gutters). Top to bottom: the wordmark line with the directory and time (secondary, 13px), the score line as the H1 (22px/600; it wraps only at its `·` separators, each separator ending its part), the grade card (a 72px tile holding the letter at 40px/650, then points and the small print, stacked below 600px), then the deductions table directly under it (the score never appears without them; points right-aligned in tabular numerals with the true minus sign; deduction labels on one line from 600px up). Findings are cards with a severity chip (`verdict-badge` metrics: high = the MISMATCH pair, medium = the UNVERIFIABLE amber pair, low and info = neutral), the detail, fixes in secondary text and sources in mono. The Postgres table keeps a 720px minimum and scrolls inside its frame with the first column sticky; every scrolling frame and code block is focusable with the accent focus ring. Policies are dark `code-block`s in both themes. The footer is one line with one link. Lighthouse 100/100/100 on desktop and mobile (S32).

### Playground stage (S47)
**`play-stage`** (`site/src/play/ui/`, deedglass.com/play): built on the site's v4 tokens with fallbacks (the site, not the Console, owns those values). Scene tabs as a segmented control (44px targets, wrapping to two rows under 860px). At 1440 a 5/7 two-column stage: the agent (statement in mono on a panel, its claim as a quoted line, the decision line, Run / Approve, the collapsible 6-line policy editor with inline subset errors, the eight-statement menu) beside the check (the verdict line in mono, `aria-live`, then the "rows outside" line, a real `<table>` captioned "Rows Postgres changed" with 36px rows, tabular numerals, the named row first and other rows' changed cells at 12% of the mismatch colour, scrolling inside its own frame; Undo; the honesty line above a hairline). Below: a horizontal chain of compact record cards (seq, tool, decision, `Checked · Matches|Doesn't match`, short hash), Check these deeds and Change one byte, then the terminal panel's copy rows. One column at 360 in the order agent, check, records, terminal. Colour: red only for Doesn't match and blocked, violet only for the approval wait, teal only for Matches and restored. Motion: rows appear at once; the only animation is one 400ms teal ring on "rows restored" (Sealed), removed under reduced motion. Every string from `engine/copy.js`, set with `textContent`.

### Grade glyph (S47)
**`grade-glyph`** (`site/src/viral/ShareCard.astro`, /s): one 112px tile with the letter at 64px/600, then the H1 `Agent blast radius: D`, points and counts, the deductions table (tabular numerals), finding titles, the footnote, and one action (the `npx deedglass scan` copy row). Badge colours only: A and B teal, C neutral, D and F amber; never red. The shields badge uses the same mapping (`0F766E`, `3F4453`, `9A5B00`).

### Install row (S47)
**`install-row`** (`site/src/viral/InstallRows.astro`, /install): Raycast-style rows on a panel with a hairline: the client name (17px/600), one note line, then a primary text button (`Add to Cursor`) for deeplinks or a mono command block that scrolls inside itself, then quieter alternate links. No third-party image on deedglass.com. Anchors per client; `:target` raises the border.

### PR comment (S47)
**`pr-comment`** (`scan-action/src/render.mjs`): Markdown only, the marker comment the only HTML. Headline carries the verdict (`agent blast radius **C** (was **B**)`), one compact Before/After table, "New in this pull request", "Fix this first", and a `<sub>` footer with the only links (deedglass.com/score and the run). Under 2,000 characters for a typical diff; no image, no emoji.

### Console kit (S24)
The Console builds every screen from the kit at `/app/_kit` (dev builds; `web/src/components`, `web/src/lib/chips.ts`, `web/src/controllers`). Rules the kit settled:
- **Chips:** verdict chips keep their capitals (`L2 · VERIFIED`) at 12px/600 with +0.04em; decision, state and status chips are sentence case at 12px/550 (`Allowed`, `Pending approval`, `Stopped`). Every chip has an icon and a word. Denied is an outline.
- **Table header:** caption size, small caps (`font-variant-caps: small-caps`), secondary text. Row links read as text and turn accent on hover, so a column of tool names doesn't become a column of blue.
- **Tables under 640px:** compact cards: the first cell as the title, the meta cells (agent, verdict, time) on one line, the rest (`data-card="hide"`) on the detail page.
- **Secondary text on raised surfaces** (kbd hints, the segmented control, a selected palette row) uses `text-2-on-alt`.
- **Sheets:** right side, 480px (a bottom sheet under 640px), glass, Escape and the scrim close, focus trapped and returned, the page behind inert. **ConfirmDialog** (typed): the red button enables only on an exact, case-sensitive match; Cancel has the initial focus; the reason says where it goes.
- **Banner:** a 3px inset rule on the leading edge (red only for the org-stopped banner), an icon, one or two lines; no button when the top bar already carries the action.
- **Palette:** 560px glass, 20vh from the top (full screen under 640px), groups Jump to id · Go to · Actions · Agents · Recent, shortcuts as kbd chips, a pasted id jumps.
- **The Mirror in lists:** only live rows that were just witnessed show it (VERIFIED plays Sealed, MISMATCH stops short); history rows show a quiet icon, so aurora stays out of lists.
- **Avatar:** initials in an 8px-radius square, not a circle.

### Console screens (S25)
The five screens (Overview, Actions, Action detail, Deeds, Agents) add these components to the kit (`/app/_kit`, section "S25 components"; `web/src/controllers/components.ts`, `difftable.ts`, `jsonview.ts`, `deedpanel.ts`). No new token: every value is an existing role variable.
- **`metric-tile`:** tiles share one hairline strip (`{rounded.card}`, cells divided by 1px hairlines, never four separate cards; 2 × 2 under 1024px). Label in `console-caption` small caps, the number in `console-metric` with tabular numerals, one neutral footnote that states its window (`+8% vs previous 24 hours`; a delta is never a verdict colour), an optional 120 × 28 sparkline in `text-2` with the min and max in its label.
- **`verification-bar`:** one 8px stacked bar (VERIFIED, MISMATCH, UNVERIFIABLE in their `-fg` colours, not witnessed in `border-strong`), drawn as SVG rects (the CSP refuses inline widths); the legend carries every label and count in text, and each verdict links to the feed pre-filtered.
- **`usage-meter`:** a 4px track and the numbers in text (`212 of 5,000`), amber (`unverifiable-fg`) from 80 %, red (`mismatch-fg`) at the limit with the refusal sentence; no limit reads `18 calls today`.
- **`attention-list`:** 56px rows, a 16px state icon in its tone, the title in 550 weight, the detail (the Witness's sentence, verbatim) in secondary text, relative time right; the whole row is a link. Empty: a neutral ✓ (teal is reserved for verified deeds).
- **`filter-bar`:** sticky under the 56px top bar on `--bg`; row 1 the search (with `/`), pickers and More filters, row 2 the segmented filters (one horizontal scroller under 640px). Multi-select uses `aria-pressed` buttons in the segmented control.
- **`verdict-sentence`:** the action's one sentence at 15px/550 on `surface` with a 3px inset leading rule in the verdict's colour (the only red on a MISMATCH page besides the chip and the diff tint: "one hairline red"); text stays `--text`.
- **`diff-table`** (brand §12): two summary lines (`Expected`, `Observed`, caption small caps, mono values), then Table · Key · Column · Expected · Observed in mono; an unexpected row's observed cell is tinted with `--mismatch-fg` at 12 % (pending at 12 % in the undo sheet), weight 600, a `●` and the words "unexpected change"; the before value follows as `was paid` in secondary text; masked values are `•••` with a 11px `masked` tag. Under 640px each row is a card with the labels inline. `Showing 20 of 301 rows` and `Download diff (JSON)` close it.
- **`deed-panel`:** a `deed-card` in the aside (sticky at 80px from 1200px): the Mirror at 48px in the deed's final state, `Deed sealed · seq 617`, a compact key/value list (Deed, Body hash, Seal key in mono with copy), `Check in this browser` as a full-width secondary button, the result line (✓ teal icon, ✕ red text, ○ amber icon), the honest note in footnote, then `Download deed` and `Copy command` as quiet buttons. Sealed plays once, only on a live seal or a successful check.
- **`json-view`:** a tree with 14px chevrons, keys in secondary mono, strings in primary mono, numbers, booleans and null in secondary; copy path and copy value appear on hover or focus; nodes past depth 8 or 2,000 load on click.
- **Feed cards (< 640px):** each action row becomes one card (the tool and the verdict chip, then agent · decision · time), at least 64px tall; the tool's link still covers the row.

### Console screens II (S26)
Approvals, Policies, Connectors and Settings add these components (`/app/_kit`, sections "S26 components"; `web/src/controllers/decisionsheet.ts`, `approvals.ts`, `policyeditor.ts`, `diagnostics.ts`, `traceview.ts`, `linediffview.ts`, `schemaform.ts`, `secretfield.ts`, `checklist.ts`, `countdown.ts`). No new token: every value is an existing role variable.
- **`approval-card`** (designed at 360px first): min 96px, `{rounded.card}`, hairline, the whole card is the link. Line 1: the pending `◐` in `--pending-fg`, the tool in secondary mono, the countdown right (tabular). Line 2: the one-line effect at 15/22, weight 600 (server context, never agent text). Lines 3–4: agent · on behalf of, then the rule in mono and `1 of 2 countersigned` in pending. Selected: surface lift plus the 2px accent leading bar. Violet appears only on `◐` and four-eyes progress; everything else is neutral, so the one decision is the loudest thing on the phone.
- **`approval-detail`:** the effect as `title-3`, then the rule and its reason as a quote with a 2px `border-strong` rule, the SQL as a `code-block` (wrapping), facts as a 104px-label key/value list on every width, the arguments in a hairline region labelled `Arguments (from the agent)`, the agent's last ten actions with verdict chips, the decisions, the expiry sentence.
- **`decision-bar`:** sticky at the bottom of the detail (above the 64px bottom bar and the safe area on phones; inline at the end of the detail from 1024px), two 48px buttons with a 12px gap: Deny in `button-danger-outline` (a refusal, never filled), Approve in `button-primary`. When the person can't decide, one sentence replaces the buttons.
- **`decision-sheet`:** a glass bottom sheet under 640px, a 440px dialog above. The lede repeats the effect; Approve's reason is optional, Deny's is required (3–280). On touch, Approve opens with focus on Cancel so the keyboard doesn't cover the decision; the `⌘ Enter` hint is hidden on coarse pointers. The busy button shows `◐`.
- **`countdown`:** `in 9 min`, every 30s, every second under 2 minutes; under 5 minutes the weight goes to 600 (never red: an expiry isn't a mismatch); one polite announcement at 1 minute, from the open approval only.
- **`policy-editor`:** always a `code-block` surface (`code-bg`, `code-hairline`) in both themes, 10px radius, a 32px header strip (file name, `Line 40, col 13`), a quiet gutter (line numbers in `code-comment`, a 20px marker column with ✕/○), Geist Mono 13/20, tab size 2; diagnostics are wavy underlines in `mismatch-text-dark` / `unverifiable-text-dark`, never shaded blocks; the focus ring wraps the whole editor. Under 640px the gutter narrows to 3ch and the padding to 10px.
- **`diagnostics-list`:** rows of `[✕ Error] Line 40, col 5 · message hint` with `Apply fix` (secondary, small) and `Docs ↗` (quiet); clean is a neutral `✓ No problems · 16 rules · 1 budget`, only from a real lint result.
- **`trace-view`:** the decision chip and the Gate's message, `Rule block-unscoped-writes · line 47` (the line is a button that reveals it), then one mono row per step: `✓` pass, `✕` a matched deny, `◐` a matched approval, `·` no match; the SQL facts and `evaluated in 38 µs` in secondary.
- **`line-diff`:** unified, three lines of context, `@@ lines 37 to 44 @@` headers, old and new line numbers, `+`/`−` gutters with sr-only words; added lines on accent at 10%, removed on `surface-alt`: neutral, because a policy diff isn't a verdict.
- **`activation-confirm`** (the typed confirm, S24's ConfirmDialog rules): the exact version and hash in a key/value row, the policy's name typed (case-sensitive) and a required reason before `Enforce v3` enables; Cancel has the initial focus; a `default_allow` version adds a required checkbox. Shadow needs only the reason.
- **`readiness-report`** (`check-list`): reads like a plan: one summary line (`Ready for Proven checks · 14 passed · 3 notes · 0 failed` quiet, or `Not ready for Proven checks · 1 item to fix` with a red `✕`), then a calm column: passing rows are compact, secondary and link-free; notes (`○` amber) and failures (`✕` red) spell out the detail, `Fix`, `Copy SQL` (always with the `-- Review before running.` header) and `Docs`. The provider line is a neutral banner.
- **`witness-card`:** an 8px status dot (verified, unverifiable or mismatch colour) beside `Witness · healthy · 12 MB behind · last commit 4s ago`, then slot, watched tables, retained WAL and since as key/values; lagging adds one amber sentence, invalidated a sentence with a 3px red inset rule.
- **`schema-form` / `secret-field`:** labels above with the config key in secondary mono and a `Required` word, help below, the server's error under its field; enum lists as checkboxes, string lists as removable chips, nested objects as fieldsets, anything deeper as a JSON textarea with a note. Secrets are `type=password`, never pre-filled, with a `Show` toggle that resets on blur.
- **`type-card`:** 120px min, the type icon in a 36px `surface-raised` square, the title, level badges (`L0 L1 L2`, 20px hairline boxes, small caps) right, one description; cards align their tops.
- **`settings-layout`:** a 200px side menu on desktop (the current section with the accent bar), a list screen on phones; rows are label and one sentence left, the control right, hairlines between.
- **Approval page (S22) addition:** the pending state also carries a quiet `Open in Console` link below the decision buttons.

### Console: Replay (S27)
The Policies screen's Replay tab (`web/src/controllers/replay.ts`, `web/src/lib/models/replay.ts`, `web/src/styles/s27-replay.css`; copy in `copy-policies.ts` `replay`). A replay is a what-if, not a verdict, so the tab is neutral: no new token, no aurora, no accent fill outside the Run button. Its sentences are `internal/learn/copy.go`'s, word for word (a Vitest pins them).
- **`replay-form`:** one card: `Replay v2` (headline), the lede, then label-above controls on one row from 640px: `Window` (segmented `Last 24 h · Last 7 d · Custom`; Custom opens two `datetime-local` fields that name the browser's zone and refuse more than 90 days inline) and `Compare with` (a native select: `Recorded decisions`, then `Version vN` for every other version), one help sentence, then `Run replay` (primary). While a replay runs, Run stays focusable with `aria-disabled` and `Cancel replay` (secondary) sits beside it; under 640px both are full width.
- **Role rule:** viewers get the lede, a `banner-readonly` with `Viewers can read replays. Running one needs the Admin role.` and Run disabled; the controls are hidden. Everyone sees the policy's latest replay (the open version's first), labelled `Latest replay`.
- **`replay-progress`:** a card with `◐ Replaying 5,890 actions · 41%` (tabular, `◐` in `text-2`: nothing waits on a person, so never violet) over a 4px native `progress` (track `surface-raised`, fill `text-2`, never the accent), `aria-valuenow` and `aria-valuetext` set; the meta line below. Polled every 500 ms and updated in place (no rebuild, no transition), so nothing flickers and focus never moves.
- **`replay-result`:** one card, three bands split by hairlines. Head: the summary line as the `h2` (`453 decisions would change · 15 by a different rule · 5,422 unchanged · 0 unknown`, headline size, 600, tabular), the meta in footnote secondary (`acme-demo v2 against recorded decisions · 26 Sept 2026 07:03 to 4 Oct 2026 07:03 UTC · 5,890 actions · 90 ms · 16m ago`); the unknown line only when unknown > 0 (`○` in `unverifiable-fg`, `14 unknown` at 550, the reasons in secondary; never red); the sqlguard drift line (info icon, secondary) only when recorded differs from current. Body: `What would change` (footnote 600 secondary), one row per transition read like the CLI: from 768px a two-column grid (the pair `require_approval → allow` in mono plus its count right-aligned in a 34ch column, then the candidate's rules as `label-chip`s, the human outcome sentence in primary footnote, the tools in secondary mono, and `First 20 by decision time` as a `details` whose ids are `shortId` links to `/app/actions/{id}` in an auto-fill grid); stacked under 768px. Rule changes get their own band (`Same decision, different rule`, `deny` then `refund-limit → refund-limit-strict`). Foot: a `surface-alt` strip, `text-2-on-alt`: `Result sha256:786a…f22c · same inputs and version always give this digest` with an icon-only copy button carrying the full digest.
- **Colour rule:** transitions are neutral. The only red is `deny` as the candidate's new decision: the denied outline (1px inset ring in `denied-fg`, chip radius, never filled) on that word alone. No verdict colours, no accent tint on rows.
- **`replay-failed`:** a `banner` with `role=alert`, the 3px neutral leading rule and a secondary `circle-alert`: `The replay failed.` (strong) and the server's message, which carries the next step; the meta line under it. No second Run button: the form's Run is the next step.
- **`replay-cancelled`:** a card with a neutral `circle-slash` and `Replay cancelled after 2,415 of 5,890 actions. Run it again to see the full result.`; no alert role, no red.
- **Older servers:** a 404 shows S26's `banner-readonly` sentence (`Replay isn't available on this server yet. Use Dry run to test a single action.`).

### Console: Insights (S28)
The Insights screen (`/app/insights`; `web/src/controllers/insights.ts`, `insightscharts.ts`, `activation.ts`, `web/src/lib/models/insights.ts`, `web/src/styles/s28-insights.css`; copy in `copy-insights.ts`). Deedglass proposes, people decide, so the screen reads like Linear's inbox: one decision per card, keyboard first, nothing flashing. No new token and no aurora: violet only on the approval chip (a person is asked), red only in the deny and mismatch chips and the MISMATCH series, glass only on sheets and toasts, the accent only on the primary button, links and focus.
- **Layout:** the page head (`Insights`, the lede `Suggestions from your agents' traffic. Nothing changes until someone on your team accepts it.`), then four sections in one scroll, each an `h2` (headline size) with a 1.5px Lucide icon in `text-2` and a tabular count right (`3 open`): Suggestions (`file-diff`), Shadow (`git-compare`), Anomalies (`activity`), Trends (`chart-column`), 32px apart. Admins also get `Check for suggestions` (secondary, small; icon-only with its name kept for screen readers under 640px) beside the Suggestions count, so the page head never grows after load. From 1280px a 296px right rail (sticky under the top bar) holds the settings summary and `How Insights works`; below 1280px the rail follows the sections. Each section loads, fails (the S24 error state in a card: message, `request_id` in mono, Retry) and retries on its own; skeletons keep the card's shape, and a suggestion skeleton is about a real card's height (560px), so nothing below it jumps (Lighthouse CLS 0.01).
- **`suggestion-card`:** `{rounded.card}`, hairline, 20px padding (16px under 640px), 12px gaps, `tabindex=-1` so `j`/`k` can focus it (2px accent ring, 2px offset, `:focus-visible` only). Header: the kind in small caps (12/16, 600, +0.04em, `text-2`), `·`, the target rule in Geist Mono (truncates), the id right as an `idchip` (copy). Title `title-3`; the rationale in body `text-2`, 72ch. Then two hairline panels (10px radius, side by side from 768px): **Evidence**, a `max-content | 1fr` key/value list with labels in `text-2` and values right-aligned in tabular numerals (mono values and sample-action links take the full row), and **Replay**, `14 days · 5,890 actions` in the panel head and one row per transition (`replay-evidence`).
- **`replay-evidence`:** `[Approval] → [Allowed]  411` with the S24 decision chips (Allowed neutral, Denied the red outline, Approval violet), the arrow in `text-2` with a screen-reader "to", the count right at 600 tabular, the human outcome under it in footnote primary (`398 approved · 0 denied · 5 expired · 8 pending`; zeros dropped except denied). The Shadow card and the Promote sheet reuse the same rows (`enforce → shadow`).
- **`policy-diff`** (reuses S26's `line-diff`): collapsed by default as a quiet toggle `▸ Policy change +8 lines` (the chevron turns 90° in 120ms; instant under reduced motion; `aria-expanded`/`aria-controls`). Open, the line-diff sits on the code surface in both themes (`code-bg`, `code-hairline`, Geist Mono, `code-plain` text, numbers and signs in `code-comment`): `+` lines tinted with `accent-dark` at 12% and an accent `+`, `−` lines a neutral `code-plain` 6% tint with a `−` marker. **No red in diffs.** The frame is a focusable region, max 420px tall, and scrolls on both axes inside itself, so a long `when:` never widens the page.
- **Risk note:** a footnote row above a hairline: `Risk` in small caps `text-2`, then the server's sentence in primary text. Neutral, never amber. The **phrasing note** (an `info` icon and `Worded by claude-sonnet-5 from the numbers above. Deedglass checked that every number matches the evidence.`) appears only when `phrasing.source` is `claude`.
- **Actions:** a footer above a hairline, right-aligned: `Reject` (`btn-quiet`) and `Accept and run in shadow` (`btn-primary`, the card's one primary). Advice cards (no diff) offer only Reject with a one-line note. Viewers get neither, and a `lock` footnote `Only admins can accept or reject suggestions.` Under 640px the footer sticks to the card's bottom edge above the bottom bar (`surface` fill, both buttons full width) and focused elements keep a scroll margin so the bar never covers them.
- **Sheets** (S24 sheet: glass, right side, 480px, a bottom sheet under 640px, 240ms): **Accept** (`Accept this suggestion?`, the body `Deedglass creates acme-sim v3 from it and runs it in shadow next to v1. Nothing is enforced until someone promotes it.`, the suggestion in a hairline strip, an optional reason; when a shadow is live a strip with a 3px neutral inset rule, `acme-sim v2 is running in shadow now. Accepting replaces it.` and a required checkbox `Replace v2 in shadow` that gets the initial focus; Enter outside the reason submits, so `j j a Enter` asks for the box and sends nothing). Success toast `acme-sim v3 is running in shadow · deed_01m4…pytr sealed` with `View deed`. **Reject** (`Reject this suggestion?`, `Deedglass won't suggest this again for 30 days unless the numbers double.`, reason required; the button stays enabled and an empty reason shows `A reason is required to reject a suggestion. It goes into the sealed deed.` under the field with `aria-invalid` and focus). **Promote** reuses S26's `activation-confirm` (`activation.ts`): the shadow sentence, `95.8% agreement · Agrees on 4,004 of 4,179 actions` and the transition rows in a hairline strip, the neutral `banner-readonly` notice under 100 decided actions or 24 hours (`Only 12 actions have been decided since v3 entered shadow. You can promote now, or wait for more traffic.`), then the typed name, the reason and the `default_allow` box; toast `✓ acme-sim v3 enforced · deed sealed`. **Resolve** and **Insights settings** follow the same reason-required pattern and say the change is sealed. Kit change: a centred `dialog` is capped at the viewport (`100dvh - 18vh - 16px`) and scrolls its body, so a tall Promote keeps its buttons on screen.
- **Shadow card:** the version in mono (`acme-sim v2`), a neutral outline `Shadow` chip (not violet: nobody is being asked), `In shadow since 4 Oct 2026 15:35`, `Promote` (`btn-secondary` with the `gate` icon, admins only); the server's shadow sentence; an agreement strip (`95.8%` in `console-metric`, `Agrees on 4,004 of 4,179 actions`, `Enforced: acme-sim v1`); `Where it disagrees` as transition rows with the rules and agents in mono; `Open in Policies →`.
- **Anomaly list:** one card, hairline rows: kind in small caps · agent (600) · tool in mono, an amber outline chip `Active now · policies see it` (`Active now` when anomaly facts are off; `unverifiable-fg`, transparent, `activity` icon) only while active, the time right; the server's title in tabular numerals; `Acknowledge` (quiet, check icon) and `Resolve` (secondary) for admins.
- **`insights-chart`:** a `figure` card per chart, no chart dependency: inline SVG with rects only (attributes, no inline styles, so the CSP holds), fills by class from state roles (`verified-fg`, `mismatch-fg`, `unverifiable-fg`, `border-strong` for not witnessed; denials and approval waits in `text-2` and `text-2` at 55%), a hairline baseline, no tracks, no gridlines, no entrance animation. `role="img"` with a summary `aria-label` (`Verification mix, last 30 days: 97.8% verified, 0.1% mismatch, 0.7% unverifiable`), a legend of icon, word and tabular total, a caption-size axis (first day, `max 933`, last day; `aria-hidden`), and `Show as table` (`details`) over a focusable scrolling table with every number. A small non-zero MISMATCH or UNVERIFIABLE day keeps 2 units of height; the table carries the exact count. The mix chart spans the row from 900px; denials and approval wait sit side by side.
- **Rail:** `Settings` with `Change` (admins; opens the settings sheet: anomaly facts, Claude phrasing (disabled with the server-key sentence when unavailable), digest recipients, reason required) over a footnote key/value list (`Anomaly facts` · `Off · policies don't read anomalies`, `Claude phrasing` · `Off · Deedglass's own wording`, `Weekly digest` · `Owners`), then `How Insights works` as a numbered footnote list in `text-2` and `Read about suggestions ↗`.
- **`simulated-banner`** (shell, every screen of a simulated org): a neutral `banner` (3px `text-2` inset rule, `flask-conical` icon on the first line, copy with `text-wrap: pretty`) under the top bar with `internal/learn/copy.go`'s `SimulatedBanner` word for word. It shows when `GET /v1/org` carries `settings.simulated` (S29: read only, omitted for a real org), or a screen that learns it another way calls `setSimulated`. S29, no layout shift: the shell draws it in its first pass, run inline when the page script starts, from the last org's flag (`deedglass.simulated` in localStorage, a display hint beside the theme, cleared on sign-out) and the page (`.main`) stays hidden until that pass sets `body[data-shell]` (1.5 s CSS fallback without JavaScript); `GET /v1/org` then confirms or removes it and updates the flag. A browser's first visit to a simulated org still shows it after `GET /v1/org`.
- **Live and keyboard:** `suggestion.created`, `anomaly.detected`, `policy.activated` and admin `deed.sealed` refetch only their section (debounced; an unchanged list isn't redrawn, so focus and open diffs stay); the polite shell live region says `1 new suggestion`. `j`/`k` move between cards, `Enter` on a card toggles its diff, `a` and `r` open Accept and Reject, and `?` lists them above the global keys. Shortcuts never fire while typing or with a sheet open.

### Undo diff (approval page)
**`undo-diff`** (S23, inside `approval-page` when the action is an undo): what the undo puts back, read like a `deed-card` diff. A caption line `Puts back` (secondary, 13px/600) followed by the headline in primary text (`51 rows in public.orders · 51 updates`, counts joined with non-breaking spaces); an optional amber `banner` when `allow_triggers` is on; then a three-column table in a 10px-radius hairline frame: `Row` (46%, secondary mono, wraps; the table name leads when several tables), `Now`, `After undo` (mono, one line, cut with an ellipsis and the full value in `title`). Header 32px caption type; rows 36px with a hairline between rows (none on the last); tabular numerals. The `After undo` cells carry the pending state at 12% (`color-mix(in srgb, var(--pending) 12%, transparent)`, fallback `--pending-bg`): nothing changes until a person countersigns, so the tint is the waiting colour, never verified teal. Two rows on a phone (keeps **Approve and run** above the fold at 360×800), six from 441px; `And N more steps in the plan.` below (the plan digest in its `title`). The scroll frame is a focusable `region` with the 2px accent focus ring. The requester's reason is a `Why` fact in the context grid. No red anywhere: asking for an undo is not destructive until a person countersigns.

### Slack approval card (S29)
**`slack-approval-card`** (`internal/notify/slack/card.go`, goldens in `testdata/cards/`): the `deed-card` hierarchy in Block Kit, text only. Slack owns the layout and colours, so the brand lives in order, words and symbols. Top to bottom: header `Approval needed · github.create_pr` (plain text); the summary sentence (names in bold, identifiers in code spans; built from facts, never agent prose); fields Rule, Reason, On behalf of, Expires (a Slack date token, viewer-local), Approvals (`1 of 2 needed`), Request (`sha256:3f9a…c21e`); the label _Values below come from the agent's request._ over the arguments in a fenced block (≤ 1,200 characters, then `… N more characters. The full request is in the Console.`); a context line of short ids (`act_01j9…7p9q` · `apr_…` · policy and version); actions Approve (primary, with a confirm dialog), Deny (danger, opens the reason modal), Open in Console. After a decision the header reads `Approval request · <tool>` and the body shrinks to two quiet lines (`✓ Approved by Arjun · <date>` then `✓ Deed sealed · L1 · VERIFIED`, or `✕ L1 · MISMATCH · <sentence>`; `✕ Denied by …`; `○ Expired <date>. Nothing ran.`; `○ Cancelled by the agent. Nothing ran.`), with Open in Console kept as a tap target. The symbols ✓ ✕ ◐ ○ always sit next to words; no emoji. Every interpolated value is escaped (`&`, `<`, `>`), flattened to one line, and runs of three backticks are broken, so an agent can't add a mention, a link or a status line. Limits checked on every golden: ≤ 50 blocks, section ≤ 3,000, field ≤ 2,000, button ≤ 75, card < 16 KiB. The deny modal: title `Deny action`, one multiline field `Reason · recorded with the decision` (1 to 280 characters), submit `Deny`, close `Cancel`.

### OAuth loopback page (S30)
**`oauth-loopback-page`** (`internal/connector/gmail/page.go`, goldens `testdata/page_ok.html`, `page_fail.html`): the tab `deedglass connector gmail authorize` opens on `127.0.0.1` after Google's consent. The `approval-page` card at a smaller scale and with no external asset at all (inline CSS and SVG, `Content-Security-Policy: default-src 'none'`, a `data:` favicon): a 400px card (mobile: full width minus 32px, 28px/24px padding; ≥ 441px: 36px) centred on `--bg`, safe-area insets respected. Top to bottom: the flat Split Orb (22px, aurora left half, frost right half: `frost-light`, or `frost-dark` at 85% on dark) with the lowercase wordmark; one state chip in `verdict-badge` metrics (`✓ Connected` on the verified pair, `✕ Not connected` on the mismatch pair; icon plus word); the title (22px/600, balanced); one sentence (16px, secondary text, the email in primary text with `translate="no"`); on failure, the next step with the command in an inline `code` (mono 13px on `mist-100` / `ink-850`); a hairline and the fine print (13px): `Deedglass writes drafts only. People send them.` or `Nothing was stored. You can close this tab.` No buttons and no links: the terminal is the next step. Light and dark follow `prefers-color-scheme` (`color-scheme: light dark`, two `theme-color` metas); the 240ms enter fades only under `prefers-reduced-motion: no-preference`. No new token.

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

Built in S36 (`~/txv/site`, Astro static on Cloudflare Pages, ADR-071). How it maps, so later pages (S37, S42, S43, S47, S48) stay in lockstep:
- **Theme source.** `src/styles/tokens.gen.css` is generated by `scripts/sync-product.mjs` from `web/tokens/tokens.json`: primitives as `--c-*`, the light roles on `:root`, the dark roles under `prefers-color-scheme: dark`, and `.on-ink` (the dark roles on `ink-950`) for every band that is ink in both themes. No hex outside it (`check-dist.mjs`). The page follows the system theme; the ink bands are the deliberate polarity (brand §13.2), not a theme switch.
- **Page rhythm.** Every page opens with an ink-950 head band (`PageHead`: display-m H1, a slate-400 lead at most 680px wide); the home page's hero is ink-950 with the glass orb (`orb.svg`, `mix-blend-mode: lighten` so the page's one aurora glow, 40px blur at 35%, shows behind the orb only). Reading sections follow on `paper-50`/`ink-950`, 96px apart (64px under 734px), separated by a hairline. The final call is an ink band.
- **Header.** Sticky, 64px, overlaid on the head band (`margin-bottom: -64px`): transparent over ink, then the brand §9 glass layer after the head band through a CSS scroll-driven animation; without scroll timelines it is an opaque ink bar. Flat mark (kit file) at 28px + the wordmark in Inter 600 at -0.04em. Below 734px the links fold into a `<details>` menu (no JavaScript).
- **CTAs.** `btn-aurora` (ink-900 fill, snow text, 1px aurora border with a soft glow) is the one primary marketing CTA per view: **Try it in your browser** in the hero and final call, **Get started** on `/pricing`'s Self-hosted card, **Get Pro yearly** on `/pro`. Everything else is `btn-secondary` or `btn-primary` (accent fill) inside forms. 44px tall; the hero pair stacks below 480px with the primary first.
- **The copy box** (`CopyLine`): `$ npx deedglass scan` in Geist Mono on ink-900, a Copy button added by a < 1 KB script (hidden without JavaScript; `Copied` for 2 s with a polite live region).
- **Pricing.** Two shelves: three cards (radius 14, hairline, no "Most popular") then a two-row hairline list. Prices in Inter 650 with tabular numerals; the yearly Pro price large, the monthly under it, the saving line in the verified colour. Quotas and money in Geist Mono only where they are data.
- **`/pro`.** Two columns at 1440: what Pro adds and what stays free on the left, a price card on the right with the buy links (plain `<a href="/buy/<sku>">`, yearly first and primary). Then the report (only with `REPORT_LISTING`), the licence, checkout and the Review line as three text columns, then the founder block.
- **Founder block.** A hairline card: name, `Founder, Deedglass · India`, `Replies within one business day.`, the contact email; a photo only when `public/founder.jpg` exists. **Founder story** (home section 9): a small-caps label, the line in display-m at reading width, the founder's words when present, the name line and two text links. No quote marks, no photo, no ornament.
- **Deed check.** A card with the flat Split Orb (the kit's paths, grouped so CSS moves each half: Sealed closes them over 240ms and one teal ring pulses for 400ms at 20%; a mismatch stops 1 unit short with a 2-unit red seam; reduced motion jumps to the end state with a fade), three action deeds as rows (seq, id, tool, verdict chip), the buttons, and the result in Geist Mono on the verdict pair.
- **Attack results.** A framed, horizontally scrolling table (min 760px) with a sticky header: case id in mono, title with its category under it, expected and actual in mono, and a chip (`✓ Match` verified pair, `✕ Differs` mismatch pair, `◐ Waiting` pending pair). A differing row is tinted at 6% of the mismatch colour, never collapsed.
- **Releases block** (`/security#releases`): the latest version and date, the sentence `Every release is signed. Check yours before you run it.`, the verify command from `release.json` in an ink-900 panel, then the checksum, signature and SBOM links. Absent until the first release.
- **`/play` shell.** Three scene cards (agent line on ink-900 in mono, result sentence, chip), the honesty line, then the terminal panel with the copy box. S47 owns the playground's own look.
- **`/design-partners`.** A letter: the offer as four short lines and a places chip in the head band, `How it works in writing` as a numbered list beside the form, then the founder block and a two-item FAQ.
- **Forms.** Labels above inputs (15px, 550), 44px inputs, helper and error text below, the required "Where did you first hear about Deedglass?" select on every form, a polite live region for the result. No placeholder labels.
- **OG cards** (`scripts/brand/cards.mjs`, template `link`): 1200x630, ink-950, the glass orb at 120px and the wordmark top-left, the title in Inter 64/600 snow, the subtitle in slate-400, the aurora glow bottom-right at 30%.

**S36D: the pane-of-light language** (the design upgrade, `~/txv/site` only; proposed, the founder approves; v2 notes in `DEEDGLASS_BRAND_V2.md`). The mark's own construction (two halves and a pane of light) becomes the site's visual system: one thin bright line runs through the hero orb's seam (the beam), separates the hero from the page (the horizon), links the flow's nodes and the chain's deeds, and cracks when a deed is changed.
- **Motion tokens (website only, `src/styles/global.css`):** `--ease-glide: cubic-bezier(.16,1,.3,1)` for reveals and settles; `--ease-swing: cubic-bezier(.65,0,.35,1)` for lights that travel (the flow links, the sheen); `--dur-glide: 720ms` (opacity), `--dur-stage: 900ms` (transform); `--stagger: 70ms` per sibling; `--rise: 18px`. The brand's 120/180/240/400 ms and enter/exit easings stay for UI states, and Sealed keeps its exact 240 ms close and 400 ms teal ring at 20%.
- **Reveal:** `[data-rv]` elements that start below the fold rise 18px and fade in once as they enter (`src/scripts/motion.ts`, IntersectionObserver, no scroll listeners); elements already on screen are never hidden; no JavaScript or reduced motion means nothing is hidden. `data-rv="scale"` adds a 0.97 scale for big artefacts.
- **The hero:** the kit's `orb.svg` shown three times from one request, clipped at the kit's gap centres into the left half, the pane strip and the right half (masked to the disc, R 292 of 1024), so the halves assemble on arrival (1.1 s glide from 4.5% apart, the pane rises 520 ms later), close for Sealed when the terminal's `deedglass verify` line lands, and rest exactly as drawn. Light: a breathing aurora glow (7 s), a teal sub-glow drifting (11 s), the beam breathing (6 s) and one sheen across the glass every 9 s (1.6 s of light). **These are the only perpetual animations on the site**, in the hero and page heads only, paused off screen and when the tab is hidden (`[data-live]`), and off under reduced motion: a deliberate website exception to "no perpetual loops" (Console rules unchanged). Pointer parallax (fine pointers only): the orb tilts up to 7° and drifts 8px toward the pointer.
- **The terminal:** a glass panel (brand §9 glass over the hero light, opaque without `backdrop-filter`) holding a real `deedglass scan` transcript (`src/data/scan-transcript.md` says where it came from) and the verifier's own line for the demo deeds. JavaScript types the commands (28 to 62 ms a character, clip-path on the line, the caret moved by transform), shows output lines 70 ms apart and scrolls by transform, so nothing shifts; a replay button; reduced motion or no JavaScript shows the end of the transcript.
- **Product mockups are live components, not pictures:** `ConsoleFrame` (the Console chrome at small scale: org, ⌘K, red-outline Stop all agents, the icon rail with the accent bar), the action detail around the DiffCard, the flow (`FlowStage`), the Gate feed, the policy editor, the deed stack, the Ledger, the Insights suggestion (the product's own templates, `internal/learn`), the Slack card (the S29 goldens word for word), the typed Stop confirmation and the undo diff. Every id, tool, rule, verdict and count is read at build time from the demo deeds (`src/lib/demo.ts`), never typed. They follow the section's theme roles, so paper sections show the Console's light theme.
- **Depth (website only):** `--shadow-float` and `--shadow-lift` (three soft layers, tinted to the ink, darker in dark mode) plus `--edge` (a 1px inner top highlight) on product artefacts and cards; cards lift 3px on hover (fine pointers). Ink stages (`.stage-ink.on-ink`) carry the flow, the Gate tile and Insights in both themes. Radii: 20 for tiles and frames, 14 for cards, 10 for inner panels.
- **Type:** `display-l` (36 to 54px, 640, -0.038em) for section heads; the hero H1 at up to 64px on two lines; `stat` numerals (48 to 92px, 600, -0.045em, tabular) for real numbers only (attack cases, bench rows), counting up from 0 once in view; `.fig` captions in Geist Mono 12px under artefacts, never above headings.
- **CTA light:** `btn-aurora` keeps its 1px aurora border; on hover or focus a blurred aurora halo (opacity .38, scale .9 to 1) rises behind it. Buttons lift 1px on hover, press to .98.
- **Scroll timelines:** the header's glass and the Console frame's settle use CSS scroll-driven animations, with `animation-timeline` and `animation-range` kept in their own `:not(#tl)` rules (`.tl-hdr`, `.tl-view`), because the CSS minifier folds a timeline into the `animation` shorthand, which browsers reject (the S36 header glass never ran because of it). In light mode the header glass is 92% white so its text stays AA over ink bands.
- **Section rhythm (home):** hero (ink) → the claim beside the Console (asymmetric) → the flow stage (full width) with steps and the real policy → the deed chain card → the capabilities bento (7 tiles, 12-column, two ink tiles) → the people bento (Slack card, Stop, Undo) → the level picker (radio group, CSS `:has`) → the scoreboard and bench grid → the founder's line → the FAQ with a sticky head → the ink final call with the orb and the beam.

### Docs (docs.deedglass.com)
Starlight themed with these tokens. 760px text column, `{typography.body}`, code blocks in Geist Mono on `ink-900` in both themes. Flat logo in the header. Callouts use neutral surfaces with a state icon, not coloured slabs.

Built in S33 (`docs-site/`, Astro + Starlight, ADR-064). How the tokens map, so later pages stay in lockstep:
- **Theme source.** `docs-site/src/styles/tokens.gen.css` is a byte copy of the Console's `web/src/styles/tokens.css` (`npm run gen:check` fails on drift); `deedglass.css` maps it onto Starlight's `--sl-*` variables. Dark is the default; light follows `prefers-color-scheme` or the theme picker. No hex outside the token files and the mark (`check-css.mjs`).
- **Type.** Inter Variable (self-hosted, latin preloaded, a metric-matched `Inter Fallback` so the swap never shifts), body 16/27, H1 36px at -0.03em, H2 24px at -0.02em, weight 600; Geist Mono 14px for code. Tabular numerals in every table.
- **Chrome.** The top bar is the one glass layer (`glass-dark|light`, blur 20px). The sidebar is quiet: group labels in small caps at `slate-400`/`slate-600`, the current page on `ink-850`/`mist-100` with a 2px accent bar on its left edge, never a filled accent pill. The mobile menu button is a hairline square, not a filled disc.
- **Components** (`docs-site/src/components`, all on `/_kit`): `VerdictChip` and `LevelBadge` (the chip pairs of brand §6.3, icon and word), `DeedCard` (a sample deed with an in-page check through `web/verify`; VERIFIED plays the Sealed moment, a tampered deed shows the MISMATCH halves and the CLI's own sentence), `Terminal` (✓ ✕ ◐ ○ coloured, `aria-hidden`, words kept), `ErrorEnvelope`, `Limits` (the hairline card under "What it doesn't do"), `Since`, `SealedMark` (the flat Split Orb; `glow` is the page's one aurora moment, home hero only), `Lifecycle` and `Architecture` (static SVG drawn at build, with a text alternative).
- **Asides** map Starlight's hues onto the state pairs: note → accent, tip → verified, caution → unverifiable, danger → mismatch; a hairline card with a 2px state edge.
- **Code.** Expressive Code theme `deedglass-ink` (the `code-*` tokens on `ink-900`, both themes), 10px radius, hairline `ink-800`, copy buttons copy commands only (the `$ ` transcript lines).
- **OpenAPI pages.** HTTP methods are neutral chips (`allowed-*`), never state colours.
- **OG images** (`public/og/*.png`, `scripts/og.mjs`): 1200×630 per section in the §13.7 template.

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
- **Site colour tokens** (resolved in S36): the site generates `src/styles/tokens.gen.css` from `web/tokens/tokens.json` (`scripts/sync-product.mjs`; `npm run check` fails when it is stale) and its old colour block is gone; display-xl is now 650 at -0.04em and display-m 600 at -0.03em, inside the brand doc's range.
- **Charts.** The brand doc bans aurora in charts but defines no chart palette. Until one exists, charts use neutrals plus verdict colours for verdict series only (see the dataviz skill for a validated method).
- **Final logo vectors** are the kit in `~/txv/site/brand/deedglass/final/` (DL-018), copied byte for byte into the site in S36. Under DL-057 the brand doc now follows the kit (glass gaps about 2.1% and the pane about 1.4% of the diameter; the light flat right half `#C3CADB`). Still open: the `frost-light` token (`web/tokens/tokens.json`, `#D9DEEA`) that the Console's flat mark and the site's generated tokens use; changing it regenerates the site's `tokens.gen.css`, so it waits for the site's review round (S37 site follow-up).
- **Light-mode code blocks** stay dark (`ink-900`) per brand doc 13.2; confirm this holds for inline code (proposed: inline code on light uses `mist-100` fill and `ink-text`).
- **Forced-colors mode** (Windows High Contrast) is not specified; chips must keep their border and icon so meaning survives.
- **Contrast script** ran in S24: `web/scripts/contrast.mjs` checks every declared pair in both themes (54 pairs, 0 failing) in `make web-test` and CI; the generated table is in `docs/DESIGN_SYSTEM.md`. Role fixes inside the palette: `on-accent-dark` = ink-950, `on-danger` dark = ink-950, secondary text on mist-100 = `#3F4453` (the `text-2-on-alt` role), input borders slate-400 / slate-500.
