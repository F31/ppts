"""注视页取样与孔内窗口（V13 / V13b / V13c）。

与 Go `internal/render/stillpage.go` 的 `isGazeLookPage` / `lookChainLayerOK`、
以及 `gazepage.go` 的 `lookWindows` / `lookHoldMid` 同口径：
gaze+entrance 带 Focus.rect 留在 look 链；稳态中点避开 240ms 交叉；
孔边 blur(8px) 不进全帧门槛，V13c 孔内再内缩 16px。
本模块不依赖 numpy / Pillow，门禁脚本与单测都能 import。
"""

from __future__ import annotations

FOCUS_FADE_MS = 240
LOOK_XFADE_MS = 120  # look 仍帧默认叠化；纱/圈选已烧进仍帧，240ms 叠两帧会闪
LOOK_ARRIVE_HOLD_MS = 400
LOOK_ARRIVE_MIN_MS = 1600
LOOK_ARRIVE_XFADE_MS = 80
LOOK_NEAR_XFADE_MS = 80
LOOK_WIPE_XFADE_MS = 140
LOOK_FAR_XFADE_MS = 160
LOOK_CROP_XFADE_MS = 200
PAGE_FADE_MS = 280  # catalog 页间 fade；HTML 在这窗口内还叠着上一页，MP4 分段没有
HOLE_RING_PX = 16
CAPTION_CROP_RATIO = 0.12
HOLE_MIN_SSIM = 0.97
ENTRANCE_EFFECTS = frozenset({"fadeIn", "slideIn", "pop", "grow"})


def _int(v, default=0):
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def _rect(obj):
    if not obj:
        return None
    w = _int(obj.get("w"))
    h = _int(obj.get("h"))
    if w <= 0 or h <= 0:
        return None
    return {
        "x": _int(obj.get("x")),
        "y": _int(obj.get("y")),
        "w": w,
        "h": h,
    }


def has_focus_rect(page):
    for span in page.get("focus") or []:
        if _rect(span.get("rect")):
            return True
    return False


def has_entrance_element_layer(page):
    for layer in page.get("layers") or []:
        if not (layer.get("elementId") or layer.get("elementID")):
            continue
        for clip in layer.get("clips") or []:
            effect = clip.get("effect") or clip.get("type") or ""
            if effect in ENTRANCE_EFFECTS:
                return True
    return False


def look_chain_layer_ok(layer):
    """无 clip 的层，以及只含入场 effect 的元素层，可以留在 look 链。"""
    clips = layer.get("clips") or []
    if not clips:
        return True
    if not (layer.get("elementId") or layer.get("elementID")):
        return False
    for clip in clips:
        effect = clip.get("effect") or clip.get("type") or ""
        if effect not in ENTRANCE_EFFECTS:
            return False
    return True


def _transition_type(page):
    tr = page.get("transition") or {}
    if isinstance(tr, str):
        return tr
    return tr.get("type") or ""


def is_gaze_look_page(page):
    """教学注视页：完整页 PNG + 纱/圈选。gaze+entrance 带 Focus.rect 也留在 look 链。"""
    if page.get("shots"):
        return False
    tr = _transition_type(page)
    if tr and tr not in ("", "none", "fade"):
        return False
    if not has_focus_rect(page):
        return False
    for layer in page.get("layers") or []:
        if not look_chain_layer_ok(layer):
            return False
    return True


def look_hold_mid(start, end, fade=0, late=False):
    """注视窗稳态取样点。前缀窗也走这里，避免 MidMs=0 采到页边界。late 采 hold 的 3/4。"""
    if end <= start:
        return start
    if fade < 0:
        fade = 0
    if fade >= end - start:
        fade = 0
    hold = end - start - fade
    if late and hold > 0:
        mid = start + fade + hold * 3 // 4
    else:
        mid = start + fade + hold // 2
    if mid <= start and end > start + 1:
        return start + 1
    if mid >= end and end > start:
        return end - 1
    return mid


def _look_travel_xfade(prev, cur, look_crop):
    if look_crop:
        return LOOK_CROP_XFADE_MS, "fade"
    if not prev or not cur:
        return LOOK_XFADE_MS, "fade"
    dx = (cur["x"] + cur["w"] / 2) - (prev["x"] + prev["w"] / 2)
    dy = (cur["y"] + cur["h"] / 2) - (prev["y"] + prev["h"] / 2)
    dist = (dx * dx + dy * dy) ** 0.5
    if dist < 48:
        return LOOK_NEAR_XFADE_MS, "fade"
    if abs(dx) >= abs(dy) * 1.4 and abs(dx) >= 80:
        return LOOK_WIPE_XFADE_MS, ("wiperight" if dx > 0 else "wipeleft")
    if abs(dy) >= abs(dx) * 1.4 and abs(dy) >= 80:
        return LOOK_WIPE_XFADE_MS, ("wipedown" if dy > 0 else "wipeup")
    if dist > 600:
        return LOOK_FAR_XFADE_MS, "fade"
    return LOOK_XFADE_MS, "fade"


def look_windows(page):
    """返回 [{startMs, endMs, fadeMs, midMs, rect, xfade}, ...]。

    算法对齐 gazepage.go：缺 Rect 的 span 丢掉；第一段之前若有空隙补前缀；
    教学页第一条 Focus 拆成 arrive+settle；切焦按位移选 wipe/fade。
    """
    duration = _int(page.get("durationMs"))
    spans = []
    for raw in page.get("focus") or []:
        rect = _rect(raw.get("rect"))
        if not rect:
            continue
        spans.append({
            "startMs": _int(raw.get("startMs")),
            "endMs": _int(raw.get("endMs")),
            "fadeMs": _int(raw.get("fadeMs")),
            "rect": rect,
        })
    if not spans or duration <= 0:
        return []

    late = has_entrance_element_layer(page)
    look_crop = bool(page.get("lookCrop"))
    out = []
    if spans[0]["startMs"] > 0:
        end = spans[0]["startMs"]
        out.append({
            "startMs": 0,
            "endMs": end,
            "fadeMs": 0,
            "midMs": look_hold_mid(0, end, 0, late),
            "rect": None,
            "xfade": "fade",
        })
    prev_rect = None
    first_rect = True
    for i, span in enumerate(spans):
        start = span["startMs"]
        end = duration
        if span["endMs"] > start:
            end = span["endMs"]
        elif i + 1 < len(spans) and spans[i + 1]["startMs"] > start:
            end = spans[i + 1]["startMs"]
        if end > duration:
            end = duration
        if end <= start:
            continue
        fade, kind = 0, "fade"
        if out:
            fade, kind = _look_travel_xfade(prev_rect, span["rect"], look_crop)
            if fade >= end - start:
                fade, kind = 0, "cut"
        dur = end - start
        split = (
            first_rect
            and not look_crop
            and dur >= LOOK_ARRIVE_MIN_MS
            and dur > LOOK_ARRIVE_HOLD_MS + LOOK_ARRIVE_XFADE_MS + 200
        )
        first_rect = False
        if split:
            arrive_end = start + LOOK_ARRIVE_HOLD_MS
            if fade >= arrive_end - start:
                fade, kind = 0, "cut"
            out.append({
                "startMs": start,
                "endMs": arrive_end,
                "fadeMs": fade,
                "midMs": look_hold_mid(start, arrive_end, fade, False),
                "rect": span["rect"],
                "xfade": kind,
            })
            settle_fade = LOOK_ARRIVE_XFADE_MS
            if settle_fade >= end - arrive_end:
                settle_fade = 0
            out.append({
                "startMs": arrive_end,
                "endMs": end,
                "fadeMs": settle_fade,
                "midMs": look_hold_mid(arrive_end, end, settle_fade, late),
                "rect": span["rect"],
                "xfade": "fade",
            })
        else:
            out.append({
                "startMs": start,
                "endMs": end,
                "fadeMs": fade,
                "midMs": look_hold_mid(start, end, fade, late),
                "rect": span["rect"],
                "xfade": kind,
            })
        prev_rect = span["rect"]
    if out and out[-1]["endMs"] < duration:
        last = out[-1]
        last["endMs"] = duration
        last["midMs"] = look_hold_mid(last["startMs"], duration, last["fadeMs"], late)
    return out


def gaze_sample_points(page, fps=30):
    """注视页取样：每条 Focus 稳态中点（避开 240ms 交叉与页间 fade），加上末帧。

    不取 0ms：页间 fade 窗口里 HTML 还叠着上一页，MP4 分段是本页静图，V13 会假红。
    前缀 look 中点若仍落在 PAGE_FADE_MS 内同样跳过。
    """
    duration = _int(page.get("durationMs"))
    frame = max(1, int(round(1000.0 / max(int(fps or 30), 1))))
    points = []
    for i, window in enumerate(look_windows(page)):
        mid = window["midMs"]
        if PAGE_FADE_MS <= mid < duration:
            points.append((f"look{i}", mid))
    last = duration - 34
    if duration > PAGE_FADE_MS + 40 and last >= PAGE_FADE_MS:
        points.append(("last", last))
    seen, unique = set(), []
    for label, ms in points:
        if ms in seen:
            continue
        seen.add(ms)
        unique.append((label, ms))
    return unique


def last_look(page):
    windows = look_windows(page)
    if not windows:
        return None
    return windows[-1]


def hole_interior(rect, ring=HOLE_RING_PX):
    """孔内窗口：Focus.rect 内缩 ring 像素，避开孔边 blur(8px) 的外扩环。"""
    if not rect:
        return None
    x, y, w, h = rect["x"], rect["y"], rect["w"], rect["h"]
    pad = max(int(ring), 0)
    ix, iy = x + pad, y + pad
    iw, ih = w - 2 * pad, h - 2 * pad
    if iw < 8 or ih < 8:
        return None
    return {"x": ix, "y": iy, "w": iw, "h": ih}


def caption_crop_height(height, ratio=CAPTION_CROP_RATIO):
    cut = int(round(int(height) * float(ratio)))
    if cut < 1 or cut >= int(height):
        return 0
    return cut


def slide_as_page(slide, duration_ms=None):
    """把成品包清单里的一页转成 look_windows 能读的 page 字典。"""
    if duration_ms is None:
        start = _int(slide.get("startUs")) / 1000.0
        end = _int(slide.get("endUs")) / 1000.0
        duration_ms = int(round(end - start))
    return {
        "durationMs": duration_ms,
        "focus": slide.get("focus") or [],
        "layers": slide.get("layers") or [],
        "shots": slide.get("shots") or [],
        "transition": slide.get("transition") or {},
        "lookCrop": bool(slide.get("lookCrop")),
        "annotations": slide.get("annotations") or [],
    }
