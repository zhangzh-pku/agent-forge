# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.24-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG APP=taskapi

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/agentforge ./cmd/${APP}

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=builder /out/agentforge /usr/local/bin/agentforge

ENV AGENTFORGE_RUNTIME=local \
    AGENTFORGE_AUTH_MODE=header \
    AGENTFORGE_LLM_PROVIDER=mock \
    PORT=8080

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/agentforge"]
