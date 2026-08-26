# Copyright 2022 The Kube Bind Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Use node:lts-alpine for better compatibility and smaller size
FROM node:22-alpine AS ui-build-env
WORKDIR /app

# Install build dependencies needed for native modules
RUN apk add --no-cache python3 make g++

# Copy package files
COPY ./web/package*.json ./
COPY ./web/.npmrc ./

RUN npm ci

# Copy the Vue app files
COPY ./web .

# Set environment to avoid native dependency issues
ENV NODE_ENV=production
ENV VITE_BUILD_TARGET=docker

# Building UI with Docker-specific config
RUN npm run build

# Build Go binary with embedded UI assets
FROM golang:1.26.5 AS go-build-env
WORKDIR /app

# Accept build arguments for multi-arch support
ARG TARGETARCH
ARG TARGETOS
ARG LDFLAGS

RUN apt-get update && apt-get install -y make jq

# Copy go.mod and go.sum files first for better caching
COPY go.mod .
COPY go.sum .

# Copy the source code
COPY . .

# Copy built UI assets for embedding
COPY --from=ui-build-env /app/dist ./backend/static/web/dist

# Build with embedded assets
RUN if [ -n "$LDFLAGS" ]; then \
        echo "Building with LDFLAGS: $LDFLAGS for $TARGETOS/$TARGETARCH"; \
        CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="$LDFLAGS" -o bin/backend ./cmd/backend; \
    else \
        CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH make build; \
    fi

FROM alpine:3.22.1
RUN apk --update add ca-certificates

COPY --from=go-build-env /app/bin/backend /bin
COPY --from=ui-build-env /app/dist /www



ENTRYPOINT ["/bin/backend"]
