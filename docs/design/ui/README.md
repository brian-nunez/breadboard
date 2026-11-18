# Web UI design (v1)

Accepted by Brian on 2026-11-14 (D-031). This is the reference design for the Phase 3 web UI (`bb serve`, D-060).

- Design file: [`breadboard-ui.figcraft`](breadboard-ui.figcraft), 12 frames on one page.
- Renders: [`renders/`](renders), one PNG per frame.
- Source: [`source/`](source). The frames are generated from Python; change the screen functions and run `source/build.sh` (needs `figcraft-cli` and `python3`) instead of editing the `.figcraft` file by hand.

## Brief

- **Audience:** developers of every experience level learning system design. Write for people who have built software before.
- **Purpose:** read docs, visualize the architecture, and see what you built. Not a monitoring tool: no alerts.
- **Read-only:** the UI never changes the system. Anything actionable is a copyable terminal command (`bb add redis`).
- **Flow:** home (scenario suggestions or skip) → dashboard (overview, architecture, services) and docs (systems, tech overviews, guides) and starters, all in one site with a shared top bar.
- **Direction:** calm, light mode, minimal copy, no decorative pills or chatbot styling. Patterns map to templUI/shadcn components and Tailwind.

## Frames

| # | Frame | Notes |
|---|---|---|
| 01 | Home | Headline, plain search, scenario cards, skip to dashboard. |
| 02 | Scenario preview | Architecture, what it covers, containers and limits, run commands. |
| 03 | Dashboard overview | Stats, containers table (status, CPU and memory against limits, address, TLS), architecture preview. |
| 04 | Architecture | Graph with labelled edges (plaintext dashed red), inspector for the selected node. |
| 05 | Service: kafka | What you're running, topics and groups, try/break exercises, connection, resources, certificate. |
| 06 | Docs home | One card per system. |
| 07 | Kafka overview | The template for every tech page: what it is, good and poor fit, diagram, "go deeper" guides, Breadboard specifics, starters. |
| 08 | Guide: consumer groups | Article with diagram, live "in your experiment" note, code. |
| 09 | Starter: kafka/sink-to-postgres | The template for every starter page: about, architecture, how it works, the key code, files, `experiment.yaml` additions, run commands, exercises. |
| 10 | No experiment yet | Empty dashboard after skipping. |
| 11 | States and components | Health states, resource bars, edges, terminal block, type, service kinds. |
| 12 | Mobile overview | 390 px. |

## Visual vocabulary

- **Type:** Inter for UI and prose; Menlo for commands, addresses, env vars and code.
- **Colour:** slate neutrals. Indigo only for links and selection. Status colours (green healthy, amber near limit, red unhealthy, blue starting, grey stopped) always appear with a word.
- **Shapes:** square badge carrying the tech's logo for catalog infrastructure, round lettered badge for the user's apps.
- **Commands:** dark terminal block with a Copy control. Docs code: light block with a filename tab.

## Known limits

- Frames are static; interactions (copy, search, navigation, node selection) are specified, not prototyped.
- Contrast was chosen conservatively but not measured; keyboard and focus behaviour are not represented.
- Metrics, image versions and code are illustrative. The 85% "near limit" threshold is a proposal.
- Frames 02, 08 and 10 have not had the final "less text" pass that the dashboard received.
