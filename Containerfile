ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
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
