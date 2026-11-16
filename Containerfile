ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27@sha256:fe517acaa580cc7915457080f850c37e618b3c1e750f98b58fc6429b605147a1 AS build
ARG TARGETARCH
ARG REVISION=development
ENV GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY . .
RUN GOARCH=$TARGETARCH go build -p 2 -trimpath -ldflags "-s -w -X main.revision=$REVISION" -o /out/seestar-wormhole-cli ./cmd/seestar-wormhole

FROM scratch
COPY --from=build /out/seestar-wormhole-cli /seestar-wormhole-cli
# Account login persists only the installation UUID. Mount /state for restarts.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Default local ports are unprivileged; no capabilities are needed.
# Mount /state with ownership 65532:65532 to persist the installation UUID.
USER 65532:65532
ENTRYPOINT ["/seestar-wormhole-cli", "proxy", "--bind", "0.0.0.0", "--state-dir", "/state"]
