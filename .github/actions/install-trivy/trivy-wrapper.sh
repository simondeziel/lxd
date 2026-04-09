#!/bin/bash
exec /usr/bin/docker run --rm \
  --user "$(id -u):$(id -g)" \
  -v /home/runner/vuln-cache:/cache \
  -v "$GITHUB_WORKSPACE":"$GITHUB_WORKSPACE" \
  -w "$PWD" \
  "${TRIVY_IMAGE}" \
  --cache-dir /cache "$@"
