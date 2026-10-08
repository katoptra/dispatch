# The toolbox: each check operates in this image, on a laptop and in CI. It uses Go 1.26
# because the host builds with the buildGoModule of nixos-26.05, which is Go 1.26. Do not
# use a newer Go toolchain here. A newer toolchain can accept code that the build on the
# host rejects.
FROM golang:1.26-alpine@sha256:8ac98ca534ac3f51e1f420a1dd2c15e74c75cfa0f23f3ad27eb5d7236c349a0c
# The mount does not always contain .git, and no code reads the VCS stamp.
ENV GOFLAGS=-buildvcs=false
