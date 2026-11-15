import sys
from lib import *

DW = 1440  # desktop width
GAP = 160

SERVICES = [
    # name, letter, kind, desc, status, cpu(used,limit), mem(used,limit MiB), addr, tls
    ("kafka", "K", "infra", "apache/kafka 4.1.0 · KRaft", "healthy", (0.42, 1.00), (712, 1024), "kafka.bb.internal:9093", "TLS"),
    ("postgres", "P", "infra", "postgres 18.1", "healthy", (0.08, 0.50), (96, 512), "postgres.bb.internal:5432", "TLS"),
    ("redis", "R", "infra", "redis 8.2", "healthy", (0.02, 0.25), (14, 128), "redis.bb.internal:6379", "TLS"),
    ("producer", "Pr", "app", "./orders · cmd/producer", "healthy", (0.11, 0.25), (38, 128), "producer.bb.internal", "TLS"),
    ("consumer", "Co", "app", "./orders · cmd/consumer", "warning", (0.19, 0.25), (236, 256), "consumer.bb.internal", "Plaintext"),
]


# ---------------------------------------------------------------- shell
def topbar(d, parent, active=None, search=True):
    t = d.row(parent, "topbar", gap=24, padding=[0, 24, 0, 24], sizingH="fill", h=56, fill=SURFACE,
              strokeSides=[0, 0, 1, 0], stroke=BORDER, mainAlign="spaceBetween")
    left = d.row(t, "left", gap=28)
    logo(d, left)
    if search:
        s = d.row(left, "search", gap=8, padding=[7, 10, 7, 12], w=360, fill=CANVAS, stroke=BORDER, cornerRadius=8,
                  mainAlign="spaceBetween")
        d.text(s, "Search docs, starters, services", 13, "r", MUTED)
        k = d.row(s, "kbd", padding=[1, 6, 1, 6], fill=SURFACE, stroke=BORDER, cornerRadius=4)
        d.text(k, "⌘K", 11, "m", MUTED)
    right = d.row(t, "right", gap=20)
    for label in ["Dashboard", "Docs", "Starters"]:
        d.text(right, label, 13, "m", INK if label == active else MUTED)
    d.text(right, "Read-only", 12, "m", FAINT)
    return t


def sidebar(d, parent, active="Overview", experiment=True):
    s = d.col(parent, "sidebar", gap=24, padding=[20, 16, 20, 16], w=248, sizingV="fill", fill=SURFACE,
              strokeSides=[0, 1, 0, 0], stroke=BORDER)
    # experiment block
    e = d.col(s, "experiment", gap=6, padding=12, sizingH="fill", fill=CANVAS, stroke=BORDER, cornerRadius=10)
    overline(d, e, "Experiment")
    if experiment:
        r = d.row(e, "name", gap=8)
        d.ellipse(r, 8, STATUS["healthy"][1])
        d.text(r, "kafka-lab", 15, "s", INK)
        d.text(e, "~/code/kafka-lab", 12, "r", MUTED, font=MONO)
        d.text(e, "5 services · up 2h 14m", 12, "r", MUTED)
    else:
        d.text(e, "None in this directory", 14, "s", INK2)
        d.text(e, "~/code/scratch", 12, "r", MUTED, font=MONO)

    def group(title, items):
        g = d.col(s, title, gap=2, sizingH="fill")
        if title:
            pad = d.row(g, "h", padding=[0, 8, 6, 8])
            overline(d, pad, title)
        for label, sub, dim in items:
            on = label == active
            it = d.row(g, label, gap=10, padding=[7, 10, 7, 10], sizingH="fill", cornerRadius=8,
                       mainAlign="spaceBetween", **({"fill": ACCENT_SOFT} if on else {}))
            d.text(it, label, 13, "m" if not on else "s", ACCENT_INK if on else (FAINT if dim else INK2))
            if sub:
                sub(it)
        return g

    def dot(status):
        return lambda p: d.ellipse(p, 7, STATUS[status][1])

    dim = not experiment
    group(None, [("Overview", None, dim), ("Architecture", None, dim)])
    if experiment:
        group("Services", [(n, dot(st), False) for n, _, _, _, st, *_ in SERVICES])
    group("Learn", [("Docs", None, False), ("Starters", None, False), ("Catalog", None, False)])
    sp = d.frame(s, "spacer", sizingH="fill", sizingV="fill")
    foot = d.col(s, "footer", gap=2, padding=[0, 10, 0, 10])
    d.text(foot, "bb v0.1.0", 12, "m", MUTED)
    d.text(foot, "domain bb.internal", 12, "r", FAINT)
    return s


def app_shell(d, x, name, active, experiment=True, h=1024, top_active="Dashboard"):
    sc = d.screen(name, x, 0, DW, h, layout="vertical", sizingV="fixed")
    topbar(d, sc, top_active)
    body = d.row(sc, "body", gap=0, sizingH="fill", sizingV="fill", crossAlign="min")
    sidebar(d, body, active, experiment)
    main = d.col(body, "main", gap=24, padding=[32, 40, 40, 40], sizingH="fill", sizingV="fill")
    return sc, main


def page_header(d, parent, title, sub=None, right=None):
    h = d.row(parent, "page-header", gap=16, sizingH="fill", mainAlign="spaceBetween", crossAlign="max")
    l = d.col(h, "titles", gap=4)
    d.text(l, title, 24, "s", INK, letterSpacing="-1%")
    if sub:
        d.text(l, sub, 14, "r", MUTED)
    if right:
        right(h)
    return h


def live(d, parent):
    r = d.row(parent, "live", gap=6)
    d.ellipse(r, 7, STATUS["healthy"][1])
    d.text(r, "Live", 12, "m", MUTED)
    return r


# ---------------------------------------------------------------- 1. home
SUGGESTIONS = [
    ("Kafka producers & consumers", "Topics, partitions, keys, consumer groups and rebalancing.", ["Kafka"]),
    ("Stream events into Postgres", "Consume, transform and write idempotently with at-least-once delivery.", ["Kafka", "Postgres"]),
    ("Cache invalidation", "Cache-aside reads with invalidate-on-write driven by Kafka events.", ["Redis", "Kafka"]),
    ("Service discovery", "Register apps with a Consul sidecar and resolve peers by name.", ["Consul"]),
    ("Object storage basics", "Buckets, prefixes, multipart uploads and presigned URLs.", ["S3 Compatible"]),
]


def home_top(d, sc):
    t = d.row(sc, "topbar", gap=24, padding=[0, 32, 0, 32], sizingH="fill", h=64, mainAlign="spaceBetween")
    logo(d, t)
    r = d.row(t, "right", gap=24)
    d.text(r, "Docs", 13, "m", MUTED)
    d.text(r, "Starters", 13, "m", MUTED)
    button(d, r, "Skip to dashboard  →")


def home_heading(d, col):
    d.text(col, "What do you want to build?", 40, "s", INK, letterSpacing="-2%", textAlign="center")
    d.text(col, "Pick a scenario to see its architecture and how to run it.", 16, "r", MUTED, textAlign="center")


def search_box(d, col, value=None):
    s = d.row(col, "search", gap=12, padding=[14, 16, 14, 16], sizingH="fill", fill=SURFACE, stroke=BORDER_STRONG,
              cornerRadius=10, mainAlign="spaceBetween")
    d.text(s, value or "Search starters and docs", 15, "r", INK if value else FAINT)
    k = d.row(s, "kbd", padding=[2, 7, 2, 7], fill=CANVAS, stroke=BORDER, cornerRadius=5)
    d.text(k, "⌘K", 12, "m", MUTED)


def suggestion_card(d, parent, title, desc, tags, selected=False):
    c = d.col(parent, "suggestion", gap=10, padding=18, sizingH="fill", minHeight=150, fill=SURFACE,
              stroke=ACCENT if selected else BORDER, strokeWeight=1.5 if selected else 1, cornerRadius=12,
              effects=SHADOW)
    d.text(c, title, 15, "s", INK)
    d.text(c, desc, 13, "r", MUTED, wrap=True, lh="150%")
    d.text(c, " · ".join(tags), 12, "m", INK2)
    return c


def home(d, x):
    sc = d.screen("01 Home — suggestions", x, 0, DW, 1024, layout="vertical", sizingV="fixed")
    home_top(d, sc)
    wrap = d.col(sc, "content", gap=0, sizingH="fill", sizingV="fill", crossAlign="center", padding=[56, 0, 0, 0])
    col = d.col(wrap, "column", gap=20, w=880, crossAlign="center")
    home_heading(d, col)
    d.frame(col, "space", h=12, w=10)
    search_box(d, col)
    d.frame(col, "space", h=8, w=10)
    rows = [d.row(col, "suggestions-row", gap=12, sizingH="fill", crossAlign="min") for _ in range(2)]
    for i, (t, desc, tags) in enumerate(SUGGESTIONS):
        suggestion_card(d, rows[i // 3], t, desc, tags)
    more = d.col(rows[1], "browse", gap=8, padding=18, sizingH="fill", minHeight=150, fill=CANVAS, stroke=BORDER,
                 cornerRadius=12, dashes=[4, 4], mainAlign="center")
    d.text(more, "Browse all starters", 15, "s", INK)
    link(d, more, "Open catalog →")
    return sc


# ---------------------------------------------------------------- 2. home: selected suggestion
def mini_flow(d, parent, nodes, w):
    """nodes: list of (label, kind). Horizontal chain with arrows."""
    r = d.row(parent, "flow", gap=0, sizingH="fill", mainAlign="spaceBetween")
    for i, (label, kind, sub) in enumerate(nodes):
        n = d.col(r, label, gap=4, padding=[10, 12, 10, 12], fill=SURFACE, stroke=BORDER, cornerRadius=10,
                  crossAlign="center", effects=SHADOW)
        rr = d.row(n, "id", gap=8)
        monogram(d, rr, label[0].upper(), kind, 22)
        d.text(rr, label, 13, "s", INK)
        d.text(n, sub, 11, "r", MUTED)
        if i < len(nodes) - 1:
            a = d.frame(r, "arrow", w=40, h=12)
            d.vector(a, "M 0 6 L 32 6", 0, 0, stroke=FAINT, strokeWeight=1.5, fills=[])
            d.vector(a, "M 0 0 L 8 4 L 0 8 Z", 31, 2, fill=FAINT)
    return r


def home_selected(d, x):
    sc = d.screen("02 Home — scenario preview", x, 0, DW, 1024, layout="vertical", sizingV="fixed")
    home_top(d, sc)
    wrap = d.col(sc, "content", gap=0, sizingH="fill", sizingV="fill", crossAlign="center", padding=[40, 0, 0, 0])
    col = d.col(wrap, "column", gap=16, w=880)
    back = d.row(col, "back", gap=6)
    d.text(back, "←  All scenarios", 13, "m", MUTED)
    p = card(d, col, "preview", pad=0, gap=0)
    head = d.col(p, "head", gap=8, padding=[24, 28, 20, 28], sizingH="fill")
    d.text(head, "kafka/sink-to-postgres", 13, "r", MUTED, font=MONO)
    d.text(head, "Stream events into Postgres", 26, "s", INK, letterSpacing="-1%")
    d.text(head, "A producer writes order events to Kafka. A sink consumer reads them, transforms each one and "
                 "upserts it into Postgres. You'll see why at-least-once delivery means your writes must be "
                 "idempotent.", 15, "r", INK2, wrap=True, lh="160%")
    flow = d.col(p, "architecture", gap=10, padding=[20, 28, 24, 28], sizingH="fill", fill=CANVAS,
                 strokeSides=[1, 0, 1, 0], stroke=BORDER)
    overline(d, flow, "Architecture")
    mini_flow(d, flow, [("producer", "app", "Go · franz-go"), ("kafka", "infra", "topic: orders · 3 partitions"),
                        ("sink", "app", "Go · franz-go + pgx"), ("postgres", "infra", "table: orders")], 820)
    lower = d.row(p, "lower", gap=32, padding=[24, 28, 28, 28], sizingH="fill", crossAlign="min")
    left = d.col(lower, "learn", gap=12, sizingH="fill")
    overline(d, left, "What you'll work through")
    for item in ["Consumer groups and partition assignment",
                 "Offsets, commits and at-least-once delivery",
                 "Idempotent upserts with ON CONFLICT",
                 "Schema migrations for the sink table",
                 "Break it: kill the sink mid-batch and replay"]:
        r = d.row(left, "item", gap=10, crossAlign="min")
        d.text(r, "—", 14, "r", FAINT)
        d.text(r, item, 14, "r", INK2)
    svc = d.col(left, "services", gap=6, padding=[8, 0, 0, 0], sizingH="fill")
    overline(d, svc, "Containers it adds (default limits)")
    lst = d.col(svc, "list", gap=0, sizingH="fill", stroke=BORDER, cornerRadius=8)
    items = [("kafka", "infra", "1.00 CPU", "1 GiB"), ("postgres", "infra", "0.50 CPU", "512 MiB"),
             ("producer", "app", "0.25 CPU", "128 MiB"), ("sink", "app", "0.25 CPU", "256 MiB")]
    for i, (n, k, c_, m_) in enumerate(items):
        r = d.row(lst, n, gap=10, padding=[8, 12, 8, 12], sizingH="fill",
                  **({"strokeSides": [0, 0, 1, 0], "stroke": BORDER} if i < len(items) - 1 else {}))
        monogram(d, r, n[0].upper(), k, 20)
        nm = d.row(r, "n", sizingH="fill")
        d.text(nm, n, 13, "m", INK)
        d.text(r, c_ + "  ·  " + m_, 12, "r", MUTED, font=MONO)
    right = d.col(lower, "start", gap=12, w=400)
    overline(d, right, "Start it")
    terminal(d, right, [("mkdir orders-lab && cd orders-lab", None), ("bb init orders-lab", None),
                        ("bb starter kafka/sink-to-postgres", None), ("bb up", None)])
    d.text(right, "First run downloads images. Kafka is usually the last container to report healthy.", 12, "r",
           MUTED, wrap=True, lh="150%")
    acts = d.row(right, "actions", gap=8)
    button(d, acts, "Read the guide")
    button(d, acts, "View starter code", "ghost")
    return sc


# ---------------------------------------------------------------- 3. overview
def stat(d, parent, label, value, sub=None, tone=None):
    c = card(d, parent, "stat", pad=16, gap=4)
    d.text(c, label, 12, "m", MUTED)
    d.text(c, value, 22, "s", INK, features={"tnum": True})
    return c


COLS = [("Service", 260), ("Status", 124), ("CPU", 170), ("Memory", 190), ("Address", 220), ("Edges", None)]


def services_table(d, parent):
    c = card(d, parent, "services", pad=0, gap=0)
    head = d.row(c, "title", gap=8, padding=[16, 20, 16, 20], sizingH="fill")
    d.text(head, "Containers", 15, "s", INK)
    hr = d.row(c, "header", gap=0, padding=[8, 20, 8, 20], sizingH="fill", fill=CANVAS,
               strokeSides=[1, 0, 1, 0], stroke=BORDER)
    for label, w in COLS:
        cell = d.row(hr, label, w=w) if w else d.row(hr, label, sizingH="fill")
        d.text(cell, label, 12, "m", MUTED)
    for i, (name, letter, kind, desc, st, cpu, mem, addr, tls) in enumerate(SERVICES):
        row = d.row(c, name, gap=0, padding=[14, 20, 14, 20], sizingH="fill",
                    **({"strokeSides": [0, 0, 1, 0], "stroke": BORDER} if i < len(SERVICES) - 1 else {}),
                    )
        c1 = d.row(row, "svc", gap=12, w=260)
        monogram(d, c1, letter[0], kind, 32)
        t = d.col(c1, "t", gap=2)
        nr = d.row(t, "n", gap=6)
        d.text(nr, name, 14, "s", INK)
        d.text(t, desc, 12, "r", MUTED)
        c2 = d.row(row, "status", w=124)
        badge(d, c2, st, "Near limit" if st == "warning" else None)
        c3 = d.col(row, "cpu", gap=6, w=170)
        bar(d, c3, cpu[0] / cpu[1], 150)
        d.text(c3, f"{cpu[0]:.2f} / {cpu[1]:.2f} cores", 12, "r", INK2, features={"tnum": True})
        c4 = d.col(row, "mem", gap=6, w=190)
        bar(d, c4, mem[0] / mem[1], 150)
        hi = mem[0] / mem[1] >= 0.85
        d.text(c4, f"{mem[0]} / {mem[1]} MiB", 12,
               "s" if hi else "r", "#92400E" if hi else INK2, features={"tnum": True})
        c5 = d.row(row, "addr", w=220)
        d.text(c5, addr, 12, "r", INK2, font=MONO)
        c6 = d.row(row, "tls", gap=6, sizingH="fill")
        if tls == "TLS":
            d.text(c6, "TLS", 12, "m", INK2)
        else:
            d.vector(c6, "M 0 0 L 14 0", 0, 0, stroke=DANGER, strokeWeight=1.5, dashes=[3, 2], fills=[])
            d.text(c6, tls, 12, "m", "#991B1B")
    return c


def attention(d, parent):
    a = d.row(parent, "attention", gap=16, padding=[16, 20, 16, 20], sizingH="fill", fill="#FFFBEB",
              stroke="#FCD34D", cornerRadius=12, crossAlign="min")
    icon = d.frame(a, "icon", w=28, h=28, fill="#FEF3C7", cornerRadius=14, layout="horizontal", mainAlign="center",
                   crossAlign="center")
    d.text(icon, "!", 14, "b", "#92400E")
    body = d.col(a, "body", gap=6, sizingH="fill")
    d.text(body, "consumer is close to its memory limit", 15, "s", INK)
    d.text(body, "236 MiB of 256 MiB (92%). Docker killed it for exceeding the limit (OOMKilled) and restarted it "
                 "2 times in the last 10 minutes. Each restart triggers a consumer-group rebalance.", 14, "r", INK2,
           wrap=True, lh="155%")
    r = d.row(body, "fix", gap=10, padding=[4, 0, 0, 0])
    d.text(r, "To raise it, set", 13, "r", INK2)
    code_chip(d, r, "apps.consumer.resources.memory: 512m")
    d.text(r, "then run", 13, "r", INK2)
    code_chip(d, r, "bb up", dark=True)
    links = d.col(a, "links", gap=8, w=240)
    link(d, links, "Why containers get OOMKilled →")
    link(d, links, "Rebalancing in consumer groups →")
    return a


def mini_graph(d, parent, w=520, h=230):
    c = d.frame(parent, "mini-graph", w=w, h=h, fill=CANVAS, cornerRadius=10, stroke=BORDER, clip=True)
    mid = (w - 116) / 2
    pos = {"producer": (40, 24), "kafka": (mid, 24), "consumer": (40, 110), "redis": (w - 156, 110),
           "postgres": (mid, 186)}
    edges = [("producer", "kafka", False), ("consumer", "kafka", False), ("consumer", "postgres", False),
             ("consumer", "redis", True)]
    nw, nh = 116, 40
    for a, b, plain in edges:
        ax, ay = pos[a]; bx, by = pos[b]
        x1, y1 = ax + nw, ay + nh / 2
        x2, y2 = bx, by + nh / 2
        if bx < ax:
            x1, x2 = ax, bx + nw
        col = DANGER if plain else FAINT
        mx = min(x1, x2); my = min(y1, y2)
        path = f"M {x1-mx} {y1-my} C {(x1+x2)/2-mx} {y1-my} {(x1+x2)/2-mx} {y2-my} {x2-mx} {y2-my}"
        d.vector(c, path, mx, my, stroke=col, strokeWeight=1.5, fills=[], **({"dashes": [5, 4]} if plain else {}))
    for name, (x, y) in pos.items():
        st = next(s for s in SERVICES if s[0] == name)
        n = d.row(c, name, gap=8, padding=[8, 10, 8, 10], x=x, y=y, w=nw, fill=SURFACE, stroke=BORDER,
                  cornerRadius=8, effects=SHADOW)
        d.ellipse(n, 7, STATUS[st[4]][1])
        d.text(n, name, 12, "s", INK)
    return c


def overview(d, x):
    sc, main = app_shell(d, x, "03 Dashboard — overview", "Overview", h=1064)
    page_header(d, main, "kafka-lab", "~/code/kafka-lab · up 2h 14m", lambda h: live(d, h))
    stats = d.row(main, "stats", gap=16, sizingH="fill", crossAlign="min")
    stat(d, stats, "Containers", "5")
    stat(d, stats, "Healthy", "4 of 5")
    stat(d, stats, "TLS edges", "3 of 4")
    stat(d, stats, "Memory", "1.07 / 2.00 GiB")
    services_table(d, main)
    g = card(d, main, "architecture", pad=20, gap=14)
    gh = d.row(g, "h", sizingH="fill", mainAlign="spaceBetween")
    d.text(gh, "Architecture", 15, "s", INK)
    link(d, gh, "Open →")
    mini_graph(d, g, 1072, 250)
    return sc


if __name__ == "__main__":
    d = Doc()
    which = sys.argv[1:] or ["home", "home_selected", "overview"]
    x = 0
    for w in which:
        globals()[w](d, x)
        x += DW + GAP
    import os; d.dump(os.path.join(os.path.dirname(os.path.abspath(__file__)), "out.json"))
    print(d.n)
