# Local-development specimen: build only the hub from the current source tree.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ENV CGO_ENABLED=0 GOOS=linux
RUN mkdir -p /out/data \
    && go build -trimpath -ldflags="-s -w" -o /out/cloud-pulse-hub ./cmd/hub

# Distroless supplies system CA roots and has no shell/package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/cloud-pulse-hub /cloud-pulse-hub
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV CP_LISTEN=0.0.0.0:8090 \
    CP_DATA_DIR=/data
EXPOSE 8090
ENTRYPOINT ["/cloud-pulse-hub"]
