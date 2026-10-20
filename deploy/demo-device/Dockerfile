# syntax=docker/dockerfile:1.7
# Docker "demo device": a Linux XFCE desktop on Xvfb running the real
# remote-agent with the X11/x264 native backend. Build context = repo root.

FROM golang:1.27-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential pkg-config libx11-dev libxext-dev libxfixes-dev libxtst-dev \
      libxdamage-dev libxrandr-dev libx264-dev libswscale-dev libavutil-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY proto/go.mod proto/
COPY agent/go.mod agent/go.sum agent/
RUN --mount=type=cache,target=/go/pkg/mod cd agent && go mod download
COPY proto proto
COPY native/include native/include
COPY native/linux native/linux
COPY agent agent
RUN make -C native/linux
ARG VERSION=0.1.0-demo
ARG RELEASE_PUBLIC_KEY=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd agent && CGO_ENABLED=1 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.releasePublicKey=${RELEASE_PUBLIC_KEY}" \
      -o /out/remote-agent ./cmd/remote-agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      xvfb xauth dbus-x11 x11-xserver-utils x11-utils xclip procps ca-certificates \
      xfce4-session xfwm4 xfce4-panel xfdesktop4 xfce4-settings xfconf xfce4-terminal mousepad thunar \
      adwaita-icon-theme fonts-dejavu-core fonts-noto-color-emoji \
      libx11-6 libxext6 libxfixes3 libxtst6 libxdamage1 libxrandr2 libx264-164 libswscale6 libavutil57 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -m -s /bin/bash -u 1000 device \
    && mkdir -p /data && chown device:device /data
COPY --from=build /out/remote-agent /usr/local/bin/remote-agent
COPY deploy/demo-device/entrypoint.sh /usr/local/bin/entrypoint.sh
COPY deploy/demo-device/welcome.txt /home/device/Desktop/Welcome.txt
RUN chmod 0755 /usr/local/bin/entrypoint.sh && chown -R device:device /home/device
USER device
ENV DISPLAY=:0 RC_DATA_DIR=/data RC_SCREEN=1920x1080 HOME=/home/device
VOLUME /data
EXPOSE 5561/udp 5562/tcp
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["run"]
