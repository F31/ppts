#!/usr/bin/env python3
"""Trim 《人工智能》 export snapshot to the 3-page canary set and enqueue.

Picks slide-257 / slide-258 / slide-278. Rewrites timeline + subtitles +
pagePngKeys + specs together. Writes a new timeline object under canary-3p.
Does not source /etc/ppts/config.env.

Run on 101.34.82.237 as root:
  python3 /tmp/ppts-3p/enqueue.py
  python3 /tmp/ppts-3p/enqueue.py html_bundle
"""
from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import uuid

SRC_JOB = "99353147-976e-42c7-8fa6-ed6be5dacdd6"
IDEM = os.environ.get("PPTS_CANARY_IDEM", "gaze-canary-20261116-3p")
PICK = ("slide-257", "slide-258", "slide-278")
TENANT = "10b1891a-a34d-4ed0-8275-da5531b7a79d"
PROJ = "33e4919a-7025-4f62-b54c-c7e7bc3ee762"
OBJECTS = "/var/lib/ppts/objects"
SQL_PATH = "/var/tmp/ppts-3p-enqueue.sql"


def psql(sql: str) -> str:
    return subprocess.check_output(
        ["sudo", "-u", "postgres", "psql", "-d", "ppts", "-v", "ON_ERROR_STOP=1", "-Atqc", sql],
        text=True,
    )


def shift_span(obj: dict, delta: int, *keys: str) -> None:
    for k in keys:
        if k in obj and obj[k] is not None:
            obj[k] = int(obj[k]) + delta


def trim_timeline(tl: dict) -> dict:
    by_id = {s["slideId"]: s for s in tl.get("slides") or []}
    missing = [sid for sid in PICK if sid not in by_id]
    if missing:
        raise SystemExit(f"timeline missing {missing}")
    subs_by = {}
    for cue in tl.get("subtitles") or []:
        subs_by.setdefault(cue.get("slideId"), []).append(cue)

    cursor = 0
    slides = []
    subs = []
    for sid in PICK:
        slide = json.loads(json.dumps(by_id[sid]))
        old_start = int(slide["startUs"])
        old_end = int(slide["endUs"])
        dur = old_end - old_start
        if dur <= 0:
            raise SystemExit(f"{sid} non-positive duration")
        delta = cursor - old_start
        shift_span(slide, delta, "startUs", "endUs")
        for seg in slide.get("segments") or []:
            shift_span(seg, delta, "startUs", "endUs")
        for cue in subs_by.get(sid, []):
            c = json.loads(json.dumps(cue))
            shift_span(c, delta, "startUs", "endUs")
            for ch in c.get("chars") or []:
                shift_span(ch, delta, "startUs", "endUs")
            subs.append(c)
        slides.append(slide)
        cursor += dur

    out = json.loads(json.dumps(tl))
    out["slides"] = slides
    out["subtitles"] = subs
    out["durationUs"] = cursor
    return out


def srt_ts(us: int) -> str:
    ms = us // 1000
    h, ms = divmod(ms, 3_600_000)
    m, ms = divmod(ms, 60_000)
    s, ms = divmod(ms, 1000)
    return f"{h:02d}:{m:02d}:{s:02d},{ms:03d}"


def vtt_ts(us: int) -> str:
    return srt_ts(us).replace(",", ".")


def render_srt(cues: list) -> str:
    parts = []
    for i, cue in enumerate(cues, 1):
        text = (cue.get("text") or "").replace("\r\n", "\n").replace("\r", "\n").strip()
        parts.append(f"{i}\r\n{srt_ts(int(cue['startUs']))} --> {srt_ts(int(cue['endUs']))}\r\n{text}\r\n\r\n")
    return "".join(parts)


def render_vtt(cues: list) -> str:
    parts = ["WEBVTT\n\n"]
    for cue in cues:
        text = (cue.get("text") or "").replace("\r\n", "\n").replace("\r", "\n").strip()
        text = (
            text.replace("&", "&amp;")
            .replace("<", "&lt;")
            .replace(">", "&gt;")
        )
        parts.append(f"{vtt_ts(int(cue['startUs']))} --> {vtt_ts(int(cue['endUs']))}\n{text}\n\n")
    return "".join(parts)


def write_object(rel: str, data: bytes) -> str:
    path = os.path.join(OBJECTS, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(data)
    os.replace(tmp, path)
    os.chmod(path, 0o644)
    return path


def main() -> int:
    fmt = "mp4"
    if len(sys.argv) > 1:
        fmt = sys.argv[1]
    idem = IDEM if fmt == "mp4" else f"{IDEM}-{fmt.replace('_', '')}"

    running = psql(
        "SELECT id::text || ' ' || kind || ' ' || state FROM jobs "
        "WHERE state IN ('queued','running') ORDER BY created_at;"
    ).strip()
    if running:
        print(f"BUSY {running}", file=sys.stderr)
        return 1

    src_state = psql(f"SELECT state FROM jobs WHERE id = '{SRC_JOB}'::uuid;").strip()
    if src_state != "succeeded":
        print(f"source job not ready: {src_state!r}", file=sys.stderr)
        return 1

    existing = psql(
        f"SELECT id::text FROM jobs WHERE idempotency_key = '{idem}' AND kind = 'export' LIMIT 1;"
    ).strip()
    if existing:
        print(f"EXISTING {existing}", file=sys.stderr)
        return 1

    snap = json.loads(psql(f"SELECT input_snapshot FROM jobs WHERE id = '{SRC_JOB}'::uuid;"))
    src_key = snap["timelineKey"]
    src_path = os.path.join(OBJECTS, src_key)
    bundle = json.load(open(src_path, encoding="utf-8"))
    tl = trim_timeline(bundle["timeline"])
    want_ms = 83840 + 21080 + 31840
    got_ms = tl["durationUs"] // 1000
    if got_ms != want_ms:
        print(f"duration_ms {got_ms} want {want_ms}", file=sys.stderr)
        return 1

    tl_bytes = json.dumps(tl, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    asset = hashlib.sha256(tl_bytes).hexdigest()
    rev = "canary-3p"
    prefix = f"{TENANT}/{PROJ}/{rev}"
    srt_key = f"{prefix}/subtitle/{asset}.srt"
    vtt_key = f"{prefix}/subtitle/{asset}.vtt"
    tl_key = f"{prefix}/timeline/{asset}.json"
    new_bundle = {
        "timeline": tl,
        "srtKey": srt_key,
        "vttKey": vtt_key,
    }
    bundle_bytes = json.dumps(new_bundle, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    write_object(tl_key, bundle_bytes)
    write_object(srt_key, render_srt(tl["subtitles"]).encode("utf-8"))
    write_object(vtt_key, render_vtt(tl["subtitles"]).encode("utf-8"))
    canary_dir = os.path.join(OBJECTS, TENANT, PROJ, rev)
    shutil.chown(canary_dir, user="ppts", group="ppts")
    for root, dirs, files in os.walk(canary_dir):
        shutil.chown(root, user="ppts", group="ppts")
        for name in dirs + files:
            shutil.chown(os.path.join(root, name), user="ppts", group="ppts")

    pngs = snap.get("pagePngKeys") or []
    idx = {"slide-257": 1, "slide-258": 2, "slide-278": 22}
    snap["format"] = fmt
    snap["timelineKey"] = tl_key
    snap["pagePngKeys"] = [pngs[idx[sid]] for sid in PICK]
    # Identity refs from live store (latest apply-all), not the 30-page snapshot.
    # Tracks stay in animation_revisions; assembler loads by revision number.
    in_list = ",".join("'" + sid.replace("'", "''") + "'" for sid in PICK)
    spec_rows = psql(
        "SELECT slide_id || E'\\t' || coalesce(language,'zh-CN') || E'\\t' || "
        "source_revision_no::text || E'\\t' || revision::text || E'\\t' || "
        "coalesce(preset_id,'') || E'\\t' || coalesce(tracks->>'motionMode','') "
        "FROM animation_revisions "
        f"WHERE project_id = '{PROJ}'::uuid AND slide_id IN ({in_list}) "
        "AND source_revision_no = (SELECT current_revision FROM projects "
        f"WHERE id = '{PROJ}'::uuid) "
        "ORDER BY slide_id;"
    ).strip().splitlines()
    live = {}
    for line in spec_rows:
        if not line.strip():
            continue
        sid, lang, src_rev, rev, preset, mode = line.split("\t")
        live[sid] = {
            "slideId": sid,
            "language": lang,
            "sourceRevisionNo": int(src_rev),
            "revision": int(rev),
            "presetId": preset,
            "motionMode": mode,
        }
    missing = [sid for sid in PICK if sid not in live]
    if missing:
        print(f"animation_revisions missing {missing}", file=sys.stderr)
        return 1
    snap["animation"] = {
        "specs": [
            {
                "slideId": sid,
                "language": live[sid]["language"],
                "sourceRevisionNo": live[sid]["sourceRevisionNo"],
                "revision": live[sid]["revision"],
            }
            for sid in PICK
        ]
    }
    for sid, key in zip(PICK, snap["pagePngKeys"]):
        if not key:
            print(f"empty png for {sid}", file=sys.stderr)
            return 1
        if not os.path.isfile(os.path.join(OBJECTS, key)):
            print(f"missing png {key}", file=sys.stderr)
            return 1

    body = json.dumps(snap, ensure_ascii=False, separators=(",", ":"))
    job_id = str(uuid.uuid4())
    tag = "EXPORTSNAP"
    sql = f"""
INSERT INTO jobs (
  id, tenant_id, project_id, kind, state, idempotency_key, input_snapshot,
  run_at, traceparent, affected_pages, created_at, updated_at
)
SELECT
  '{job_id}'::uuid, tenant_id, project_id, 'export', 'queued',
  '{idem}',
  ${tag}${body}${tag}$,
  now(), '', '[]'::jsonb, now(), now()
FROM jobs
WHERE id = '{SRC_JOB}'::uuid
RETURNING id::text;
"""
    with open(SQL_PATH, "w", encoding="utf-8", newline="\n") as f:
        f.write(sql)
    os.chmod(SQL_PATH, 0o644)
    got = subprocess.check_output(
        ["sudo", "-u", "postgres", "psql", "-d", "ppts", "-v", "ON_ERROR_STOP=1", "-Atqf", SQL_PATH],
        text=True,
    ).strip()
    if got != job_id:
        print(f"INSERT returned {got!r}", file=sys.stderr)
        return 1
    print(f"JOB {job_id}")
    print(f"IDEM {idem}")
    print(f"FORMAT {fmt}")
    print(f"TIMELINE {tl_key}")
    print(f"DURATION_MS {got_ms}")
    print(f"PAGES {','.join(PICK)}")
    print(f"SPECS {json.dumps(snap['animation']['specs'], ensure_ascii=False)}")
    print(
        "LIVE "
        + json.dumps(
            {sid: {"preset": live[sid]["presetId"], "mode": live[sid]["motionMode"], "rev": live[sid]["revision"]} for sid in PICK},
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
