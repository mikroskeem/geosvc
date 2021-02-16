#!/bin/sh
set -euo pipefail

export DOCKER_BUILDKIT=1

docker build \
	-t mikroskeem/geosvc \
	-f docker/Dockerfile .
