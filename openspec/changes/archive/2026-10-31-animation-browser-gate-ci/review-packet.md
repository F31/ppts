# 评审记录：成品包浏览器门禁进 CI

记录原则同前：**读数在前，结论在后**。每一条都写明命令、输入与实测输出，便于别人复跑。

## 1. 改动前的状态（为什么这不是"补个 CI 步骤"）

`bundle-probe.mjs` 里有四行把门禁钉在渲染镜像上：

| 位置 | 原内容 | 后果 |
|---|---|---|
| `createRequire('/app/package.json')` | 从镜像里取 `ws` | 镜像外跑不了（`ERR_MODULE_NOT_FOUND`） |
| `CHROME = '/opt/chrome/chrome-headless-shell-linux64/...'` | 只有镜像里有这个路径 | 本机有 Chrome 也用不上 |
| `file://${path.posix.join(pkgDir,'index.html')}` | Windows 上拼出 `file://C:\...` | Chrome 直接拒收 |
| `bundle-gate.py` 只有 `docker run` | 没镜像就没有第二条路 | 「能不能验」取决于「镜像能不能构建」 |

而镜像构建这条路当时是断的（容器内 apt 拉不动），也就是说：**V16/V17 这两条判据在合并前实际上从未被执行过**。

## 2. 正例：本机（Windows + Chrome）跑通

```
python scripts/review/bundle-gate.py --pkg tmp/gate/pkg
成品包：tmp/gate/pkg
清单：3 页，12.0 秒，体积 0.71 MiB / 预算 3.11 MiB
浏览器：native · C:\Program Files\Google\Chrome\Application\chrome.exe
...
PASS：全部阻断项通过          # 21 条判据，EXIT=0
```

样例包由 `share-baseline -pages 3 -page-secs 4 -layers 2` 生成：3 页、每页 2 层、每层 fadeIn + slideUp、页间 `fade` 360ms。

## 3. 负例（先证明会红）

| 注入的故障 | 期望 | 实测 | 退出码 |
|---|---|---|---|
| 把 `slides/002-layer-00.png` 写成 `not a png at all` | 结构检查仍过，只有 V16 资源项红 | `V16 清单资源全部可加载 FAIL：10 个资源，失败 ['slides/002-layer-00.png']`，其余 20 项 PASS | 1 |
| 用不带 `-layers` 的包 | V17 应有判据红 | 红四项：`默认态存在位移`（0 层有位移）、`reduced-motion 保留 opacity`（取值 `[]`）、`时长不变`（`默认 [] / 降级 []`）、`无 WAAPI 时层为终态可见`（0 层） | 1 |
| `PPT_CHROME=/definitely/not/here --engine native` | 环境缺失，不得判过 | `V16 浏览器探针可执行 FAIL：PPT_CHROME 指向的文件不存在…`，两条判据都标 FAIL | 2 |

第三条尤其重要：门禁对「没跑」和「跑了通过」必须给两种不同的结果。`PASS` 只在 21 条判据都执行过之后才可能打印。

## 4. 两处自己撞上的坑

**① 样例包的动画时长 520ms 太短。** 门禁在页首 +400ms 取样，探针实际落在 500–700ms 之间（seek 之后还要等一帧）。520ms 的动画在那里已经收尾，实测位移只剩 `0.000489px`——判据以「有位移」通过，但靠的是千分之一像素，等于没测。改成 900ms 后位移约 10px。

**② 我先把「无图层会让判据静默通过」写进了注释，实测是反的。** 无图层的包会让四项判据**红**，不是静默通过。已改注释与提案措辞。这条记在这里是因为它属于同一类错误：**先有推论，后去验证**。

## 5. 顺手修正的两处文档不实

- `html-share-quality` 规格里的 `bundle-gate.py --scan` 这个参数**不存在**（实现是 `--skip-browser`），已在 spec delta 中改正。
- 路线图 §4 的 V15 一行写着「CI 的 CLI 往返没带 `-budget`」——而**同一个提交** `d71b112` 刚给 CI 加上了 `-budget`。两句话在同一次提交里互相矛盾，本轮改正为「已过闸」。

## 6. 归档前读数

| 项 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` | 通过 |
| `go test ./...` | 42 包无失败（含新增 `TestSyntheticAnimation`） |
| `gofmt -l cmd internal scripts` | 空 |
| `openspec validate --all` | 12 passed（唯一告警仍属已归档 `render-plan`） |
| `verify.py --self-test` | 39/39 |
| `bundle-gate.py`（本机） | 21/21，三条负例按预期红 |

## 7. 仍未验证的一件事

**`share-browser` 作业本身在本机无法执行**（没有 GitHub runner）。本地验的是同一条命令序列（`share-baseline -layers 2` → `bundle-gate.py --engine native`）与探针解析逻辑；CI 侧新引入的变量只有「runner 上的 Chrome 在哪」这一项，作业里对此有两个分支（预装路径 / 兜底拉 `chrome-headless-shell@stable`）。首跑结果以 CI 为准，红了按上面第 3 节的判据定位。
