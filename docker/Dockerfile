FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.24 AS builder

ARG TARGETOS
ARG TARGETARCH

ENV GOCACHE=/root/.cache/go-build

WORKDIR $GOPATH/src/github.com/mikroskeem/geosvc
RUN --mount=target=. \
    --mount=type=cache,sharing=locked,target=/go/pkg/mod \
    --mount=type=cache,sharing=locked,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=${TARGETOS} \
    go mod download

RUN --mount=target=. \
    --mount=type=cache,sharing=locked,target=/go/pkg/mod \
    --mount=type=cache,sharing=locked,target=/root/.cache/go-build \
    --network=none \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} GOOS=${TARGETOS} \
    go build -mod=readonly -trimpath -ldflags="-w -s" -o /build/geosvc

# Create geosvc image
FROM scratch
COPY ./LICENSE /LICENSE
COPY --from=builder /etc/ssl /etc/ssl
COPY --from=builder /build/geosvc /usr/bin/geosvc

USER 0:0
VOLUME /data
EXPOSE 5000/tcp

ENV GEOSVC_LISTEN_ADDR=0.0.0.0:5000
ENV GEOSVC_DATA_DIR=/data
ENV PATH="/usr/bin"

CMD ["geosvc"]
