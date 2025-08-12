### Default image is base. You can add other support by modifying BASE_IMAGE_TAG. The following parameters are supported: base (default), aria2, ffmpeg, aio
ARG BASE_IMAGE_TAG=base

FROM alpine:edge AS builder
LABEL stage=go-builder
WORKDIR /app/
RUN apk add --no-cache bash curl jq gcc git go musl-dev
COPY go.mod go.sum ./
RUN go mod download
COPY ./ ./
RUN bash build.sh release docker

FROM openlistteam/openlist-base-image:${BASE_IMAGE_TAG}
LABEL MAINTAINER="OpenList"
ARG INSTALL_FFMPEG=false
ARG INSTALL_ARIA2=false
ARG USER=openlist
ARG UID=1001
ARG GID=1001

WORKDIR /opt/openlist/

COPY --chmod=755 --from=builder /app/bin/openlist ./
COPY --chmod=755 entrypoint.sh /entrypoint.sh
RUN adduser -u ${UID} -g ${GID} -h /opt/openlist/data -D -s /bin/sh ${USER} \
    && chown -R ${UID}:${GID} /opt \
    && chown -R ${UID}:${GID} /entrypoint.sh

USER ${USER}
RUN /entrypoint.sh version

ENV UMASK=022 RUN_ARIA2=${INSTALL_ARIA2}
VOLUME /opt/openlist/data/
EXPOSE 5244 5245
CMD [ "/entrypoint.sh" ]
