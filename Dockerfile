# Build the manager binary
# Pinned by digest, like every action and every tool in this repository.
# The base image is the one input that ends up inside the artifact being
# signed and inventoried, so it is the last one that should float.
FROM golang:1.26@sha256:2005724102f45917a63e9d092fc0e4ea56ea575048ce147caad5f5f61502c365 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the Go source (relies on .dockerignore to filter)
COPY . .

# Build
# the GOARCH has no default value to allow the binary to be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

# Static annotations only. created, revision and version are deliberately
# absent: baking them in would invalidate this layer on every build and go
# stale the moment the image outlives the commit that wrote it. The workflow
# supplies those three at push time, where they can be correct.
#
# licenses is absent too, and that is a statement rather than an oversight:
# this repository ships no LICENSE file, so there is no SPDX expression to
# assert. Claiming one here while the registry index reported an empty value
# left the image contradicting itself about its own terms.
#
# base.name and base.digest name the immediate parent, so a scanner can tell
# which layers are ours. They repeat the digest in the FROM line above, and a
# test in test/ci fails if the two ever disagree -- which is exactly what a
# dependency bot updating one and not the other would produce.
LABEL org.opencontainers.image.source=https://github.com/negativecycle/podpool-controller \
      org.opencontainers.image.title=podpool-controller \
      org.opencontainers.image.description="A Kubernetes controller that distributes replicas across scaling groups" \
      org.opencontainers.image.base.name=gcr.io/distroless/static:nonroot \
      org.opencontainers.image.base.digest=sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
