### Default image is base. You can add other support by modifying BASE_IMAGE_TAG. The following parameters are supported: base (default), aria2, ffmpeg, aio
ARG BASE_IMAGE_TAG=base

FROM alpine:edge AS builder
LABEL stage=go-builder
WORKDIR /app/
RUN apk add --no-cache bash curl jq gcc git go musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY ./ ./
# Railway/Docker build context often omits .git; synthesize minimal repo for build.sh metadata
RUN git init \
 && git config user.email "build@open-box.local" \
 && git config user.name "open-box-build" \
 && git add -A \
 && git commit -m "docker-build" --allow-empty \
 && bash build.sh release docker

FROM openlistteam/openlist-base-image:${BASE_IMAGE_TAG}
LABEL org.opencontainers.image.title="Open-Box" \
      org.opencontainers.image.description="Open-Box storage gateway powered by OpenList" \
      org.opencontainers.image.source="https://github.com/hillstreet-ph/open-box" \
      org.opencontainers.image.licenses="AGPL-3.0"
ARG INSTALL_FFMPEG=false
ARG INSTALL_ARIA2=false
ARG USER=openlist
ARG UID=1001
ARG GID=1001

WORKDIR /opt/openlist/

RUN addgroup -g ${GID} ${USER} && \
    adduser -D -u ${UID} -G ${USER} ${USER} && \
    mkdir -p /opt/openlist/data

COPY --from=builder --chmod=755 --chown=${UID}:${GID} /app/bin/openlist ./
COPY --chmod=755 --chown=${UID}:${GID} entrypoint.sh /entrypoint.sh

USER ${USER}
RUN /entrypoint.sh version

ENV UMASK=022 RUN_ARIA2=${INSTALL_ARIA2}
# Railway: volume mounted externally; do not declare Docker VOLUME
EXPOSE 5244 5245
ENTRYPOINT ["/entrypoint.sh"]
CMD ["server"]
