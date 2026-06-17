# Copyright 2026 The Kube Bind Authors.
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

# v2 slim-core konnector image.
#
# Build context is the v2/ directory (the konnector module's `replace
# github.com/kbind/kbind/v2/sdk => ../sdk` needs the sibling sdk module):
#
#   docker build -f konnector/Dockerfile -t kbind/konnector:dev .
#
# from inside v2/.
# Pin the builder to the build host's native platform and cross-compile via
# GOARCH/GOOS (CGO disabled), so multi-arch builds need no QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.2 AS builder
WORKDIR /workspace

ARG TARGETARCH
ARG TARGETOS
ARG LDFLAGS

# Both modules are needed: konnector requires sdk via a local replace. Build
# standalone (GOWORK=off) so the image does not depend on the go.work file.
COPY sdk/ sdk/
COPY konnector/ konnector/

WORKDIR /workspace/konnector
ENV GOWORK=off
RUN go mod download
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="${LDFLAGS}" -o /bin/konnector ./cmd/konnector

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/konnector /bin/konnector
USER 65532:65532
ENTRYPOINT ["/bin/konnector"]
