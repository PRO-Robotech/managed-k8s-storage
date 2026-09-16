FROM golang:1.26.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates e2fsprogs xfsprogs util-linux && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/storage-api"]
