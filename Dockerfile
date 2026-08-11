# syntax=docker/dockerfile:1
FROM golang:1.26.5-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/parser-job ./cmd/app

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/parser-job /parser-job
USER nonroot:nonroot
ENTRYPOINT ["/parser-job"]
