# Build context: repository root (the server module imports ../proto).
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY proto/ proto/
COPY server/go.mod server/go.sum server/
RUN cd server && go mod download
COPY server/ server/
RUN cd server \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/remotectl-api ./cmd/remotectl-api \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/remotectl-release ./cmd/remotectl-release \
 && mkdir -p /data/downloads

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
# Uploaded agent installers (a named volume inherits this ownership).
COPY --from=build --chown=nonroot:nonroot /data/ /data/
USER nonroot:nonroot
EXPOSE 8080 9090
ENTRYPOINT ["/usr/local/bin/remotectl-api"]
