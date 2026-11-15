from lib import *
from screens import SERVICES, DW, topbar, mini_flow


def doc_nav(d, body, active):
    nav = d.col(body, "doc-nav", gap=20, padding=[28, 20, 28, 24], w=264, sizingV="fill", fill=SURFACE,
                strokeSides=[0, 1, 0, 0], stroke=BORDER)
    for sys_name, items in [
        ("Getting started", ["How Breadboard works", "Experiments", "TLS by default"]),
        ("Kafka", ["Overview", "Topics and partitions", "Consumer groups", "Offsets and delivery", "Rebalancing",
                   "Kafka in Breadboard"]),
        ("Postgres", []), ("Redis", []), ("Consul", []), ("S3 Compatible", [])]:
        g = d.col(nav, sys_name, gap=2, sizingH="fill")
        hr = d.row(g, "h", padding=[0, 8, 4, 8], sizingH="fill", mainAlign="spaceBetween")
        d.text(hr, sys_name, 13, "s", INK)
        if not items:
            d.text(hr, "›", 13, "m", FAINT)
        for it in items:
            on = it == active
            r = d.row(g, it, padding=[6, 8, 6, 12], sizingH="fill", cornerRadius=6,
                      **({"fill": ACCENT_SOFT} if on else {}))
            d.text(r, it, 13, "s" if on else "r", ACCENT_INK if on else INK2)
    return nav


def code_block(d, parent, filename, lines):
    code = d.col(parent, "code", gap=0, sizingH="fill", fill=SURFACE, stroke=BORDER, cornerRadius=10)
    ch = d.row(code, "tab", gap=8, padding=[10, 16, 10, 16], sizingH="fill", mainAlign="spaceBetween",
               strokeSides=[0, 0, 1, 0], stroke=BORDER)
    d.text(ch, filename, 12, "m", INK2, font=MONO)
    d.text(ch, "Copy", 12, "m", MUTED)
    cb = d.col(code, "lines", gap=4, padding=[14, 16, 16, 16], sizingH="fill", fill=CANVAS, cornerRadius=[0, 0, 10, 10])
    for ln in lines:
        d.text(cb, ln if ln else " ", 12.5, "r", MUTED if ln.strip().startswith("//") else INK, font=MONO)
    return code


def prose(d, parent, text, size=15):
    return d.text(parent, text, size, "r", INK2, wrap=True, lh="170%")


def section_title(d, parent, text):
    return d.text(parent, text, 20, "s", INK, letterSpacing="-1%")


SYSTEMS = [
    ("Kafka", "K", "Partitioned, replicated event log.", "6 guides · 3 starters"),
    ("Postgres", "P", "Relational database with strong consistency.", "5 guides · 2 starters"),
    ("Redis", "R", "In-memory key-value store and cache.", "4 guides · 2 starters"),
    ("Consul", "C", "Service discovery and health checking.", "4 guides · 1 starter"),
    ("S3 Compatible", "S", "Object storage over the S3 API.", "3 guides · 1 starter"),
    ("Getting started", "B", "How experiments, TLS and starters work.", "3 guides"),
]


def docs_home(d, x):
    sc = d.screen("06 Docs — home", x, 0, DW, 760, layout="vertical", sizingV="fixed")
    topbar(d, sc, "Docs")
    wrap = d.col(sc, "content", gap=0, sizingH="fill", sizingV="fill", crossAlign="center", padding=[56, 0, 0, 0])
    col = d.col(wrap, "column", gap=28, w=1040)
    h = d.col(col, "h", gap=6)
    d.text(h, "Docs", 32, "s", INK, letterSpacing="-2%")
    d.text(h, "Concepts, configuration and code for every system in the catalog.", 15, "r", MUTED)
    rows = [d.row(col, "row", gap=16, sizingH="fill", crossAlign="min") for _ in range(2)]
    for i, (name, letter, desc, meta) in enumerate(SYSTEMS):
        c = card(d, rows[i // 3], name, pad=22, gap=14)
        monogram(d, c, letter, "infra", 40)
        t = d.col(c, "t", gap=4, sizingH="fill")
        d.text(t, name, 17, "s", INK)
        d.text(t, desc, 13, "r", MUTED, wrap=True, lh="150%")
        d.text(c, meta, 12, "m", INK2)
    return sc


def kafka_overview(d, x):
    sc = d.screen("07 Docs — Kafka overview", x, 0, DW, 1300, layout="vertical", sizingV="fixed")
    topbar(d, sc, "Docs")
    body = d.row(sc, "body", gap=0, sizingH="fill", sizingV="fill", crossAlign="min")
    doc_nav(d, body, "Overview")
    content = d.col(body, "content", gap=36, padding=[40, 64, 56, 64], sizingH="fill")
    # hero
    hero = d.row(content, "hero", gap=20, sizingH="fill", mainAlign="spaceBetween", crossAlign="min")
    hl = d.row(hero, "l", gap=18)
    monogram(d, hl, "K", "infra", 56)
    ht = d.col(hl, "t", gap=6)
    d.text(ht, "Docs  /  Kafka", 13, "m", MUTED)
    d.text(ht, "Kafka", 34, "s", INK, letterSpacing="-2%")
    d.text(ht, "A partitioned, replicated log. Producers append, consumers read at their own pace.", 16, "r", INK2)
    add = d.col(hero, "add", gap=8, w=320)
    terminal(d, add, [("bb add kafka", None)], title="Add to an experiment")
    what = d.row(content, "what", gap=40, sizingH="fill", crossAlign="min")
    wl = d.col(what, "explain", gap=14, w=640)
    section_title(d, wl, "What is Kafka?")
    prose(d, wl, "Kafka stores events in an append-only log. Producers write records to a topic; Kafka keeps them, "
                 "in order, for a retention period whether or not anyone has read them yet. Consumers read the log "
                 "at their own pace and track their position with an offset.")
    prose(d, wl, "That one design choice decouples writers from readers. A slow consumer doesn't slow the producer, "
                 "a new consumer can replay history from the beginning, and many independent services can read the "
                 "same events for different purposes.")
    prose(d, wl, "To scale, a topic is split into partitions. Order is guaranteed within a partition, not across "
                 "them, and the record's key decides which partition it lands in.")
    wr = d.col(what, "use", gap=16, sizingH="fill")
    for title, items, ink in [("Good fit", ["Event streams between services", "Change data capture",
                                           "Replayable audit and activity logs", "High-throughput ingestion"], INK),
                              ("Poor fit", ["Request/response RPC", "Small queues with per-message ack",
                                            "Ad-hoc queries over stored data"], INK)]:
        c = card(d, wr, title, pad=18, gap=8)
        d.text(c, title, 14, "s", ink)
        for it in items:
            r = d.row(c, it, gap=10, crossAlign="min")
            d.text(r, "—", 13, "r", FAINT)
            d.text(r, it, 13, "r", INK2)
    # diagram
    fig = d.frame(content, "figure", sizingH="fill", h=216, fill=SURFACE, stroke=BORDER, cornerRadius=12)
    def box(x, y, w, title, items, accent=False):
        b = d.col(fig, title, gap=8, padding=14, x=x, y=y, w=w, fill=ACCENT_SOFT if accent else CANVAS,
                  stroke="#C7D2FE" if accent else BORDER, cornerRadius=10)
        d.text(b, title, 12, "s", ACCENT_INK if accent else INK, font=MONO)
        for it in items:
            r = d.row(b, it, padding=[7, 10, 7, 10], sizingH="fill", fill=SURFACE, stroke=BORDER, cornerRadius=6)
            d.text(r, it, 12, "m", INK2)
        return b
    box(124, 40, 170, "producers", ["producer"])
    box(414, 28, 230, "topic: orders", ["partition 0", "partition 1", "partition 2"])
    box(764, 40, 200, "group: orders-sink", ["consumer"], accent=True)
    for (x1, y1, x2, y2) in [(294, 96, 414, 96), (644, 96, 764, 96)]:
        d.vector(fig, f"M 0 0 L {x2-x1-9} 0", x1, y1, stroke=FAINT, strokeWeight=1.5, fills=[])
        d.vector(fig, "M 0 0 L 9 4.5 L 0 9 Z", x2 - 9, y1 - 4.5, fill=FAINT)
    d.text(fig, "append", 11, "m", MUTED, x=334, y=76)
    d.text(fig, "read + commit offsets", 11, "m", MUTED, x=650, y=76)
    # concepts
    cs = d.col(content, "concepts", gap=14, sizingH="fill")
    section_title(d, cs, "Go deeper")
    concepts = [("Topics and partitions", "How data is split and ordered."),
                ("Consumer groups", "How readers share a topic."),
                ("Offsets and delivery", "At-most, at-least and exactly once."),
                ("Rebalancing", "What happens when members change."),
                ("Producers and keys", "Keys decide the partition."),
                ("Kafka in Breadboard", "KRaft, TLS listener, env vars.")]
    rows = [d.row(cs, "row", gap=14, sizingH="fill", crossAlign="min") for _ in range(2)]
    for i, (t, sub) in enumerate(concepts):
        c = card(d, rows[i // 3], t, pad=18, gap=6)
        r = d.row(c, "h", sizingH="fill", mainAlign="spaceBetween")
        d.text(r, t, 14, "s", INK)
        d.text(r, "→", 14, "m", FAINT)
        d.text(c, sub, 13, "r", MUTED)
    # breadboard specifics + starters
    lower = d.row(content, "lower", gap=20, sizingH="fill", crossAlign="min")
    bb = card(d, lower, "in-breadboard", pad=20, gap=10)
    d.text(bb, "In Breadboard", 15, "s", INK)
    for k, v in [("Image", "apache/kafka 4.1.0"), ("Mode", "KRaft, single node"), ("Listener", "SSL :9093"),
                 ("Env", "KAFKA_BROKERS")]:
        r = d.row(bb, k, sizingH="fill", mainAlign="spaceBetween")
        d.text(r, k, 13, "r", MUTED)
        d.text(r, v, 12, "r", INK, font=MONO)
    st = card(d, lower, "starters", pad=20, gap=10)
    d.text(st, "Starters", 15, "s", INK)
    for name, sid in [("Kafka producers & consumers", "kafka/producer-consumer"),
                      ("Stream events into Postgres", "kafka/sink-to-postgres"),
                      ("Cache invalidation", "redis/cache-invalidation")]:
        r = d.row(st, name, sizingH="fill", mainAlign="spaceBetween", padding=[6, 0, 6, 0])
        d.text(r, name, 13, "m", INK)
        d.text(r, sid, 12, "r", MUTED, font=MONO)
    return sc


def starter_page(d, x):
    sc = d.screen("09 Starters — kafka/sink-to-postgres", x, 0, DW, 1980, layout="vertical", sizingV="fixed")
    topbar(d, sc, "Starters")
    wrap = d.col(sc, "content", gap=0, sizingH="fill", sizingV="fill", crossAlign="center", padding=[40, 0, 0, 0])
    col = d.col(wrap, "column", gap=28, w=1120)
    h = d.row(col, "header", gap=20, sizingH="fill", mainAlign="spaceBetween", crossAlign="max")
    ht = d.col(h, "t", gap=6)
    d.text(ht, "Starters  /  kafka", 13, "m", MUTED)
    d.text(ht, "Stream events into Postgres", 30, "s", INK, letterSpacing="-2%")
    d.text(ht, "kafka/sink-to-postgres  ·  Go 1.27.4  ·  Kafka, Postgres", 13, "r", MUTED, font=MONO)
    cols = d.row(col, "cols", gap=28, sizingH="fill", crossAlign="min")
    left = d.col(cols, "left", gap=24, sizingH="fill")
    ab = d.col(left, "about", gap=12, sizingH="fill")
    section_title(d, ab, "About this example")
    prose(d, ab, "An order service publishes events to Kafka. A separate sink reads them and keeps a Postgres table "
                 "in sync. It's the shape of most event-driven pipelines: the producer never talks to the database, "
                 "and the sink can be stopped, replayed or scaled without the producer noticing.")
    a = card(d, left, "architecture", pad=20, gap=12)
    d.text(a, "Architecture", 15, "s", INK)
    mini_flow(d, a, [("producer", "app", "franz-go"), ("kafka", "infra", "orders · 3 partitions"),
                     ("sink", "app", "franz-go + pgx"), ("postgres", "infra", "orders table")], 680)
    hw = d.col(left, "how", gap=12, sizingH="fill")
    section_title(d, hw, "How it works")
    for i, (t, body) in enumerate([
        ("The producer keys each event by order ID",
         "Same key, same partition. All events for one order stay in order."),
        ("The sink joins the consumer group orders-sink",
         "Kafka assigns it all three partitions. Start a second sink and they split."),
        ("Each record becomes an upsert",
         "INSERT … ON CONFLICT (id) DO UPDATE means applying the same event twice leaves the same row."),
        ("Offsets are committed after the write",
         "Crash before the commit and the batch is redelivered. That's at-least-once delivery, and step 3 is "
         "why it's safe.")], start=1):
        r = d.row(hw, f"step{i}", gap=14, sizingH="fill", crossAlign="min")
        n = d.frame(r, "n", w=24, h=24, fill=ACCENT_SOFT, cornerRadius=12, layout="horizontal", mainAlign="center",
                    crossAlign="center")
        d.text(n, str(i), 12, "s", ACCENT_INK)
        tc = d.col(r, "t", gap=3, sizingH="fill")
        d.text(tc, t, 14, "s", INK)
        d.text(tc, body, 14, "r", INK2, wrap=True, lh="160%")
    cd = d.col(left, "code", gap=12, sizingH="fill")
    section_title(d, cd, "The sink loop")
    code_block(d, cd, "orders/cmd/sink/main.go", [
        "client, _ := kgo.NewClient(",
        "    kgo.SeedBrokers(os.Getenv(\"KAFKA_BROKERS\")),",
        "    kgo.ConsumerGroup(\"orders-sink\"),",
        "    kgo.ConsumeTopics(\"orders\"),",
        "    kgo.DisableAutoCommit(), // we decide when a batch is done",
        ")",
        "",
        "for {",
        "    fetches := client.PollFetches(ctx)",
        "    fetches.EachRecord(func(r *kgo.Record) {",
        "        var o Order",
        "        _ = json.Unmarshal(r.Value, &o)",
        "        // Idempotent: replaying this record changes nothing.",
        "        _, _ = db.Exec(ctx, `INSERT INTO orders (id, total, status)",
        "            VALUES ($1, $2, $3)",
        "            ON CONFLICT (id) DO UPDATE",
        "            SET total = EXCLUDED.total, status = EXCLUDED.status`,",
        "            o.ID, o.Total, o.Status)",
        "    })",
        "    // Commit only after every row in the batch is written.",
        "    _ = client.CommitUncommittedOffsets(ctx)",
        "}",
    ])
    d.text(cd, "Error handling is trimmed here; the generated file handles retries and shutdown.", 12, "r", MUTED)
    f = card(d, left, "files", pad=20, gap=10)
    d.text(f, "Files it creates", 15, "s", INK)
    tree = d.col(f, "tree", gap=0, sizingH="fill", stroke=BORDER, cornerRadius=8)
    files = [("orders/", "", 0), ("go.mod", "franz-go, pgx/v5", 1), ("Dockerfile", "dev + prod stages", 1),
             ("cmd/producer/main.go", "writes order events", 1), ("cmd/sink/main.go", "consume → upsert", 1),
             ("migrations/001_orders.sql", "orders table", 1), ("README.md", "try this / break this", 1)]
    for i, (fn, note, depth) in enumerate(files):
        r = d.row(tree, fn, gap=8, padding=[8, 12, 8, 12 + depth * 18], sizingH="fill", mainAlign="spaceBetween",
                  **({"strokeSides": [0, 0, 1, 0], "stroke": BORDER} if i < len(files) - 1 else {}))
        d.text(r, fn, 12, "s" if depth == 0 else "r", INK, font=MONO)
        d.text(r, note, 12, "r", MUTED)
    y = card(d, left, "yaml", pad=0, gap=0)
    yh = d.row(y, "h", padding=[12, 16, 12, 16], sizingH="fill", mainAlign="spaceBetween", strokeSides=[0, 0, 1, 0],
               stroke=BORDER)
    d.text(yh, "Added to experiment.yaml", 13, "s", INK)
    d.text(yh, "Copy", 12, "m", MUTED)
    yb = d.col(y, "lines", gap=3, padding=[12, 16, 14, 16], sizingH="fill", fill=CANVAS, cornerRadius=[0, 0, 12, 12])
    for ln in ["services:", "  kafka:    { type: kafka }", "  postgres: { type: postgres }", "apps:", "  producer:",
               "    path: ./orders", "    command: [go, run, ./cmd/producer]", "    uses: [kafka]", "  sink:",
               "    path: ./orders", "    command: [go, run, ./cmd/sink]", "    uses: [kafka, postgres]"]:
        d.text(yb, ln, 12, "r", INK2, font=MONO)
    right = d.col(cols, "right", gap=20, w=380)
    terminal(d, right, [("bb init orders-lab", None), ("bb starter kafka/sink-to-postgres", None), ("bb up", None)],
             title="Run it")
    cv = card(d, right, "covers", pad=20, gap=10)
    d.text(cv, "Covers", 15, "s", INK)
    for t in ["Consumer groups", "At-least-once delivery", "Idempotent upserts", "Schema migrations"]:
        d.text(cv, t, 13, "r", INK2)
    ex = card(d, right, "exercises", pad=20, gap=10)
    d.text(ex, "Exercises", 15, "s", INK)
    for kind, t in [("Try", "Add a second sink and watch the rebalance"),
                    ("Try", "Replay from offset 0 and confirm no duplicates"),
                    ("Break", "Kill the sink mid-batch"),
                    ("Break", "Drop the unique index, then replay")]:
        r = d.row(ex, t, gap=10, sizingH="fill", crossAlign="min")
        d.text(r, kind, 12, "s", ACCENT_INK if kind == "Try" else "#991B1B", w=40)
        d.text(r, t, 13, "r", INK2, wrap=True, lh="150%")
    lb = card(d, right, "libs", pad=20, gap=10)
    d.text(lb, "Libraries", 15, "s", INK)
    for lib_ in ["github.com/twmb/franz-go", "github.com/jackc/pgx/v5"]:
        d.text(lb, lib_, 12, "r", INK2, font=MONO)
    return sc
