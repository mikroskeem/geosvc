FROM golang:1.13.6-alpine3.10

# Install required dependencies
RUN    apk update \
    && apk add git binutils

# Build geosvc
WORKDIR /tmp
COPY . /tmp/geosvc
RUN chown -R nobody:nobody /tmp/geosvc

USER nobody
RUN    cd geosvc \
    && env GOCACHE=/tmp/.cache CGO_ENABLED=0 GOOS=linux go build \
    && strip --strip-unneeded geosvc

# Create geosvc image
FROM scratch
COPY --from=0 /tmp/geosvc/geosvc /geosvc

USER 20000:20000
VOLUME /data
EXPOSE 5000/tcp

ENV GEOSVC_LISTEN_ADDR=0.0.0.0:5000
ENV GEOSVC_DATA_DIR=/data

CMD ["/geosvc"]
