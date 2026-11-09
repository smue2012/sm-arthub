# --- Stufe 1: Programm kompilieren --------------------------------------------
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# VERSION setzt GitHub Actions (Tag ohne «v» bzw. <branch>-<commit>).
ARG VERSION=lokal
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X main.gebaut=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.version=${VERSION}" \
      -o /sm-arthub ./cmd/sm-arthub

# --- Stufe 2: Laufzeit-Image --------------------------------------------------
# Ubuntu wie smerp: curl und ssh-keyscan braucht später der Importer, um die
# Preislisten der Lieferanten über sftp/ftps/https zu holen.
FROM ubuntu:24.04
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata curl openssh-client \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 65532 --no-create-home arthub
COPY --from=build /sm-arthub /sm-arthub
USER 65532
ENV TZ=Europe/Zurich
EXPOSE 8110
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/sm-arthub", "health"]
ENTRYPOINT ["/sm-arthub"]
