# Stage 1: Build stage
FROM golang:1.21 AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libsqlite3-dev \
    make \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy all source files including templates for embedding
COPY *.go ./
COPY templates ./templates
COPY Makefile ./

# Build the application statically with embedded templates
RUN make mailsink-static

# Stage 2: Runtime stage using Google distroless
FROM gcr.io/distroless/static

# Copy only the binary from builder (templates are embedded)
COPY --from=builder /app/mailsink /mailsink

# Create data directory
VOLUME ["/data"]

# Expose ports
EXPOSE 2525 8080

# Set the entrypoint with database path and bind to all interfaces
ENTRYPOINT ["/mailsink", "-smtp", "0.0.0.0:2525", "-http", "0.0.0.0:8080", "-db", "/data/mailsink.db"]
