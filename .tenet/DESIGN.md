# Design System — port-manager

## Chosen Direction
- Selected mockup: `.tenet/visuals/2026-04-17-01-mockup-terminal-dark.html`
- Design rationale: **Terminal-dark, monospace-first**, dense table layout on
  desktop that collapses into stacked cards under 820px. Matches the user's
  mental model (the dashboard is a replacement for running `lsof | grep 55`
  in a terminal) while adding colour-coded row state + inline actions. Low
  visual noise, high information density, fast to scan.

## Visual Principles

### Color palette
| Token | Hex | Role |
|---|---|---|
| `--bg` | `#0a0e14` | Page background |
| `--panel` | `#0f141b` | Cards, buttons, inputs |
| `--row` | `#0c1118` | Striped table row (even) |
| `--row-alt` | `#0a0e14` | Striped table row (odd) — matches bg |
| `--border` | `#1e2631` | All borders, dividers |
| `--ink` | `#d7dde3` | Primary text |
| `--dim` | `#7a8292` | Secondary / meta text |
| `--accent` | `#64d89f` | Success, live ports, "captured" badge, brand `▶` |
| `--info` | `#72c1f4` | Action-button hover, info badges, token value |
| `--warn` | `#f1c36a` | External-process badge, non-destructive warnings |
| `--danger` | `#f07a7a` | Kill action, self-row, error text |
| `--magenta` | `#d876d8` | Labels, restart-ready / remembered state |
| `--row-self` | `#0d1016` | Row background for dashboard's own-PID row (self highlight) |
| `--row-restart` | `#141026` | Row background for remembered / restart-pending row highlight |

No gradients. Solid colours only. A single border style (1px solid `--border`)
binds the whole UI together; accents come from border colour shifts, not fills.

### Typography
- Primary font: `ui-monospace, "SF Mono", "JetBrains Mono", Menlo, monospace`
  (monospace throughout — including body text. Matches the terminal aesthetic
  and makes `ps`-style command output readable.)
- Body size: `13px`
- Header/toolbar meta: `12px`
- Table header: `10.5px` uppercase, `letter-spacing: 0.6px`, `--dim` colour
- Footer: `11px` `--dim`
- Port numbers: `12.5px` weight `700` in `--accent`
- Labels: weight `600` in `--magenta`

### Spacing
Base unit: 4px. Row padding `10px` vertical × `10px` horizontal. Card padding
`12px`. Page padding `18–20px` desktop, `14px` mobile. Button padding
`6–8px × 8–12px`. No spacing scale token — this is a small UI; concrete
values are fine.

### Border radius
- Buttons, badges: `3–4px`
- Toolbar text input: `4px`
- Mobile cards: `8px`
- No pill shapes or large radii. Everything feels like a terminal.

## Component Patterns

### Buttons (primary actions, inline row actions)
- Background `none` or `--panel`; border `1px solid --border`.
- Default text colour `--dim`; on hover border becomes `--info` and text
  `--info`.
- `.btn.primary` — border `--accent`, text `--accent`. No filled primary.
- `.btn.kill` — text `--danger`, border `#3a2428`, hover fills to `#2a1518`.
- `.btn.restart-ready` — text `--magenta`, border `#3a2640`. Used only when
  the row is remembered (killed) and restartable.
- `.btn.disabled` — `opacity: 0.35; cursor: not-allowed`. No pointer events
  at runtime.

### Inline copy affordance (desktop cwd cell)
- `<span class="copy">⎘</span>` appended to the cwd text
- 1px border `--border`, tiny padding `0 4px`, colour `--dim`
- Hover → `--info` for colour and border

### Forms (rename input, filter input, login token input)
- Background `--panel`, border `1px solid --border`, colour `--ink`
- Padding `6px 10px`
- Focus outline: remove browser default, replace with `box-shadow: 0 0 0 1px var(--info)`

### Badges (source column)
- Inline-block, `2px 6px` padding, `3px` radius, `10px` font
- Variants: `.cap` (--accent), `.ext` (--warn), `.self` (--danger),
  `.remembered` (--dim). Colour sets both text and border; no fill.

### Table vs Cards
- Desktop (≥820px): `<table>` with zebra stripes `--row` / `--row-alt`.
- Mobile (<820px): `<table>` is `display: none`, `<div class="cards">`
  becomes `display: block`. Each card has a head row (port + label +
  badge + uptime), a cmd line (plain --ink), a cwd line (--dim), and a
  flex-wrap action bar with larger (`7px × 10px`) tap targets.

### Toasts (to be added in prototype)
- Fixed to bottom-center, `--panel` background, `--accent` left border,
  2s auto-dismiss. Text `--ink`, `12px`.

### Confirm dialog (kill / restart)
- Centred modal, `--panel` bg, `--border` 1px, `8px` radius
- Dim-overlay backdrop (`rgba(0,0,0,0.6)`)
- Two buttons right-aligned: `cancel` (plain ghost), `confirm` (danger or
  magenta depending on action)

## Layout
- Desktop: single-column `main`, max-width unconstrained (table can use full
  width up to viewport). Horizontal scroll only as last resort — column
  truncation (ellipsis) preferred.
- Header is always one row on desktop; wraps on mobile; `token` meta hides
  on mobile via the `@media (max-width: 820px)` rule.
- Footer is 1 line, always visible.
- No sidebar, no multi-pane layout — table IS the interface.

### Responsive strategy
- Mobile-first? No — the reality is the user primarily uses this on a
  laptop. But a mobile view is required (user explicitly asked). We use a
  **single CSS media query at 820px** that swaps `<table>` ↔ `<div.cards>`.
  Both variants are pre-rendered; JS does nothing for responsiveness.
- No sub-820 breakpoints — the card layout adapts naturally down to ~320px.

## Evolution notes
- All new CSS MUST use the defined CSS variables, not hex literals.
- If a new component is added, document it here BEFORE using it in more
  than one place.
- Keep monospace throughout. Do not introduce sans-serif.
- htmx swaps should target the table/card container, not the whole page —
  the toolbar+header stay put across refreshes.
