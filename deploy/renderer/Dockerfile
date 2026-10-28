# 渲染服务镜像（两段构建）
#
# 为什么拆 base/app：Chrome 是 CVE 的主来源、也是升级最频繁的部件。
# 放在 base 层后，升 Chrome 只重建 base，node_modules 与 Go 编译层命中缓存。
#
# 为什么不用 `apt install chromium`：发行版版本随基础镜像漂移，
# 同一份 IR 会在不同时间构建的镜像上出不同像素，而缓存键里没有它。
#
# 构建（在仓库根目录）：
#   docker build -f deploy/renderer/Dockerfile \
#     --build-arg CHROME_VERSION=$(cat deploy/renderer/CHROME_VERSION) \
#     --build-arg CHROME_SHA256=$(cat deploy/renderer/chrome.sha256) \
#     -t ppts/renderer:dev .
#
# 自检（发布前必须跑）：
#   docker run --rm --network none --read-only \
#     --tmpfs /work:mode=1777 --tmpfs /tmp:mode=1777 \
#     ppts/renderer:dev -selftest -backend remotion -workdir /work -renderer-script /app/render.mjs
#   两个 tmpfs 都不可省：mode=1777 给 UID 10001 写权限；/tmp 是 Remotion bundler 的打包目录。

# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.25
ARG NODE_VERSION=22

# ---------- stage 1: Go 服务 ----------
FROM golang:${GO_VERSION}-bookworm AS gobuild
# GOWORK=off 是硬要求：仓库里的 go.work 指向本地 textnorm 源码，不会进镜像。
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -ldflags "-s -w" -o /out/ppts-renderer ./cmd/renderer

# ---------- stage 2: 运行时 base（Node + Chrome + 字体）----------
FROM node:${NODE_VERSION}-bookworm-slim AS base
ARG CHROME_VERSION
ARG CHROME_SHA256

RUN apt-get -o Acquire::Retries=5 update \
 && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      ca-certificates curl unzip fonts-noto-cjk fonts-liberation \
      libglib2.0-0 libnss3 libnspr4 libdbus-1-3 \
      libatk1.0-0 libatk-bridge2.0-0 libatspi2.0-0 \
      libcups2 libdrm2 libgbm1 libasound2 \
      libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libxkbcommon0 \
      libpango-1.0-0 libpangocairo-1.0-0 libcairo2 \
      libx11-6 libx11-xcb1 libxcb1 libxext6 libxrender1 libxi6 libxtst6 libxss1 \
 && rm -rf /var/lib/apt/lists/*

# 固定版本 + 摘要校验：版本化 URL 是不可变的，但校验和能挡住下载截断与镜像源篡改。
RUN set -eux; \
    test -n "${CHROME_VERSION}"; \
    test -n "${CHROME_SHA256}"; \
    url="https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/linux64/chrome-headless-shell-linux64.zip"; \
    curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors "$url" -o /tmp/chrome.zip; \
    echo "${CHROME_SHA256}  /tmp/chrome.zip" | sha256sum -c -; \
    mkdir -p /opt/chrome; \
    unzip -q /tmp/chrome.zip -d /opt/chrome; \
    rm /tmp/chrome.zip; \
    /opt/chrome/chrome-headless-shell-linux64/chrome-headless-shell --version

ENV PPTS_CHROME=/opt/chrome/chrome-headless-shell-linux64/chrome-headless-shell \
    NODE_ENV=production

# ---------- stage 3: 应用 ----------
FROM base AS app
WORKDIR /app
# 只装运行时依赖：@remotion/bundler 与 typescript 属构建期，若进入镜像会白增 80MB 与 CVE 面。
COPY renderer/package.json renderer/package-lock.json ./
RUN npm ci --omit=dev \
      --fetch-retries=5 --fetch-retry-maxtimeout=120000 --fetch-timeout=300000 \
 && npm cache clean --force
COPY renderer/src ./src
COPY renderer/render.mjs renderer/still.mjs ./
COPY --from=gobuild /out/ppts-renderer /usr/local/bin/ppts-renderer

# 非 root：Chromium 因此不需要 --no-sandbox（sandbox 是主要防线之一，不该关掉）。
RUN useradd --uid 10001 --create-home --shell /usr/sbin/nologin renderer \
 && mkdir -p /work && chown renderer:renderer /work
USER 10001

EXPOSE 8090
# 默认命令是 Go 服务：HTTP 契约、幂等、并发都在它里面，Node 只是被它 exec 的合成器。
# --selftest 由部署流水线覆盖命令触发（见 compose.yml 的 selftest 服务）。
#
# -max-queued / -job-ttl 在这里显式写上而不是靠默认值：容器里 /work 是 2g tmpfs，
# 「排队位」与「产物保留窗口」直接决定它多久写满，属于部署决策，不该藏在代码默认值里。
ENTRYPOINT ["/usr/local/bin/ppts-renderer"]
CMD ["-addr", ":8090", "-backend", "remotion", "-workdir", "/work", \
     "-renderer-script", "/app/render.mjs", "-concurrency", "2", \
     "-max-queued", "8", "-job-ttl", "30m", \
     "-fonts", "Noto Sans CJK SC@20230817"]
