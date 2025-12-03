# syntax=docker/dockerfile:1
FROM docker.io/library/golang:1.25.5 AS build
RUN <<eot
  set -Eeux
  apt-get update
  apt-get upgrade -y
  apt-get install -y curl jq tar xz-utils git
  sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b /usr/local/bin/
eot
RUN useradd -m -u 1001 dev
USER dev
WORKDIR /home/dev/src
COPY --chown=dev:dev . .
RUN task build

FROM lscr.io/linuxserver/ffmpeg:version-8.0.1-cli
RUN <<eot
  set -Eeux
  useradd -m -u 1000 nonroot
  apt-get update -y
  apt-get upgrade -y
  apt-get install -y libjpeg-turbo-progs
  apt-get clean
  rm -rf /var/lib/apt/lists/*
eot
USER nonroot
COPY --chown=nonroot:nonroot --from=build /home/dev/src/bin/tidalgram /home/nonroot/tidalgram
WORKDIR /home/nonroot
ENV TZ=UTC
STOPSIGNAL SIGINT
ENTRYPOINT [ "/home/nonroot/tidalgram" ]
