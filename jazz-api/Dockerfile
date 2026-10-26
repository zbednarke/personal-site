FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/jazz-api .

FROM debian:trixie-slim AS runtime-base
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg fonts-dejavu-core \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 65532 --no-create-home app
USER 65532:65532

FROM runtime-base
COPY --from=build /out/jazz-api /jazz-api
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/jazz-api"]
