#!/usr/bin/env bash
set -euo pipefail

release="release-20260817.0"
archive="gvisor-x86_64.tar.bz2"
sha256="ae345a8c1466586b3a163fb534301913da663a97b8ed446bc711b2e1963a32c5"
url="https://github.com/google/gvisor/releases/download/${release}/${archive}"

curl -fsSL --retry 3 --retry-delay 2 -o "/tmp/${archive}" "${url}"
printf '%s  %s\n' "${sha256}" "/tmp/${archive}" | sha256sum -c -

sudo tar -xjf "/tmp/${archive}" -C /usr/local/bin
sudo /usr/local/bin/runsc install --runtime runsc
sudo systemctl restart docker

/usr/local/bin/runsc --version
docker info --format '{{json .Runtimes}}'
docker run --rm --runtime=runsc alpine:3.22 true
