FROM golang:alpine AS builder

# Build geosvc
WORKDIR $GOPATH/src/github.com/mikroskeem/geosvc
COPY . .
RUN CGO_ENABLED=0 GOARCH=amd64 GOOS=linux go build -ldflags="-w -s" -o /geosvc

# Create geosvc image
FROM scratch
COPY ./LICENSE /LICENSE
COPY --from=builder /etc/ssl /etc/ssl
COPY --from=builder /geosvc /geosvc

USER 0:0
VOLUME /data
EXPOSE 5000/tcp

ENV GEOSVC_LISTEN_ADDR=0.0.0.0:5000
ENV GEOSVC_DATA_DIR=/data

CMD ["/geosvc"]
