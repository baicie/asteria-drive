FROM golang:1.25.13@sha256:14e75143c833c7398ea3a5e4c673aeaae35f40e781e6b060e2f97b72c475c975 AS build

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
