# Stage 1: Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies including make
RUN apk add --no-cache gcc musl-dev sqlite-dev make

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy all source files including templates for embedding
COPY *.go ./
COPY templates ./templates
COPY Makefile ./

# Build the application with embedded templates
RUN make

# Stage 2: Runtime stage using Google distroless
#FROM gcr.io/distroless/static
FROM alpine

# Copy only the binary from builder (templates are embedded)
COPY --from=builder /app/mailsink /mailsink

# Create data directory
VOLUME ["/data"]

# Expose ports
EXPOSE 2525 8080

# Set the entrypoint with database path
ENTRYPOINT ["/mailsink", "-db", "/data/mailsink.db"]
#ENTRYPOINT ["ls", "-l", "/mailsink"]
#ENTRYPOINT ["/mailsink"]
