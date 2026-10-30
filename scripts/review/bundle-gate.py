#!/usr/bin/env python3
"""成品包质量门禁（M6-4）：V14 自包含性、V15 体积、V16 可播放性、V17 降级矩阵。

为什么这些必须是门禁而不是"打开看一眼"：

  * **V14 外链**：本地开着网看不出差别，断网的客户才会看到白屏。
  * **V15 体积**：翻倍是渐进的——某次把层图从 PNG 换成未压缩、或音频存了两份，
    单看代码完全正常。实测这一项当场抓到「混音轨 + 逐段音频双份」，占 80% 体积。
  * **V16 可播放性**：删掉一个清单引用的层图，页面照样"看起来正常"，只是那一层没了。
  * **V17 降级**：三条降级路径平时没人走，坏掉也不会有人报 bug。

判定与实现分工：
  - V14/V15 交给 `cmd/share-verify`（Go 侧唯一实现，打包时自检与事后校验同一套判据）；
  - V16/V17 交给 CDP 驱动的 `bundle-probe.mjs`（虚拟时间与 1x 媒体播放不可调和，
    具体原因见 design ADR-2）。

用法：
    python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg
    python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg --skip-browser   # 只跑 V14/V15

退出码：0 全过；1 有阻断失败；2 环境缺失（既没有本地 Chrome，也没有可用的渲染镜像）。
依赖：go；浏览器侧二选一——本机 Node ≥22 + Chrome（`--engine native`，自动探测），
或者带 chrome-headless-shell 的渲染镜像（`--engine docker`）。`--engine auto` 先试本机。
"""

import argparse
import json
import os
import shutil
import subprocess
import sys

DEFAULT_IMAGE = "ppts/renderer:dev"
REVIEW_DIR = os.path.dirname(os.path.abspath(__file__))
PROBE = os.path.join(REVIEW_DIR, "bundle-probe.mjs")


# ---- WSL 路径与命令封装（与 dom-ssim.py 同一套做法，Windows 上 docker 只在 WSL 里）----
def shell_quote(part):
    return "'" + part.replace("'", "'\\''") + "'"


def to_posix(path):
    if os.name != "nt":
        return path
    out = subprocess.run(["wsl", "-e", "wslpath", "-a", "-u", os.path.abspath(path)],
                         check=True, capture_output=True, text=True)
    return out.stdout.strip()


def wsl_wrap(cmd):
    if os.name != "nt":
        return cmd
    return ["wsl", "-e", "bash", "-lc", " ".join(shell_quote(p) for p in cmd)]


class Check:
    """一条判据。blocking=False 的只告警——用于依赖未实现能力的项。"""

    def __init__(self, code, name, ok, detail, blocking=True):
        self.code = code
        self.name = name
        self.ok = ok
        self.detail = detail
        self.blocking = blocking

    @property
    def status(self):
        if self.ok:
            return "PASS"
        return "FAIL" if self.blocking else "WARN"


class StructureError(Exception):
    """包结构不自洽（清单引用的条目缺失等）。这比"画面看起来还行"更早也更准地拦住问题。"""


def run_share_verify(pkg):
    """调用 Go 侧校验，拿结构、外链与体积构成。"""
    cmd = ["go", "run", "./cmd/share-verify", "-json", "-budget", pkg]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode == 1:
        raise StructureError(proc.stderr.strip() or proc.stdout.strip())
    if proc.returncode not in (0, 2, 3):
        raise RuntimeError(f"share-verify failed ({proc.returncode}): {proc.stderr.strip()[:400]}")
    try:
        return json.loads(proc.stdout), proc.returncode
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"share-verify did not emit JSON: {proc.stdout[:300]}") from exc


def native_chrome():
    """问探针自己解析到了哪个浏览器——候选表只写在 bundle-probe.mjs 一处。

    两处各写一份候选路径，就会出现「门禁说找不到 Chrome，探针其实找得到」这种
    谁也解释不清的分歧。
    """
    node = os.environ.get("PPT_NODE") or shutil.which("node")
    if not node:
        return None, "PATH 里没有 node（原生引擎需要 Node ≥22）"
    proc = subprocess.run([node, PROBE, "--print-chrome"], capture_output=True, text=True)
    if proc.returncode != 0:
        return None, (proc.stderr or proc.stdout).strip() or "探针没有解析出浏览器"
    return (proc.stdout or "").strip(), ""


def docker_ready():
    try:
        if os.name == "nt":
            subprocess.run(["wsl", "-e", "bash", "-lc", "command -v docker"],
                           check=True, capture_output=True, text=True)
        else:
            subprocess.run(["docker", "version", "--format", "{{.Server.Version}}"],
                           check=True, capture_output=True, text=True)
        return True
    except (OSError, subprocess.CalledProcessError):
        return False


def run_probe(pkg, mode, engine, image):
    """跑一次 CDP 探针，返回解析后的报告。"""
    if engine == "native":
        node = os.environ.get("PPT_NODE") or shutil.which("node")
        cmd = [node, PROBE, "--pkg", os.path.abspath(pkg), "--mode", mode]
        proc = subprocess.run(cmd, capture_output=True, text=True)
    else:
        cmd = [
            "docker", "run", "--rm", "--entrypoint", "sh",
            "-v", f"{to_posix(REVIEW_DIR)}:/cdp:ro",
            "-v", f"{to_posix(pkg)}:/pkg:ro",
            image, "-c",
            f"node /cdp/bundle-probe.mjs --pkg /pkg --mode {mode}",
        ]
        proc = subprocess.run(wsl_wrap(cmd), capture_output=True, text=True)
    for line in proc.stdout.splitlines():
        if line.startswith("PROBE_JSON "):
            return json.loads(line[len("PROBE_JSON "):])
    stderr = (proc.stderr or "")[:400]
    raise RuntimeError(f"probe {mode} produced no result (exit {proc.returncode}): {stderr}")


def has_audio(manifest):
    return bool(manifest.get("audioMix")) or bool(manifest.get("audio"))


def check_playable(report, manifest):
    """V16：挂载、资源齐全、时长一致、能走到 ended、无错误。"""
    checks = []
    if not report.get("ok"):
        return [Check("V16", "探针执行", False, report.get("error", "unknown"))]

    res = report.get("result") or {}
    checks.append(Check("V16", "播放器挂载", bool(res.get("mounted")), "stage 已创建" if res.get("mounted") else "未找到 .ppts-stage"))

    failed = (res.get("resources") or {}).get("failed") or []
    checked = (res.get("resources") or {}).get("checked", 0)
    checks.append(Check("V16", "清单资源全部可加载", not failed,
                        f"{checked} 个资源，失败 {failed}" if failed else f"{checked} 个资源全部加载"))

    audio = res.get("audio")
    if has_audio(manifest):
        if not audio:
            checks.append(Check("V16", "音频可用", False, "清单声明了音频但播放器没有创建 <audio>"))
        else:
            diff = abs(audio.get("durationSec", 0) - audio.get("manifestSec", 0))
            checks.append(Check("V16", "音频时长与清单一致", diff <= 0.25,
                                f"{audio.get('src')} duration={audio.get('durationSec')}s 清单={audio.get('manifestSec')}s"))
            checks.append(Check("V16", "播放到结尾触发 ended", res.get("ended") == "ended",
                                f"ended={res.get('ended')} t={res.get('finalTimeSec')}"))
    else:
        checks.append(Check("V16", "音频可用", True, "包内无音频（静音片），跳过音频判据"))

    checks.append(Check("V16", "无 console error", not report.get("consoleErrors"),
                        str(report.get("consoleErrors")) if report.get("consoleErrors") else "空"))
    checks.append(Check("V16", "无资源加载失败日志", not report.get("logErrors"),
                        str(report.get("logErrors")) if report.get("logErrors") else "空"))
    checks.append(Check("V16", "页面无 script error", not res.get("pageErrors"),
                        str(res.get("pageErrors")) if res.get("pageErrors") else "空"))
    return checks


IDENTITY = ("none", "matrix(1, 0, 0, 1, 0, 0)")


def layers_of(sample):
    return sample.get("layers") or []


def moved_count(sample):
    return sum(1 for layer in layers_of(sample) if layer.get("transform") not in IDENTITY)


def check_degrade(reports, slides):
    """V17：四条降级路径，判据是「可读」而不是「没抛错」。"""
    checks = []

    # 禁 JS：noscript 必须自带逐页静态图与讲稿。
    nojs = (reports.get("nojs") or {}).get("result") or {}
    images = nojs.get("imageCount", 0)
    loaded = nojs.get("loaded", 0)
    texts = nojs.get("textCount", 0)
    checks.append(Check("V17", "禁 JS 有 noscript 内容", bool(nojs.get("hasNoscript")), "存在 .ppts-noscript"))
    checks.append(Check("V17", "禁 JS 逐页静态图", images >= slides and loaded == images,
                        f"{images} 张图（期望 ≥{slides}），已加载 {loaded}"))
    checks.append(Check("V17", "禁 JS 有讲稿文本", texts >= slides, f"{texts} 段（期望 ≥{slides}）"))
    checks.append(Check("V17", "禁 JS 不建播放器", not nojs.get("stageCreated"), "stage 未创建（说明 JS 确实被禁）"))

    # 默认态必须真的动——否则 reduced-motion 的对照无从判断。
    default_samples = (reports.get("default") or {}).get("result", {}).get("samples") or []
    default_moved = sum(moved_count(s) for s in default_samples)
    checks.append(Check("V17", "默认态存在位移（对照基准）", default_moved > 0,
                        f"{len(default_samples)} 个取样点共 {default_moved} 层有位移"))

    # reduced-motion：去掉位移，保留 opacity，时长不变。
    reduced_samples = (reports.get("reduced") or {}).get("result", {}).get("samples") or []
    reduced_moved = sum(moved_count(s) for s in reduced_samples)
    checks.append(Check("V17", "reduced-motion 去掉位移", reduced_moved == 0,
                        f"仍有 {reduced_moved} 层带位移" if reduced_moved else "全部为 identity"))

    reduced_opacity = [layer.get("opacity", 1) for s in reduced_samples for layer in layers_of(s)]
    checks.append(Check("V17", "reduced-motion 保留 opacity",
                        any(op < 0.99 for op in reduced_opacity),
                        f"透明度取值 {sorted(set(round(o, 3) for o in reduced_opacity))[:6]}"))

    default_durations = sorted({layer.get("durationMs") for s in default_samples for layer in layers_of(s)})
    reduced_durations = sorted({layer.get("durationMs") for s in reduced_samples for layer in layers_of(s)})
    checks.append(Check("V17", "reduced-motion 时长不变",
                        bool(default_durations) and default_durations == reduced_durations,
                        f"默认 {default_durations} / 降级 {reduced_durations}"))

    # 无 WAAPI：层必须是终态可见，不能停在 opacity 0。
    nowaapi_samples = (reports.get("nowaapi") or {}).get("result", {}).get("samples") or []
    nowaapi_layers = [layer for s in nowaapi_samples for layer in layers_of(s)]
    visible = all(layer.get("opacity") == 1 and layer.get("transform") in IDENTITY for layer in nowaapi_layers)
    checks.append(Check("V17", "无 WAAPI 时层为终态可见",
                        bool(nowaapi_layers) and visible,
                        f"{len(nowaapi_layers)} 层，opacity/transform 全部为终态" if visible else "存在停在中间态的层"))

    # 低内存：不建层，但页面仍渲染。
    lowmem = (reports.get("lowmem") or {}).get("result") or {}
    lowmem_samples = lowmem.get("samples") or []
    total_layers = sum(len(layers_of(s)) for s in lowmem_samples)
    checks.append(Check("V17", "低内存不建动画层", total_layers == 0,
                        f"仍创建了 {total_layers} 层" if total_layers else "层数为 0"))
    checks.append(Check("V17", "低内存仍渲染页面", bool(lowmem.get("hasStage")), "stage 存在"))
    return checks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pkg", required=True, help="成品包目录（含 index.html）")
    ap.add_argument("--image", default=DEFAULT_IMAGE, help="含 chrome 与 ws 的渲染镜像")
    ap.add_argument("--engine", choices=["auto", "native", "docker"], default="auto",
                    help="浏览器从哪来：本机 Chrome（native）还是渲染镜像（docker）；auto 先试本机")
    ap.add_argument("--skip-browser", action="store_true", help="只跑 V14/V15（无浏览器时）")
    ap.add_argument("--json", action="store_true", help="输出 JSON 报告")
    args = ap.parse_args()

    if not os.path.isdir(args.pkg):
        print(f"bundle-gate: 不是目录：{args.pkg}", file=sys.stderr)
        return 1

    try:
        verify, verify_code = run_share_verify(args.pkg)
    except StructureError as exc:
        # 结构不自洽时后面的判据都没有意义：直接给一条可读的失败，
        # 而不是让人在 traceback 里找"哪张图没了"。
        print(f"FAIL：包结构不自洽\n  {exc}", file=sys.stderr)
        return 1
    manifest = {
        "audioMix": verify.get("audioMix"),
        "audio": [{}] * verify.get("audioSegments", 0),
    }
    measure = verify.get("measure", {})
    checks = []

    # ---- V14 自包含性 ----
    samples = verify.get("externalRefSamples") or {}
    refs = verify.get("externalRefs", 0)
    detail = "0 处外部引用" if refs == 0 else "; ".join(
        f"{name} → {ref}" for name, items in samples.items() for ref in items)
    checks.append(Check("V14", "外部引用数为 0", refs == 0, detail))

    # ---- V15 体积 ----
    total_mib = measure.get("totalBytes", 0) / (1 << 20)
    budget_mib = measure.get("budgetBytes", 0) / (1 << 20)
    over = bool(measure.get("overBudget"))
    # 超预算只告警：阈值依赖字体子集/WebP/AAC，这三项都还没做；
    # 此刻阻断等于用门禁替产品做决定（design ADR-6）。
    checks.append(Check("V15", "体积在预算内", not over,
                        f"{total_mib:.2f} MiB / 预算 {budget_mib:.2f} MiB", blocking=False))
    checks.append(Check("V15", "字体子集覆盖", True, "not_applicable（不内嵌字体，使用系统字体栈）", blocking=False))

    reports = {}
    env_error = None
    engine = args.engine
    chrome = ""
    if not args.skip_browser:
        if engine == "auto":
            found, why = native_chrome()
            if found:
                engine, chrome = "native", found
            elif docker_ready():
                engine = "docker"
            else:
                env_error = f"没有可用的浏览器：{why}；docker 也不可用"
        elif engine == "native":
            chrome, why = native_chrome()
            if not chrome:
                env_error = why
        if not env_error:
            try:
                reports["play"] = run_probe(args.pkg, "play", engine, args.image)
                for mode in ("nojs", "default", "reduced", "nowaapi", "lowmem"):
                    reports[mode] = run_probe(args.pkg, mode, engine, args.image)
            except (RuntimeError, FileNotFoundError) as exc:
                env_error = str(exc)

    if env_error:
        checks.append(Check("V16", "浏览器探针可执行", False, env_error))
        checks.append(Check("V17", "降级矩阵可执行", False, env_error))
    elif not args.skip_browser:
        checks.extend(check_playable(reports["play"], manifest))
        checks.extend(check_degrade(reports, verify.get("slides", 0)))

    if args.json:
        print(json.dumps({
            "package": args.pkg,
            "engine": engine if not args.skip_browser else "skipped",
            "chrome": chrome,
            "checks": [{"code": c.code, "name": c.name, "status": c.status, "detail": c.detail} for c in checks],
            "measure": measure,
        }, ensure_ascii=False, indent=2))
    else:
        print(f"成品包：{args.pkg}")
        print(f"清单：{verify.get('slides')} 页，{verify.get('durationUs', 0) / 1e6:.1f} 秒，"
              f"体积 {total_mib:.2f} MiB / 预算 {budget_mib:.2f} MiB")
        if not args.skip_browser:
            print(f"浏览器：{engine} · {chrome or args.image}")
        print()
        print("| 项 | 判据 | 结果 | 实测 |")
        print("| --- | --- | --- | --- |")
        for c in checks:
            print(f"| {c.code} | {c.name} | {c.status} | {c.detail} |")
        blocking_fail = [c for c in checks if not c.ok and c.blocking]
        warned = [c for c in checks if not c.ok and not c.blocking]
        print()
        if blocking_fail:
            print(f"FAIL：{len(blocking_fail)} 项不达标 -> " + ", ".join(c.name for c in blocking_fail))
        else:
            print("PASS：全部阻断项通过" + (f"（{len(warned)} 项告警）" if warned else ""))

    if env_error:
        return 2
    return 1 if any(not c.ok and c.blocking for c in checks) else 0


if __name__ == "__main__":
    sys.exit(main())
