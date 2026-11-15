import os
import sys
from lib import *
from screens import (SERVICES, DW, GAP, app_shell, page_header, live, topbar, home, home_selected, overview,
                     services_table, attention, mini_flow)


# ---------------------------------------------------------------- 4. architecture
NODE_W, NODE_H = 176, 56


def arch_node(d, parent, name, x, y, selected=False):
    s = next(v for v in SERVICES if v[0] == name)
    _, letter, kind, desc, st, cpu, mem, addr, tls = s
    n = d.row(parent, name, gap=10, padding=[12, 14, 12, 14], x=x, y=y, w=NODE_W, h=NODE_H, fill=SURFACE,
              stroke=ACCENT if selected else BORDER, strokeWeight=2 if selected else 1, cornerRadius=12,
              effects=SHADOW_MD if selected else SHADOW)
    monogram(d, n, letter[0], kind, 28)
    t = d.row(n, "t", sizingH="fill")
    d.text(t, name, 14, "s", INK)
    d.ellipse(n, 8, STATUS[st][1])
    return n


def edge(d, parent, x1, y1, x2, y2, plain=False, label=None, lx=None, ly=None):
    col = DANGER if plain else "#94A3B8"
    mx, my = min(x1, x2), min(y1, y2)
    cx = (x1 + x2) / 2
    path = f"M {x1-mx} {y1-my} C {cx-mx} {y1-my} {cx-mx} {y2-my} {x2-mx} {y2-my}"
    d.vector(parent, path, mx, my, stroke=col, strokeWeight=1.75, fills=[], **({"dashes": [6, 4]} if plain else {}))
    # arrowhead pointing right at (x2,y2)
    d.vector(parent, "M 0 0 L 9 4.5 L 0 9 Z", x2 - 9, y2 - 4.5, fill=col)
    if label:
        lb = d.row(parent, "edge-label", gap=4, padding=[2, 7, 2, 7], x=lx, y=ly,
                   fill="#FEF2F2" if plain else SURFACE, stroke="#FCA5A5" if plain else BORDER, cornerRadius=6)
        d.text(lb, label, 11, "m", "#991B1B" if plain else INK2)


def architecture(d, x):
    sc, main = app_shell(d, x, "04 Dashboard — architecture", "Architecture", h=1024)
    page_header(d, main, "Architecture", None, lambda h: live(d, h))
    body = d.row(main, "body", gap=20, sizingH="fill", sizingV="fill", crossAlign="min")
    cw, ch = 760, 760
    canvas = d.frame(body, "canvas", sizingH="fill", h=ch, fill=SURFACE, stroke=BORDER, cornerRadius=12, clip=True)
    tb = d.row(canvas, "toolbar", gap=4, padding=4, x=16, y=16, fill=SURFACE, stroke=BORDER, cornerRadius=8,
               effects=SHADOW)
    for t in ["Fit", "−", "+"]:
        b = d.row(tb, t, padding=[4, 9, 4, 9], cornerRadius=6)
        d.text(b, t, 12, "m", INK2)
    lg = d.row(canvas, "legend", gap=16, padding=[8, 12, 8, 12], x=16, y=ch - 52, fill=SURFACE, stroke=BORDER,
               cornerRadius=8, effects=SHADOW)
    l1 = d.row(lg, "tls", gap=6)
    d.vector(l1, "M 0 0 L 22 0", 0, 0, stroke="#94A3B8", strokeWeight=1.75, fills=[])
    d.text(l1, "TLS, verified", 11, "m", INK2)
    l2 = d.row(lg, "plain", gap=6)
    d.vector(l2, "M 0 0 L 22 0", 0, 0, stroke=DANGER, strokeWeight=1.75, dashes=[5, 3], fills=[])
    d.text(l2, "Plaintext (intentional)", 11, "m", INK2)
    l3 = d.row(lg, "kinds", gap=6)
    monogram(d, l3, "K", "infra", 16)
    d.text(l3, "infrastructure", 11, "m", INK2)
    monogram(d, l3, "A", "app", 16)
    d.text(l3, "your code", 11, "m", INK2)
    # layout: apps left, kafka middle, stores right
    P = {"producer": (24, 130), "consumer": (24, 400), "kafka": (296, 130), "postgres": (296, 540),
         "redis": (556, 400)}
    d.text(canvas, "YOUR CODE", 11, "s", FAINT, x=24, y=96, letterSpacing="6%")
    d.text(canvas, "INFRASTRUCTURE", 11, "s", FAINT, x=296, y=96, letterSpacing="6%")

    def right(n, dy=0): return P[n][0] + NODE_W, P[n][1] + NODE_H / 2 + dy
    def left(n, dy=0): return P[n][0], P[n][1] + NODE_H / 2 + dy
    edge(d, canvas, *right("producer"), *left("kafka"), label="SSL :9093", lx=212, ly=160)
    edge(d, canvas, *right("consumer", -20), *left("kafka", 20), label="SSL :9093", lx=212, ly=300)
    edge(d, canvas, *right("consumer", 4), *left("redis", 4), plain=True, label="plaintext :6380", lx=330, ly=432)
    edge(d, canvas, *right("consumer", 28), *left("postgres"), label="TLS :5432", lx=212, ly=520)
    for n, (nx, ny) in P.items():
        arch_node(d, canvas, n, nx, ny, selected=(n == "consumer"))
    # detail panel
    pnl = d.col(body, "inspector", gap=18, padding=20, w=340, fill=SURFACE, stroke=BORDER, cornerRadius=12,
                effects=SHADOW)
    h = d.row(pnl, "h", gap=10)
    monogram(d, h, "C", "app", 32)
    t = d.col(h, "t", gap=2)
    d.text(t, "consumer", 16, "s", INK)
    d.text(t, "app · ./orders · cmd/consumer", 12, "r", MUTED)
    badge(d, pnl, "warning", "Near limit")
    sec = d.col(pnl, "conn", gap=8, sizingH="fill")
    overline(d, sec, "Connects to")
    for tgt, proto, ok in [("kafka", "SSL :9093", True), ("postgres", "TLS :5432", True),
                           ("redis", "Plaintext :6380", False)]:
        r = d.row(sec, tgt, gap=8, padding=[9, 12, 9, 12], sizingH="fill", fill=CANVAS if ok else "#FEF2F2",
                  stroke=BORDER if ok else "#FECACA", cornerRadius=8, mainAlign="spaceBetween")
        d.text(r, tgt, 13, "s", INK)
        d.text(r, proto, 12, "r", INK2 if ok else "#991B1B", font=MONO)
    d.text(sec, "Reason: measure TLS overhead on cache reads", 12, "r", "#991B1B", wrap=True, lh="150%")
    sec2 = d.col(pnl, "res", gap=8, sizingH="fill")
    overline(d, sec2, "Resources")
    for label, frac, val in [("CPU", 0.76, "0.19 / 0.25"), ("Memory", 0.92, "236 / 256 MiB")]:
        r = d.row(sec2, label, gap=10, sizingH="fill", mainAlign="spaceBetween")
        d.text(r, label, 12, "m", INK2)
        bar(d, r, frac, 120)
        d.text(r, val, 12, "r", INK2, features={"tnum": True})
    link(d, pnl, "Open service page →")
    return sc
    return sc


# ---------------------------------------------------------------- 5. service detail
def kv(d, parent, k, v, mono=False):
    r = d.row(parent, k, gap=12, sizingH="fill", mainAlign="spaceBetween", crossAlign="min")
    d.text(r, k, 13, "r", MUTED)
    d.text(r, v, 12 if mono else 13, "r" if mono else "m", INK, font=MONO if mono else SANS)
    return r


def table(d, parent, cols, rows, warn_col=None):
    t = d.col(parent, "table", gap=0, sizingH="fill", stroke=BORDER, cornerRadius=10, clip=True)
    h = d.row(t, "head", padding=[8, 14, 8, 14], sizingH="fill", fill=CANVAS, strokeSides=[0, 0, 1, 0], stroke=BORDER)
    for label, w in cols:
        c = d.row(h, label, **({"w": w} if w else {"sizingH": "fill"}))
        d.text(c, label, 12, "m", MUTED)
    for i, row in enumerate(rows):
        r = d.row(t, "r", padding=[10, 14, 10, 14], sizingH="fill",
                  **({"strokeSides": [0, 0, 1, 0], "stroke": BORDER} if i < len(rows) - 1 else {}))
        for j, ((label, w), val) in enumerate(zip(cols, row)):
            c = d.row(r, label, gap=6, **({"w": w} if w else {"sizingH": "fill"}))
            warn = warn_col is not None and j == warn_col and isinstance(val, tuple)
            if warn:
                val = val[0]
            d.text(c, str(val), 13, "s" if warn else ("m" if j == 0 else "r"), "#92400E" if warn else INK,
                   font=MONO if j == 0 else SANS, features={"tnum": True})
    return t


def service_detail(d, x):
    sc, main = app_shell(d, x, "05 Dashboard — service: kafka", "kafka", h=1120)
    crumbs = d.row(main, "crumbs", gap=6)
    d.text(crumbs, "Services", 13, "m", MUTED)
    d.text(crumbs, "/", 13, "r", FAINT)
    d.text(crumbs, "kafka", 13, "m", INK2)
    hdr = d.row(main, "header", gap=16, sizingH="fill", mainAlign="spaceBetween")
    l = d.row(hdr, "l", gap=14)
    monogram(d, l, "K", "infra", 44)
    t = d.col(l, "t", gap=4)
    tr = d.row(t, "title", gap=10)
    d.text(tr, "kafka", 24, "s", INK, letterSpacing="-1%")
    badge(d, tr, "healthy")
    d.text(t, "Apache Kafka 4.1.0 · single broker in KRaft combined mode · up 2h 14m", 14, "r", MUTED)
    live(d, hdr)
    tabs = d.row(main, "tabs", gap=24, sizingH="fill", strokeSides=[0, 0, 1, 0], stroke=BORDER)
    for i, tname in enumerate(["Overview", "Topics & groups", "Connection", "Logs", "Learn"]):
        tb = d.col(tabs, tname, gap=0, padding=[0, 0, 10, 0],
                   **({"strokeSides": [0, 0, 2, 0], "stroke": INK} if i == 0 else {}))
        d.text(tb, tname, 13, "s" if i == 0 else "m", INK if i == 0 else MUTED)
    cols = d.row(main, "cols", gap=24, sizingH="fill", crossAlign="min")
    left = d.col(cols, "left", gap=20, sizingH="fill")
    w = card(d, left, "what", pad=24, gap=10)
    d.text(w, "What you're running", 16, "s", INK)
    d.text(w, "One node is both controller and broker (KRaft). Replication factor is 1: if this container restarts, "
              "every partition is unavailable until it returns.",
           14, "r", INK2, wrap=True, lh="165%")
    tg = card(d, left, "topics", pad=24, gap=14)
    th = d.row(tg, "h", sizingH="fill", mainAlign="spaceBetween")
    d.text(th, "Topics and consumer groups", 16, "s", INK)
    table(d, tg, [("Topic", 200), ("Partitions", 110), ("Retention", 110), ("Messages", None)],
          [("orders", "3", "7 days", "48,213"), ("__consumer_offsets", "50", "compacted", "—")])
    table(d, tg, [("Group", 200), ("Members", 110), ("State", 110), ("Total lag", None)],
          [("orders-sink", "1", "Stable", ("1,204 messages",))], warn_col=3)
    ex = d.row(left, "experiments", gap=16, sizingH="fill", crossAlign="min")
    for title, kind, body, cmds in [
        ("Try this", "Watch a rebalance", "Add a second consumer app to experiment.yaml with the same group, then "
         "follow both logs as partitions move between members.", [("bb up", None), ("bb logs consumer-2 -f", None)]),
        ("Break this", "Kill the broker mid-stream", "Stop Kafka while the producer is writing. Watch producer "
         "retries, then restart it and check that no committed message is lost.",
         [("docker stop bb-kafka-lab-kafka-1", None), ("bb up", None)])]:
        c = card(d, ex, title, pad=20, gap=10)
        overline(d, c, title, ACCENT_INK if title == "Try this" else "#991B1B")
        d.text(c, kind, 15, "s", INK)
        d.text(c, body, 13, "r", INK2, wrap=True, lh="160%")
        terminal(d, c, cmds, title="Terminal")
    right = d.col(cols, "right", gap=20, w=360)
    cn = card(d, right, "connection", pad=20, gap=12)
    d.text(cn, "Connection", 15, "s", INK)
    kv(d, cn, "Address", "kafka.bb.internal:9093", True)
    kv(d, cn, "Protocol", "SSL (TLS 1.3)")
    kv(d, cn, "Client auth", "None in v1")
    kv(d, cn, "Used by", "producer, consumer")
    sep = d.rect(cn, 10, 1, fill=BORDER, sizingH="fill")
    overline(d, cn, "Env vars injected into apps")
    for k, v in [("KAFKA_BROKERS", "kafka.bb.internal:9093"), ("KAFKA_SECURITY_PROTOCOL", "SSL"),
                 ("SSL_CERT_FILE", "/etc/bb/trust-bundle.pem")]:
        r = d.col(cn, k, gap=2, sizingH="fill")
        d.text(r, k, 12, "s", INK, font=MONO)
        d.text(r, v, 12, "r", MUTED, font=MONO)
    rs = card(d, right, "resources", pad=20, gap=12)
    d.text(rs, "Resources", 15, "s", INK)
    for label, frac, val in [("CPU", 0.42, "0.42 / 1.00 cores"), ("Memory", 0.70, "712 / 1024 MiB")]:
        r = d.col(rs, label, gap=6, sizingH="fill")
        rr = d.row(r, "l", sizingH="fill", mainAlign="spaceBetween")
        d.text(rr, label, 13, "m", INK2)
        d.text(rr, val, 12, "r", INK2, features={"tnum": True})
        bar(d, r, frac, 318)
    ce = card(d, right, "cert", pad=20, gap=10)
    d.text(ce, "Certificate", 15, "s", INK)
    kv(d, ce, "Issuer", "Breadboard root CA")
    kv(d, ce, "Name", "kafka.bb.internal", True)
    kv(d, ce, "Expires", "in 83 days")
    d.text(ce, "spiffe://bb.internal/experiment/kafka-lab/service/kafka", 11, "r", MUTED, font=MONO, wrap=True)
    return sc


# ---------------------------------------------------------------- 6. docs
def docs(d, x):
    sc = d.screen("08 Docs — Kafka: consumer groups", x, 0, DW, 1300, layout="vertical", sizingV="fixed")
    topbar(d, sc, "Docs")
    body = d.row(sc, "body", gap=0, sizingH="fill", sizingV="fill", crossAlign="min")
    nav = d.col(body, "doc-nav", gap=20, padding=[28, 20, 28, 24], w=264, sizingV="fill", fill=SURFACE,
                strokeSides=[0, 1, 0, 0], stroke=BORDER)
    for sys_name, items, open_ in [
        ("Getting started", ["How Breadboard works", "Experiments and experiment.yaml", "TLS by default"], False),
        ("Kafka", ["Overview", "Topics and partitions", "Consumer groups", "Offsets and delivery", "Rebalancing",
                   "Kafka in Breadboard"], True),
        ("Postgres", [], False), ("Redis", [], False), ("Consul", [], False), ("S3 Compatible", [], False)]:
        g = d.col(nav, sys_name, gap=2, sizingH="fill")
        hr = d.row(g, "h", padding=[0, 8, 4, 8], sizingH="fill", mainAlign="spaceBetween")
        d.text(hr, sys_name, 13, "s", INK)
        if not items:
            d.text(hr, "›", 13, "m", FAINT)
        for it in items:
            on = it == "Consumer groups"
            r = d.row(g, it, padding=[6, 8, 6, 12], sizingH="fill", cornerRadius=6,
                      **({"fill": ACCENT_SOFT} if on else {}))
            d.text(r, it, 13, "s" if on else "r", ACCENT_INK if on else INK2)
    content = d.row(body, "content", gap=48, padding=[40, 56, 56, 64], sizingH="fill", crossAlign="min")
    art = d.col(content, "article", gap=20, w=720)
    cr = d.row(art, "crumbs", gap=6)
    d.text(cr, "Docs  /  Kafka", 13, "m", MUTED)
    d.text(art, "Consumer groups", 34, "s", INK, letterSpacing="-2%")
    d.text(art, "A consumer group is a set of consumers that share the work of reading a topic. Kafka assigns each "
                "partition to exactly one member of the group, so partitions are the unit of parallelism: a group "
                "can never have more active members than the topic has partitions.", 17, "r", INK2, wrap=True,
           lh="165%")
    yo = d.row(art, "your-experiment", gap=14, padding=[14, 16, 14, 16], sizingH="fill", fill=ACCENT_SOFT,
               stroke="#C7D2FE", cornerRadius=10, crossAlign="min")
    d.text(yo, "◉", 14, "s", ACCENT)
    yc = d.col(yo, "t", gap=4, sizingH="fill")
    d.text(yc, "In kafka-lab right now", 13, "s", ACCENT_INK)
    d.text(yc, "orders has 3 partitions and the group orders-sink has 1 member, so consumer owns all three. Its lag "
               "is 1,204 messages.", 13, "r", INK2, wrap=True, lh="155%")
    link(d, yc, "Open orders-sink in the dashboard →")
    # diagram
    dg = d.col(art, "diagram", gap=10, sizingH="fill")
    fig = d.frame(dg, "figure", sizingH="fill", h=212, fill=CANVAS, stroke=BORDER, cornerRadius=12)
    topic = d.col(fig, "topic", gap=8, padding=14, x=32, y=36, w=230, fill=SURFACE, stroke=BORDER, cornerRadius=10,
                  effects=SHADOW)
    d.text(topic, "topic: orders", 12, "s", INK, font=MONO)
    for i in range(3):
        p = d.row(topic, f"p{i}", gap=8, padding=[8, 10, 8, 10], sizingH="fill", fill=CANVAS, cornerRadius=6,
                  stroke=BORDER)
        d.text(p, f"partition {i}", 12, "m", INK2)
        for _ in range(5):
            d.rect(p, 10, 10, fill=BORDER_STRONG, cornerRadius=2)
    grp = d.col(fig, "group", gap=10, padding=14, x=430, y=36, w=230, fill=SURFACE, stroke=ACCENT,
                cornerRadius=10, dashes=[5, 4])
    d.text(grp, "group: orders-sink", 12, "s", ACCENT_INK, font=MONO)
    for c, parts in [("consumer-1", "owns p0, p1"), ("consumer-2", "owns p2")]:
        m = d.row(grp, c, gap=8, padding=[8, 10, 8, 10], sizingH="fill", fill=ACCENT_SOFT, cornerRadius=6,
                  mainAlign="spaceBetween")
        d.text(m, c, 12, "s", INK)
        d.text(m, parts, 11, "r", INK2)
    for (y1, y2) in [(86, 86), (122, 92), (158, 136)]:
        d.vector(fig, f"M 0 {y1-80} C 80 {y1-80} 80 {y2-80} 160 {y2-80}", 262, 80, stroke="#A5B4FC",
                 strokeWeight=1.5, fills=[])
        d.vector(fig, "M 0 0 L 9 4.5 L 0 9 Z", 422, y2 - 4.5, fill="#A5B4FC")
    d.text(dg, "Figure 1. Two members split three partitions. Add a third member and each owns one; add a fourth "
               "and it sits idle.", 13, "r", MUTED, wrap=True, lh="155%")
    d.text(art, "Joining a group", 22, "s", INK, letterSpacing="-1%")
    d.text(art, "Membership is a client setting, not something you create on the broker. Any client that sets the "
                "same group ID joins the same group. With franz-go that's one option:", 15, "r", INK2, wrap=True,
           lh="170%")
    code = d.col(art, "code", gap=0, sizingH="fill", fill=CANVAS, stroke=BORDER, cornerRadius=10)
    ch = d.row(code, "tab", gap=8, padding=[8, 14, 8, 14], sizingH="fill", mainAlign="spaceBetween",
               strokeSides=[0, 0, 1, 0], stroke=BORDER)
    d.text(ch, "orders/cmd/consumer/main.go", 12, "m", INK2, font=MONO)
    d.text(ch, "Copy", 12, "m", MUTED)
    cb = d.col(code, "lines", gap=4, padding=[14, 16, 16, 16], sizingH="fill")
    lines = [
        ("client, err := kgo.NewClient(", INK),
        ("    kgo.SeedBrokers(strings.Split(os.Getenv(\"KAFKA_BROKERS\"), \",\")...),", INK),
        ("    kgo.ConsumerGroup(\"orders-sink\"),   // same ID = same group", INK),
        ("    kgo.ConsumeTopics(\"orders\"),", INK),
        ("    // Trusts SSL_CERT_FILE, which Breadboard points at its CA bundle.", MUTED),
        ("    kgo.DialTLSConfig(&tls.Config{}),", INK),
        (")", INK),
    ]
    for ln, c in lines:
        d.text(cb, ln, 13, "r", c, font=MONO)
    d.text(art, "What triggers a rebalance", 22, "s", INK, letterSpacing="-1%")
    for b in ["A member joins or leaves the group, including a crash or an OOM kill.",
              "A member misses heartbeats for longer than session.timeout.ms.",
              "The partition count of a subscribed topic changes."]:
        r = d.row(art, "li", gap=10, crossAlign="min", sizingH="fill")
        d.text(r, "—", 15, "r", FAINT)
        d.text(r, b, 15, "r", INK2, wrap=True, lh="160%")
    toc = d.col(content, "toc", gap=10, w=200)
    overline(d, toc, "On this page")
    for i, t in enumerate(["Overview", "Joining a group", "What triggers a rebalance", "Assignment strategies",
                           "Try it in Breadboard"]):
        d.text(toc, t, 13, "m" if i == 0 else "r", INK if i == 0 else MUTED)
    d.frame(toc, "space", h=12, w=10)
    overline(d, toc, "Starter")
    code_chip(d, toc, "kafka/producer-consumer")
    return sc


# ---------------------------------------------------------------- 7. empty (after skip)
def empty(d, x):
    sc, main = app_shell(d, x, "10 Dashboard — no experiment yet", "Overview", experiment=False, h=1024)
    page_header(d, main, "Overview", "No experiment.yaml in ~/code/scratch")
    wrap = d.col(main, "wrap", gap=0, sizingH="fill", crossAlign="center", padding=[24, 0, 0, 0])
    box = d.col(wrap, "empty", gap=20, padding=36, w=760, fill=SURFACE, stroke=BORDER, cornerRadius=16,
                effects=SHADOW)
    d.text(box, "Nothing is running here yet", 22, "s", INK, letterSpacing="-1%")
    d.text(box, "bb ui shows the experiment in the directory you started it from. Create one with a single service, "
                "or start from a starter that includes working code.", 15, "r", INK2, wrap=True, lh="165%")
    two = d.row(box, "options", gap=16, sizingH="fill", crossAlign="min")
    a = d.col(two, "blank", gap=10, sizingH="fill")
    overline(d, a, "Start blank")
    terminal(d, a, [("bb init my-lab", None), ("bb add kafka", None), ("bb up", None)])
    b = d.col(two, "starter", gap=10, sizingH="fill")
    overline(d, b, "Or start from a scenario")
    for t, s in [("Kafka producers & consumers", "kafka/producer-consumer"),
                 ("Stream events into Postgres", "kafka/sink-to-postgres"),
                 ("Cache invalidation", "redis/cache-invalidation")]:
        r = d.row(b, t, gap=8, padding=[10, 12, 10, 12], sizingH="fill", fill=CANVAS, stroke=BORDER,
                  cornerRadius=8, mainAlign="spaceBetween")
        c = d.col(r, "t", gap=2)
        d.text(c, t, 13, "s", INK)
        d.text(c, s, 11, "r", MUTED, font=MONO)
        d.text(r, "→", 14, "m", MUTED)
    ft = d.row(box, "foot", gap=16, sizingH="fill", padding=[16, 0, 0, 0], strokeSides=[1, 0, 0, 0], stroke=BORDER)
    d.text(ft, "New to Breadboard?", 13, "r", MUTED)
    link(d, ft, "How experiments work →")
    link(d, ft, "Browse the catalog →")
    return sc


# ---------------------------------------------------------------- 8. states & components
def states(d, x):
    sc = d.screen("11 Reference — states & components", x, 0, DW, 860, layout="vertical", padding=56, gap=36,
                  sizingV="fixed")
    d.text(sc, "States and components", 28, "s", INK, letterSpacing="-1%")
    d.text(sc, "Every status pairs a colour with a word, so meaning survives without colour.", 15, "r", MUTED)
    row = d.row(sc, "health", gap=16, sizingH="fill", crossAlign="min")
    for st, desc in [("starting", "Container is up; its health check hasn't passed yet. Kafka often takes the longest."),
                     ("healthy", "Health check passing over TLS with certificate verification."),
                     ("warning", "Healthy, but at or above 85% of a CPU or memory limit. Threshold is a proposal."),
                     ("unhealthy", "Health check failing. Shows the last check output and links to logs."),
                     ("stopped", "Defined in experiment.yaml but not running. Start it with bb up.")]:
        c = card(d, row, st, pad=16, gap=10)
        badge(d, c, st)
        d.text(c, desc, 13, "r", INK2, wrap=True, lh="155%")
    r2 = d.row(sc, "r2", gap=24, sizingH="fill", crossAlign="min")
    a = card(d, r2, "bars", pad=20, gap=14)
    d.text(a, "Resource bars", 15, "s", INK)
    for frac, label in [(0.3, "30% · neutral"), (0.7, "70% · neutral"), (0.86, "86% · warning tone"),
                        (0.98, "98% · OOM kill likely")]:
        r = d.row(a, label, gap=12)
        bar(d, r, frac, 200)
        d.text(r, label, 12, "r", INK2)
    b = card(d, r2, "edges", pad=20, gap=14)
    d.text(b, "Edges", 15, "s", INK)
    e = d.frame(b, "e", sizingH="fill", h=90)
    edge(d, e, 10, 20, 260, 20, label="produce · :9093", lx=90, ly=8)
    edge(d, e, 10, 70, 260, 70, plain=True, label="plaintext · :6380", lx=86, ly=58)
    d.text(b, "Plaintext edges always show their reason from experiment.yaml and are isolated on a private network.",
           12, "r", MUTED, wrap=True, lh="155%")
    c = card(d, r2, "commands", pad=20, gap=14)
    d.text(c, "Commands, not buttons", 15, "s", INK)
    d.text(c, "The UI never changes your system. Anything that would be an action is a copyable terminal block.",
           12, "r", MUTED, wrap=True, lh="155%")
    terminal(d, c, [("bb add redis", "add a service"), ("bb up", None)], title="Terminal")
    r3 = d.row(sc, "r3", gap=24, sizingH="fill", crossAlign="min")
    t = card(d, r3, "type", pad=20, gap=10)
    d.text(t, "Type: Inter for UI and prose, Menlo for anything you'd type or read in a terminal", 15, "s", INK)
    d.text(t, "Page title 24 semibold · Section 16 semibold · Body 14/22 · Docs body 15–17/1.65 · Meta 12", 13, "r",
           INK2)
    d.text(t, "kafka.bb.internal:9093 · KAFKA_BROKERS · bb starter kafka/producer-consumer", 13, "r", INK2, font=MONO)
    k = card(d, r3, "kinds", pad=20, gap=10, w=420, sizingH="fixed")
    d.text(k, "Service kinds", 15, "s", INK)
    rr = d.row(k, "k", gap=12)
    monogram(d, rr, "K", "infra", 32)
    d.text(rr, "Infrastructure from the catalog (square)", 13, "r", INK2)
    rr = d.row(k, "a", gap=12)
    monogram(d, rr, "C", "app", 32)
    d.text(rr, "Your code, built from a Dockerfile (round)", 13, "r", INK2)
    return sc


# ---------------------------------------------------------------- 9. mobile overview
def mobile(d, x):
    sc = d.screen("12 Mobile — overview (390)", x, 0, 390, 960, layout="vertical", sizingV="fixed")
    t = d.row(sc, "topbar", padding=[0, 16, 0, 16], sizingH="fill", h=56, fill=SURFACE, strokeSides=[0, 0, 1, 0],
              stroke=BORDER, mainAlign="spaceBetween")
    logo(d, t, 14)
    m = d.row(t, "menu", padding=[6, 10, 6, 10], stroke=BORDER, cornerRadius=8)
    d.text(m, "Menu", 12, "m", INK2)
    body = d.col(sc, "body", gap=16, padding=16, sizingH="fill")
    e = d.row(body, "exp", gap=8, sizingH="fill", mainAlign="spaceBetween")
    l = d.col(e, "l", gap=2)
    d.text(l, "kafka-lab", 20, "s", INK)
    d.text(l, "5 containers · up 2h 14m", 12, "r", MUTED)
    live(d, e)
    a = d.col(body, "attention", gap=8, padding=14, sizingH="fill", fill="#FFFBEB", stroke="#FCD34D", cornerRadius=12)
    d.text(a, "consumer is close to its memory limit", 14, "s", INK, wrap=True)
    d.text(a, "236 / 256 MiB (92%). OOMKilled and restarted 2× in 10 min.", 13, "r", INK2, wrap=True, lh="150%")
    link(d, a, "What to change →")
    overline(d, body, "Containers")
    for name, letter, kind, desc, st, cpu, mem, addr, tls in SERVICES:
        c = d.col(body, name, gap=10, padding=14, sizingH="fill", fill="#FFFBEB" if st == "warning" else SURFACE,
                  stroke=BORDER, cornerRadius=12)
        r = d.row(c, "h", gap=10, sizingH="fill", mainAlign="spaceBetween")
        ll = d.row(r, "l", gap=10)
        monogram(d, ll, letter[0], kind, 28)
        tt = d.col(ll, "t", gap=1)
        d.text(tt, name, 14, "s", INK)
        d.text(tt, addr, 11, "r", MUTED, font=MONO)
        badge(d, r, st, "Near limit" if st == "warning" else None)
        rs = d.row(c, "res", gap=16, sizingH="fill")
        for label, u, lim, fmt in [("CPU", cpu[0], cpu[1], "{:.2f}/{:.2f}"), ("Mem", mem[0], mem[1], "{}/{} MiB")]:
            cc = d.col(rs, label, gap=4, sizingH="fill")
            rr = d.row(cc, "l", sizingH="fill", mainAlign="spaceBetween")
            d.text(rr, label, 11, "m", MUTED)
            hi = u / lim >= .85
            d.text(rr, fmt.format(u, lim), 11, "s" if hi else "r", "#92400E" if hi else INK2, features={"tnum": True})
            bar(d, cc, u / lim, 160)
    d.text(body, "Architecture and docs are in the menu. The graph view needs a wider screen; on phones it falls "
                 "back to a list of connections.", 12, "r", MUTED, wrap=True, lh="150%")
    return sc


from screens3 import docs_home, kafka_overview, starter_page
ALL = [home, home_selected, overview, architecture, service_detail, docs_home, kafka_overview, docs, starter_page, empty, states, mobile]

if __name__ == "__main__":
    d = Doc()
    names = sys.argv[1:]
    fns = [f for f in ALL if not names or f.__name__ in names]
    x = 0
    for f in fns:
        f(d, x)
        x += (390 if f is mobile else DW) + GAP
    d.dump(os.path.join(os.path.dirname(os.path.abspath(__file__)), "out.json"))
    print(d.n)
