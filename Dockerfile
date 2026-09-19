# syntax=docker/dockerfile:1

ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-bookworm AS dependencies

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

FROM dependencies AS dev

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local
COPY . .
CMD ["sleep", "infinity"]

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
