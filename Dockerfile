# DeedFlow: one static Go binary (web server, background worker and management
# commands) with the templates and static files embedded, plus the two tools it
# shells out to: Tesseract (captcha OCR) and pdftoppm (page images for the AI step).
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/deedflow ./cmd/deedflow

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tesseract-ocr poppler-utils \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/deedflow /usr/local/bin/deedflow
WORKDIR /app
ENV HTTP_ADDR=:8000 MEDIA_ROOT=/app/media
EXPOSE 8000
ENTRYPOINT ["deedflow"]
CMD ["serve"]
