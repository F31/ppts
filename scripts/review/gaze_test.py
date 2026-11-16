#!/usr/bin/env python3
"""注视门禁取样的纯函数单测。不启动浏览器、不读成片。"""

from __future__ import annotations

import os
import unittest

from gaze import (
    FOCUS_FADE_MS,
    HOLE_MIN_SSIM,
    PAGE_FADE_MS,
    caption_crop_height,
    gaze_sample_points,
    hole_interior,
    is_gaze_look_page,
    last_look,
    look_hold_mid,
    look_windows,
    slide_as_page,
)


def rect(x, y, w, h):
    return {"x": x, "y": y, "w": w, "h": h}


def focus(start, element, box, fade=240, end=0):
    return {
        "startMs": start,
        "elementId": element,
        "rect": box,
        "fadeMs": fade,
        "endMs": end,
    }


class LookWindowsTest(unittest.TestCase):
    def test_five_spotlights_over_80s_match_go(self):
        page = {
            "durationMs": 80000,
            "focus": [
                focus(0, "a", rect(1, 1, 10, 10)),
                focus(16000, "b", rect(1, 1, 10, 10)),
                focus(32000, "c", rect(1, 1, 10, 10)),
                focus(48000, "d", rect(1, 1, 10, 10)),
                focus(64000, "e", rect(1, 1, 10, 10)),
            ],
        }
        got = look_windows(page)
        self.assertEqual(len(got), 5)
        self.assertEqual([w["startMs"] for w in got], [0, 16000, 32000, 48000, 64000])
        self.assertEqual([w["endMs"] for w in got], [16000, 32000, 48000, 64000, 80000])
        self.assertEqual([w["fadeMs"] for w in got], [0, 240, 240, 240, 240])
        self.assertEqual(got[0]["midMs"], 8000)
        for w in got:
            self.assertGreaterEqual(w["midMs"], w["startMs"] + w["fadeMs"])
            self.assertLess(w["midMs"], w["endMs"])

    def test_prefix_before_first_rect(self):
        page = {
            "durationMs": 4000,
            "focus": [
                {"startMs": 0, "elementId": "a"},
                focus(1000, "b", rect(1, 1, 8, 8)),
                focus(2000, "c", rect(2, 2, 8, 8)),
            ],
        }
        got = look_windows(page)
        self.assertEqual(len(got), 3)
        self.assertEqual((got[0]["startMs"], got[0]["endMs"], got[0]["fadeMs"], got[0]["midMs"]),
                         (0, 1000, 0, 500))
        self.assertIsNone(got[0]["rect"])
        self.assertEqual(look_hold_mid(0, 1000, 0), 500)
        self.assertEqual(got[1]["rect"]["w"], 8)

    def test_last_look_extends_to_page_end(self):
        page = {
            "durationMs": 5000,
            "focus": [focus(0, "a", rect(10, 10, 40, 20), fade=0, end=2000)],
        }
        got = look_windows(page)
        self.assertEqual(len(got), 1)
        self.assertEqual(got[0]["endMs"], 5000)
        self.assertEqual(got[0]["midMs"], 2500)


class GazePageTest(unittest.TestCase):
    def test_spotlight_page_is_gaze(self):
        page = {
            "durationMs": 8000,
            "focus": [focus(0, "a", rect(8, 8, 80, 40))],
            "layers": [],
            "shots": [],
            "transition": {"type": "fade", "durationMs": 280},
        }
        self.assertTrue(is_gaze_look_page(page))

    def test_entrance_layer_with_focus_stays_gaze(self):
        page = {
            "durationMs": 8000,
            "focus": [focus(0, "a", rect(8, 8, 80, 40))],
            "layers": [{"elementId": "a", "clips": [{"effect": "fadeIn", "startMs": 0}]}],
        }
        self.assertTrue(is_gaze_look_page(page))

    def test_bob_layer_is_not_gaze(self):
        page = {
            "durationMs": 8000,
            "focus": [focus(0, "a", rect(8, 8, 80, 40))],
            "layers": [{"elementId": "a", "clips": [{"effect": "bob", "startMs": 0}]}],
        }
        self.assertFalse(is_gaze_look_page(page))

    def test_entrance_without_focus_rect_is_not_gaze(self):
        page = {
            "durationMs": 8000,
            "focus": [],
            "layers": [{"elementId": "a", "clips": [{"effect": "fadeIn", "startMs": 0}]}],
        }
        self.assertFalse(is_gaze_look_page(page))

    def test_shots_are_not_gaze(self):
        page = {
            "durationMs": 8000,
            "focus": [focus(0, "a", rect(8, 8, 80, 40))],
            "shots": [{"index": 0, "startMs": 0, "durationMs": 8000}],
        }
        self.assertFalse(is_gaze_look_page(page))

    def test_sample_points_are_hold_midpoints(self):
        page = {
            "durationMs": 80000,
            "focus": [
                focus(0, "a", rect(1, 1, 10, 10)),
                focus(16000, "b", rect(1, 1, 10, 10)),
                focus(32000, "c", rect(1, 1, 10, 10)),
                focus(48000, "d", rect(1, 1, 10, 10)),
                focus(64000, "e", rect(1, 1, 10, 10)),
            ],
        }
        points = dict(gaze_sample_points(page, 30))
        self.assertEqual(points["look0"], 8000)
        self.assertGreaterEqual(points["look1"], 16000 + FOCUS_FADE_MS)
        self.assertLess(points["look1"], 32000)
        self.assertNotIn("first", points)
        self.assertIn("last", points)
        self.assertGreaterEqual(PAGE_FADE_MS, 240)

    def test_samples_skip_page_fade_window(self):
        page = {
            "durationMs": 4000,
            "focus": [focus(420, "a", rect(8, 8, 80, 40))],
        }
        points = dict(gaze_sample_points(page, 30))
        self.assertNotIn("first", points)
        self.assertTrue(all(ms >= PAGE_FADE_MS for ms in points.values()))

    def test_last_look_is_veiled_not_source(self):
        page = {
            "durationMs": 80000,
            "focus": [
                focus(0, "a", rect(1, 1, 10, 10)),
                focus(64000, "e", rect(40, 40, 80, 40)),
            ],
        }
        last = last_look(page)
        self.assertEqual(last["startMs"], 64000)
        self.assertEqual(last["endMs"], 80000)
        self.assertIsNotNone(last["rect"])


class HoleInteriorTest(unittest.TestCase):
    def test_insets_16px_ring(self):
        inner = hole_interior(rect(20, 30, 200, 100), ring=16)
        self.assertEqual(inner, {"x": 36, "y": 46, "w": 168, "h": 68})

    def test_tiny_rect_returns_none(self):
        self.assertIsNone(hole_interior(rect(0, 0, 20, 20), ring=16))

    def test_hole_threshold_is_0_97(self):
        self.assertEqual(HOLE_MIN_SSIM, 0.97)


class CaptionCropTest(unittest.TestCase):
    def test_bottom_12_percent(self):
        self.assertEqual(caption_crop_height(1080), round(1080 * 0.12))
        self.assertEqual(caption_crop_height(10), 1)

    def test_slide_as_page_reads_manifest_us(self):
        slide = {
            "startUs": 0,
            "endUs": 8_000_000,
            "focus": [focus(0, "a", rect(1, 1, 10, 10))],
            "lookCrop": True,
        }
        page = slide_as_page(slide)
        self.assertEqual(page["durationMs"], 8000)
        self.assertTrue(page["lookCrop"])


class ProbeCaptionOffTest(unittest.TestCase):
    def test_dom_shot_removes_on_stage_caption(self):
        path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "dom-shot.mjs")
        with open(path, encoding="utf-8") as f:
            src = f.read()
        self.assertIn(".ppts-caption", src)
        self.assertIn(".ppts-subtitle", src)


if __name__ == "__main__":
    unittest.main()
