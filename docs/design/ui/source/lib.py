"""Tiny builder that emits figcraft-cli `run` scripts with predicted node ids."""
import json

# ---- tokens -------------------------------------------------------------
CANVAS = "#F8FAFC"
SURFACE = "#FFFFFF"
SUBTLE = "#F1F5F9"
BORDER = "#E2E8F0"
BORDER_STRONG = "#CBD5E1"
INK = "#0F172A"
INK2 = "#334155"
MUTED = "#64748B"
FAINT = "#94A3B8"
ACCENT = "#4F46E5"
ACCENT_SOFT = "#EEF2FF"
ACCENT_INK = "#3730A3"
TERM_BG = "#0F172A"
TERM_INK = "#E2E8F0"
TERM_MUTED = "#94A3B8"

STATUS = {
    "healthy": ("Healthy", "#16A34A", "#DCFCE7", "#166534"),
    "warning": ("Warning", "#D97706", "#FEF3C7", "#92400E"),
    "unhealthy": ("Unhealthy", "#DC2626", "#FEE2E2", "#991B1B"),
    "starting": ("Starting", "#2563EB", "#DBEAFE", "#1E40AF"),
    "stopped": ("Stopped", "#64748B", "#F1F5F9", "#334155"),
}
DANGER = "#DC2626"
WARN = "#D97706"

SANS = "Inter"
MONO = "Menlo"
W = {"r": "Regular", "m": "Medium", "s": "Semi Bold", "b": "Bold"}
SHADOW = [{"type": "dropShadow", "color": "#0F172A0F", "offset": [0, 1], "radius": 2}]
SHADOW_MD = [{"type": "dropShadow", "color": "#0F172A14", "offset": [0, 4], "radius": 12},
             {"type": "dropShadow", "color": "#0F172A0A", "offset": [0, 1], "radius": 2}]


class Doc:
    def __init__(self):
        self.cmds = []
        self.n = 1

    def _add(self, cmd, params):
        self.cmds.append({"command": cmd, "params": params})
        self.n += 1
        return self.n

    # top-level screen
    def screen(self, name, x, y, w, h=None, **p):
        p.setdefault("fill", CANVAS)
        params = {"name": name, "x": x, "y": y, "width": w, "height": h or 900, "props": p}
        return self._add("frame.create", params)

    def frame(self, parent, name="", w=None, h=None, x=None, y=None, **p):
        if "fill" not in p and "fills" not in p:
            p["fills"] = []
        params = {"type": "frame", "parent": parent, "name": name}
        if w is not None:
            params["width"] = w
            p.setdefault("sizingH", "fixed")
        if h is not None:
            params["height"] = h
            p.setdefault("sizingV", "fixed")
        if x is not None:
            params["x"] = x
        if y is not None:
            params["y"] = y
        if p.get("layout"):
            p.setdefault("sizingH", "hug")
            p.setdefault("sizingV", "hug")
        params["props"] = p
        return self._add("node.create", params)

    def row(self, parent, name="row", gap=8, **p):
        p.setdefault("crossAlign", "center")
        return self.frame(parent, name, layout="horizontal", gap=gap, **p)

    def col(self, parent, name="col", gap=8, **p):
        return self.frame(parent, name, layout="vertical", gap=gap, **p)

    def text(self, parent, t, size=14, weight="r", color=INK, font=SANS, wrap=False,
             lh=None, w=None, x=None, y=None, **p):
        props = {"fontSize": size, "fontFamily": font, "fontStyle": W.get(weight, weight), "fill": color}
        if lh:
            props["lineHeight"] = lh
        if wrap:
            props["sizingH"] = "fill"
            props["textAutoResize"] = "height"
        props.update(p)
        params = {"text": t, "x": x or 0, "y": y or 0, "parent": parent, "props": props}
        if w:
            params["width"] = w
            props["textAutoResize"] = "height"
        return self._add("text.create", params)

    def rect(self, parent, w, h, x=None, y=None, **p):
        params = {"type": "rectangle", "parent": parent, "width": w, "height": h, "props": p}
        if x is not None:
            params["x"] = x
        if y is not None:
            params["y"] = y
        return self._add("node.create", params)

    def ellipse(self, parent, d, color, x=None, y=None, **p):
        params = {"type": "ellipse", "parent": parent, "width": d, "height": d, "props": {"fill": color, **p}}
        if x is not None:
            params["x"] = x
        if y is not None:
            params["y"] = y
        return self._add("node.create", params)

    def vector(self, parent, path, x, y, **p):
        return self._add("node.create", {"type": "vector", "parent": parent, "x": x, "y": y, "path": path, "props": p})

    def dump(self, path):
        with open(path, "w") as f:
            json.dump(self.cmds, f)


# ---- components ----------------------------------------------------------
def badge(d, parent, status, label=None):
    name, dot, bg, ink = STATUS[status]
    b = d.row(parent, "status", gap=6)
    d.ellipse(b, 7, dot)
    d.text(b, label or name, 12, "m", ink if status != "healthy" else INK2)
    return b


def pill(d, parent, text, fill=SUBTLE, ink=INK2, stroke=None, size=12, weight="m", font=SANS):
    p = d.row(parent, "pill", gap=6, padding=[3, 9, 3, 9], fill=fill, cornerRadius=999,
              **({"stroke": stroke} if stroke else {}))
    d.text(p, text, size, weight, ink, font=font)
    return p


def monogram(d, parent, letter, kind="infra", size=32):
    fill, ink = (SUBTLE, INK2) if kind == "infra" else (ACCENT_SOFT, ACCENT_INK)
    m = d.frame(parent, "monogram", w=size, h=size, layout="horizontal", mainAlign="center",
                crossAlign="center", fill=fill, cornerRadius=8 if kind == "infra" else size // 2)
    d.text(m, letter, int(size * 0.42), "s", ink)
    return m


def bar(d, parent, frac, w=120, high=0.85):
    track = d.frame(parent, "bar", w=w, h=6, fill=BORDER, cornerRadius=3)
    color = WARN if frac >= high else "#64748B"
    d.rect(track, max(2, int(w * frac)), 6, x=0, y=0, fill=color, cornerRadius=3)
    return track


def button(d, parent, text, kind="secondary", size=13):
    if kind == "primary":
        b = d.row(parent, "button", gap=6, padding=[8, 14, 8, 14], fill=INK, cornerRadius=8)
        d.text(b, text, size, "m", "#FFFFFF")
    elif kind == "ghost":
        b = d.row(parent, "button", gap=6, padding=[8, 10, 8, 10], cornerRadius=8)
        d.text(b, text, size, "m", INK2)
    else:
        b = d.row(parent, "button", gap=6, padding=[7, 13, 7, 13], fill=SURFACE, stroke=BORDER_STRONG,
                  cornerRadius=8, effects=SHADOW)
        d.text(b, text, size, "m", INK)
    return b


def link(d, parent, text, size=13):
    return d.text(parent, text, size, "m", ACCENT)


def terminal(d, parent, lines, title="Run in your terminal", fill_w=True, w=None):
    """lines: list of (command, comment|None)."""
    kw = {"sizingH": "fill"} if fill_w and not w else {}
    t = d.col(parent, "terminal", gap=0, fill=TERM_BG, cornerRadius=10, **({"width": w} if False else {}), **kw)
    if w:
        pass
    head = d.row(t, "terminal-head", gap=8, padding=[10, 14, 10, 14], sizingH="fill", mainAlign="spaceBetween",
                 strokeSides=[0, 0, 1, 0], stroke="#1E293B")
    d.text(head, title, 12, "m", TERM_MUTED)
    cp = d.row(head, "copy", gap=6, padding=[3, 8, 3, 8], stroke="#334155", cornerRadius=6)
    d.text(cp, "Copy", 11, "m", TERM_INK)
    body = d.col(t, "terminal-body", gap=8, padding=[14, 16, 16, 16], sizingH="fill")
    for cmd, comment in lines:
        if comment:
            d.text(body, "# " + comment, 13, "r", TERM_MUTED, font=MONO)
        r = d.row(body, "cmd", gap=10)
        d.text(r, "$", 13, "r", "#64748B", font=MONO)
        d.text(r, cmd, 13, "r", TERM_INK, font=MONO)
    return t


def code_chip(d, parent, text, dark=False):
    c = d.row(parent, "code", gap=6, padding=[3, 8, 3, 8], fill=TERM_BG if dark else SUBTLE,
              cornerRadius=6, **({} if dark else {"stroke": BORDER}))
    d.text(c, text, 12, "r", TERM_INK if dark else INK2, font=MONO)
    return c


def card(d, parent, name="card", pad=20, gap=12, **p):
    p.setdefault("sizingH", "fill")
    return d.col(parent, name, gap=gap, padding=pad, fill=SURFACE, stroke=BORDER, cornerRadius=12,
                 effects=SHADOW, **p)


def overline(d, parent, text, color=MUTED):
    return d.text(parent, text.upper(), 11, "s", color, letterSpacing="6%")


def logo(d, parent, size=15):
    r = d.row(parent, "logo", gap=8)
    g = d.frame(r, "mark", w=22, h=22, fill=INK, cornerRadius=6, layout="grid", gridColumns=3, gridRows=3,
                padding=5, gap=2, crossGap=2)
    for _ in range(9):
        d.ellipse(g, 2, "#FFFFFF")
    d.text(r, "breadboard", size, "s", INK, letterSpacing="-1%")
    return r
