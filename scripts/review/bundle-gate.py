#!/usr/bin/env python3
"""成品包质量门禁（M6-4）：V14 自包含性、V15 体积与字体子集覆盖、V16 可播放性、V17 降级矩阵。

为什么这些必须是门禁而不是"打开看一眼"：

  * **V14 外链**：本地开着网看不出差别，断网的客户才会看到白屏。
  * **V15 体积**：翻倍是渐进的——某次把层图从 PNG 换成未压缩、或音频存了两份，
    单看代码完全正常。实测这一项当场抓到「混音轨 + 逐段音频双份」，占 80% 体积。
  * **V16 可播放性**：删掉一个清单引用的层图，页面照样"看起来正常"，只是那一层没了。
  * **V17 降级**：四条降级路径（reduced-motion / 无 WAAPI / 低内存 / 实测低帧率）平时没人走，
    坏掉也不会有人报 bug。
  * **V15 字体覆盖**：缺字在浏览器里是方框，不报错、不拦页面——字幕上少几个字，
    只有恰好认识那个字的人才会发现。

判定与实现分工：
  - V14/V15 交给 `cmd/share-verify`（Go 侧唯一实现，打包时自检与事后校验同一套判据）；
  - V16/V17 交给 CDP 驱动的 `bundle-probe.mjs`（虚拟时间与 1x 媒体播放不可调和，
    具体原因见 design ADR-2）。

用法：
    python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg
    python3 scripts/review/bundle-gate.py --pkg tmp/m64/pkg --skip-browser   # 只跑 V14/V15
    python3 scripts/review/bundle-gate.py --single tmp/m64/index.html        # 单文件形态（ADR-033）

单文件形态（`--single`）判据的差别：V14 与 V16/V17 逐条同判；V15 的体积预算**不套用**
（单文件 = 解压后 × 1.33 + noscript 页图，ADR-033 有实测），只报体积。清单字段
（页数/时长/音频段数）由探针从还原后的 `window.__PPT_MANIFEST__` 取。

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
import tempfile

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


def run_share_verify_single(path):
    """单文件形态：只拿外链与体积（没有 manifest.json 可读，见 share-verify -single）。"""
    cmd = ["go", "run", "./cmd/share-verify", "-single", "-json", path]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode not in (0, 2):
        raise StructureError(proc.stderr.strip() or proc.stdout.strip())
    try:
        return json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"share-verify -single did not emit JSON: {proc.stdout[:300]}") from exc


def stage_single(path):
    """把单文件摊成一个只有 index.html 的目录。

    为什么非要摊：探针按目录约定找 index.html（`--pkg DIR`），而单文件的"包"
    就是一个文件。摊成临时目录能让 V16/V17 复用同一套探针与判据，不必为单文件
    写第二条探测路径——两条路径迟早会分叉。
    """
    tmp = tempfile.mkdtemp(prefix="ppts-single-gate-")
    shutil.copyfile(path, os.path.join(tmp, "index.html"))
    return tmp


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
            # 两条路都算「播到了结尾」：媒体元素触发 ended，或播放器自己的 tick
            # 先到 us >= durationUs 并 finish()——后者会把音频暂停，于是 ended
            # 永远不来。探针那边已经按 OR 算了（bundle-probe.mjs 的注释），这里
            # 只认 ended 就会让同一份包随机红一半；而间歇性红最终会被人关掉。
            state = res.get("ended")
            checks.append(Check("V16", "播放到结尾触发 ended", state in ("ended", "reached-end"),
                                f"ended={state} t={res.get('finalTimeSec')}"))
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

    # 判据落在关键帧上而不是取样时刻的 computed style：淡入通常在页首几百毫秒内
    # 就结束，取样点稍偏后 opacity 就贴近 1（实测 0.99–0.993），阈值判会变成
    # "机器快一点就失败"的假阴性——而它要回答的其实是"降级后还留不留淡入"。
    reduced_layers = [layer for s in reduced_samples for layer in layers_of(s)]
    reduced_opacity_anim = any(layer.get("opacityAnimation") for layer in reduced_layers)
    sampled_opacity = sorted({round(layer.get("opacity", 1), 3) for layer in reduced_layers})
    checks.append(Check("V17", "reduced-motion 保留 opacity", reduced_opacity_anim,
                        f"{sum(1 for l in reduced_layers if l.get('opacityAnimation'))}/{len(reduced_layers)} 层关键帧含 opacity<1"
                        f"（取样值 {sampled_opacity[:6]}）"))

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

    # 实测低帧率：跑起来才知道，因此判据是「曾经有层 → 现在没有」。
    # 只要求最终层数为 0 是不够的——那一页本来就没层的话，这条会永远绿。
    lowfps = (reports.get("lowfps") or {}).get("result") or {}
    seen = bool(lowfps.get("seenLayers"))
    degraded_ms = int(lowfps.get("degradedAfterMs") or -1)
    checks.append(Check("V17", "实测低帧率触发降级",
                        seen and int(lowfps.get("layers") or 0) == 0 and degraded_ms > 0,
                        f"{degraded_ms}ms 后层数归零" if seen and degraded_ms > 0 else
                        ("从未建过层，无法判定" if not seen else f"仍有 {lowfps.get('layers')} 层")))
    checks.append(Check("V17", "降级后仍有画面与字幕",
                        bool(lowfps.get("hasStage")) and bool(str(lowfps.get("subtitleText") or "").strip()),
                        "stage 存在且字幕有文字" if bool(lowfps.get("hasStage")) else "stage 消失"))
    return checks


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pkg", help="成品包目录（含 index.html）")
    ap.add_argument("--single", metavar="HTML",
                    help="内联单文件（ADR-033）：V14/V16/V17 同判，V15 只报体积")
    ap.add_argument("--image", default=DEFAULT_IMAGE, help="含 chrome 与 ws 的渲染镜像")
    ap.add_argument("--engine", choices=["auto", "native", "docker"], default="auto",
                    help="浏览器从哪来：本机 Chrome（native）还是渲染镜像（docker）；auto 先试本机")
    ap.add_argument("--skip-browser", action="store_true", help="只跑 V14/V15（无浏览器时）")
    ap.add_argument("--json", action="store_true", help="输出 JSON 报告")
    args = ap.parse_args()

    if args.single and args.pkg:
        print("bundle-gate: --pkg 与 --single 只能二选一", file=sys.stderr)
        return 1
    if not args.single and not args.pkg:
        print("bundle-gate: 需要 --pkg（目录）或 --single（单文件）", file=sys.stderr)
        return 1

    # pkg_dir 是**探针**要吃的目录；单文件先摊成"只有 index.html 的临时目录"。
    pkg_dir = args.pkg
    if args.single:
        if not os.path.isfile(args.single):
            print(f"bundle-gate: --single 要的是一个 .html 文件：{args.single}", file=sys.stderr)
            return 1
        try:
            verify = run_share_verify_single(args.single)
        except StructureError as exc:
            print(f"FAIL：单文件检查未通过\n  {exc}", file=sys.stderr)
            return 1
        pkg_dir = stage_single(args.single)
    else:
        if not os.path.isdir(args.pkg):
            print(f"bundle-gate: 不是目录：{args.pkg}", file=sys.stderr)
            return 1
        try:
            verify, _ = run_share_verify(args.pkg)
        except StructureError as exc:
            # 结构不自洽时后面的判据都没有意义：直接给一条可读的失败，
            # 而不是让人在 traceback 里找"哪张图没了"。
            print(f"FAIL：包结构不自洽\n  {exc}", file=sys.stderr)
            return 1

    # 单文件没有可读的清单：manifest 由探针从还原后的 window.__PPT_MANIFEST__ 取，
    # 所以这里先留空，等 play 探针跑完再补（见下面 reports["play"] 之后）。
    manifest = None
    if not args.single:
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
    total_mib = verify.get("totalBytes", 0) / (1 << 20)
    if args.single:
        # 单文件不套用 zip 预算：它的体积天然是解压后 × 1.33（base64）+ noscript 页图
        # （ADR-033 实测 1.45×）。拿 zip 预算卡它只会天天红，然后被人关掉。
        checks.append(Check("V15", "体积（单文件形态）", True,
                            f"{total_mib:.2f} MiB（不套用 zip 预算，见 ADR-033）", blocking=False))
    else:
        budget_mib = measure.get("budgetBytes", 0) / (1 << 20)
        over = bool(measure.get("overBudget"))
        # 超预算仍只告警：阈值里还有两项没兑现（WebP、AAC），此时阻断等于用门禁
        # 替产品做决定（design ADR-6）。注意字体子集这轮已经做了，所以「字体让体积
        # 变轻」这条理由已经消失——真要收紧阈值，先看实测基线。参见 measure.go 的注释。
        checks.append(Check("V15", "体积在预算内", not over,
                            f"{total_mib:.2f} MiB / 预算 {budget_mib:.2f} MiB", blocking=False))
    # ---- V15 字体子集覆盖 ----
    #
    # 判据来自 share-verify（Go 侧唯一实现）：它从**包内实际文件**重建字集
    # （标题取清单、字幕解析 VTT、播放器文案读 app.js），再解析字体 cmap 逐字比对，
    # 不采信清单里自己写的 fontMissing——被测系统的账本不能同时当门禁的账本。
    #
    # 没内嵌字体时是 not_applicable 而不是 pass：这份包没做这件事，不该打勾。
    # 单文件形态同样跳过：字体已经 base64 内联进 <style>，Go 侧没有独立条目可解析。
    font = verify.get("font") or {}
    if args.single:
        checks.append(Check("V15", "字体子集覆盖", True,
                            "not_applicable（单文件里字体已内联，覆盖率以打包时校验为准）",
                            blocking=False))
    elif not font.get("Embedded"):
        checks.append(Check("V15", "字体子集覆盖", True,
                            "not_applicable（未内嵌字体，使用系统字体栈）", blocking=False))
    else:
        missing = font.get("Missing") or []
        detail = (f"{font.get('Family')} {font.get('FontBytes', 0) / 1024:.1f} KB，"
                  f"覆盖 {font.get('CharsetSize', 0)} 个字符")
        if missing:
            detail += f"，缺 {len(missing)} 个：{''.join(chr(c) for c in missing[:12])}"
        checks.append(Check("V15", "字体子集覆盖片中全部字符", not missing, detail))

    reports = {}
    env_error = None
    engine = args.engine
    chrome = ""
    # 单文件的这几项只能从页面上取（还原后的清单），先把 zip 形态的值放好。
    slides = 0 if args.single else verify.get("slides", 0)
    duration_us = 0 if args.single else verify.get("durationUs", 0)
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
                reports["play"] = run_probe(pkg_dir, "play", engine, args.image)
                for mode in ("nojs", "default", "reduced", "nowaapi", "lowmem", "lowfps"):
                    reports[mode] = run_probe(pkg_dir, mode, engine, args.image)
            except (RuntimeError, FileNotFoundError) as exc:
                env_error = str(exc)

    if env_error:
        checks.append(Check("V16", "浏览器探针可执行", False, env_error))
        checks.append(Check("V17", "降级矩阵可执行", False, env_error))
    elif not args.skip_browser:
        summary = (reports["play"].get("result") or {}).get("manifest") or {}
        if args.single:
            # 单文件没有 manifest.json：这几项来自页面里还原后的 window.__PPT_MANIFEST__。
            # 取不到（summary 为空）就按"没有音频、没有页"处理——check_playable 会随
            # 探针结果给红，而不是在这里猜一个让它变绿的值。
            manifest = {
                "audioMix": "inline" if summary.get("audioMix") else None,
                "audio": [{}] * int(summary.get("audioSegments") or 0),
            }
            slides = int(summary.get("slides") or 0)
            duration_us = int(summary.get("durationUs") or 0)
        checks.extend(check_playable(reports["play"], manifest))
        checks.extend(check_degrade(reports, slides))

    if args.json:
        print(json.dumps({
            "package": args.single or args.pkg,
            "kind": "single" if args.single else "dir",
            "engine": engine if not args.skip_browser else "skipped",
            "chrome": chrome,
            "checks": [{"code": c.code, "name": c.name, "status": c.status, "detail": c.detail} for c in checks],
            "measure": measure,
        }, ensure_ascii=False, indent=2))
    else:
        if args.single:
            print(f"成品包：{args.single}（单文件）")
            if args.skip_browser:
                print(f"体积 {total_mib:.2f} MiB（未启动浏览器，页数/时长未取）")
            else:
                print(f"清单：{slides} 页，{duration_us / 1e6:.1f} 秒（取自页面内还原后的清单），"
                      f"体积 {total_mib:.2f} MiB")
        else:
            print(f"成品包：{args.pkg}")
            print(f"清单：{verify.get('slides')} 页，{verify.get('durationUs', 0) / 1e6:.1f} 秒，"
                  f"体积 {total_mib:.2f} MiB / 预算 {measure.get('budgetBytes', 0) / (1 << 20):.2f} MiB")
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

    # 单文件摊出来的临时目录用完即删：里面只有一份 index.html 的拷贝，
    # 留着只会在跑多次后堆出几个几十 MB 的目录。
    if args.single:
        shutil.rmtree(pkg_dir, ignore_errors=True)

    if env_error:
        return 2
    return 1 if any(not c.ok and c.blocking for c in checks) else 0

if __name__ == "__main__":
    sys.exit(main())
