# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build

WORKDIR /src

# cache deps
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# static binary, stripped
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# pairing-store dir; ownership is inherited by a fresh volume mounted here
RUN mkdir -p /data && chown 65532:65532 /data

FROM gcr.io/distroless/static:nonroot

COPY --from=build /out/server /server
COPY --from=build --chown=65532:65532 /data /data

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/server"]
