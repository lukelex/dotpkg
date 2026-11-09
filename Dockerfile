# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.10

FROM golang:${GO_VERSION}-bookworm AS dependencies

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

FROM dependencies AS dev

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false
COPY . .
CMD ["sleep", "infinity"]

FROM dependencies AS tooling

RUN go install mvdan.cc/gofumpt@v0.12.0 && \
    go install github.com/mgechev/revive@v1.17.1 && \
    go install honnef.co/go/tools/cmd/staticcheck@v0.7.0 && \
    go install golang.org/x/vuln/cmd/govulncheck@v1.5.0

FROM tooling AS checks

ENV CGO_ENABLED=1 \
    GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false
COPY . .
RUN test -z "$(gofmt -l cmd internal)" && \
    test -z "$(gofumpt -l cmd internal)" && \
    revive -config revive.toml -set_exit_status ./... && \
    go test ./... && \
    go test -race ./... && \
    go vet ./... && \
    staticcheck ./... && \
    govulncheck ./... && \
    for script in integration/*; do \
        case "$script" in *.sh|dotfiles-package-stage) bash -n "$script" ;; esac; \
    done && \
    for test in integration/*.test.sh; do bash "$test"; done

FROM dependencies AS test

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local
COPY . .
RUN go test ./... && go vet ./...

FROM dependencies AS build

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local
COPY . .
RUN mkdir -p /out && \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/dotpkg ./cmd/dotpkg

FROM scratch AS runtime

COPY --from=build /out/dotpkg /dotpkg
ENTRYPOINT ["/dotpkg"]
