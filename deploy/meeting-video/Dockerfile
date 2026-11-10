# 构建上下文为仓库根目录。独立构建会议视频下载工具。
ARG BASE_IMAGE=openlistteam/openlist:latest
FROM ${BASE_IMAGE}
USER root
# 依赖在构建阶段固定，不在运行时安装。下载流程仅使用 HTTP、yt-dlp 和 FFmpeg 封装。
RUN apk add --no-cache ffmpeg nodejs npm chromium python3 py3-pip \
    && python3 -m venv /opt/openlist/meeting \
    && /opt/openlist/meeting/bin/pip install --no-cache-dir yt-dlp==2026.10.26
COPY deploy/meeting-video/browser/package.json /opt/openlist/browser/package.json
RUN cd /opt/openlist/browser && npm install --omit=dev --ignore-scripts --no-audit --no-fund
COPY --chmod=755 --chown=1001:1001 bin/openlist-linux-amd64 /opt/openlist/openlist
USER openlist
