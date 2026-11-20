FROM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/baicie/asteria-drive/internal/buildinfo.Version=${VERSION} -X github.com/baicie/asteria-drive/internal/buildinfo.Commit=${COMMIT} -X github.com/baicie/asteria-drive/internal/buildinfo.Date=${BUILD_TIME}" \
    -o /out/asteria-server ./cmd/asteria-server
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/baicie/asteria-drive/internal/buildinfo.Version=${VERSION} -X github.com/baicie/asteria-drive/internal/buildinfo.Commit=${COMMIT} -X github.com/baicie/asteria-drive/internal/buildinfo.Date=${BUILD_TIME}" \
    -o /out/asteria-migrate ./cmd/asteria-migrate
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/baicie/asteria-drive/internal/buildinfo.Version=${VERSION} -X github.com/baicie/asteria-drive/internal/buildinfo.Commit=${COMMIT} -X github.com/baicie/asteria-drive/internal/buildinfo.Date=${BUILD_TIME}" \
    -o /out/asteria-verify-storage ./cmd/asteria-verify-storage

FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/asteria-server /usr/local/bin/asteria-server
COPY --from=build /out/asteria-migrate /usr/local/bin/asteria-migrate
COPY --from=build /out/asteria-verify-storage /usr/local/bin/asteria-verify-storage

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/asteria-server"]
