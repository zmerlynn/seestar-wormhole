ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27@sha256:e432b43af23a9328d56a7c499be0476810aa344acbcf65fc7c455d4ff5a40602 AS build
ARG TARGETARCH
ARG REVISION=development
ENV GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY . .
RUN GOARCH=$TARGETARCH go build -p 2 -trimpath -ldflags "-s -w -X main.revision=$REVISION" -o /out/seestar-wormhole ./cmd/seestar-wormhole

FROM scratch
COPY --from=build /out/seestar-wormhole /seestar-wormhole
# Account login persists only the installation UUID. Mount /state for restarts.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Default local ports are unprivileged; no capabilities are needed.
# Mount /state with ownership 65532:65532 to persist the installation UUID.
USER 65532:65532
ENTRYPOINT ["/seestar-wormhole", "proxy", "--bind", "0.0.0.0", "--state-dir", "/state"]
