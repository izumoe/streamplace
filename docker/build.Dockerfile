# syntax=docker/dockerfile:1
FROM ubuntu:22.04 AS builder-no-darwin

ARG TARGETARCH
ENV TARGETARCH=$TARGETARCH

ARG DOCKERFILE_HASH
ENV DOCKERFILE_HASH=$DOCKERFILE_HASH

# You'll probably need to bump golangci-lint if you bump go
ENV GO_VERSION=1.26.10
ENV GOLANGCI_LINT_VERSION=2.12.2
ENV NODE_VERSION=22.15.0
ENV DEBIAN_FRONTEND=noninteractive

ADD docker/sources.list /etc/apt/sources.list
ADD docker/llvm-snapshot.key /etc/apt/keyrings/llvm-snapshot.key

RUN dpkg --add-architecture arm64

RUN apt update \
  && apt install -y build-essential curl git openjdk-17-jdk unzip jq g++ python3-pip ninja-build \
  gcc-aarch64-linux-gnu g++-aarch64-linux-gnu qemu-user-static pkg-config \
  nasm zip bison flex expect \
  libc6:arm64 libstdc++6:arm64 \
  cmake libssl-dev libssl-dev:arm64 libbz2-dev zlib1g-dev \
  ruby-rubygems postgresql sudo \
  && pip install meson==1.11.2 tomli \
  && curl -L --fail https://go.dev/dl/go$GO_VERSION.linux-$TARGETARCH.tar.gz -o go.tar.gz \
  && tar -C /usr/local -xf go.tar.gz \
  && rm go.tar.gz

RUN echo 'deb [arch=amd64 signed-by=/etc/apt/keyrings/llvm-snapshot.key] http://apt.llvm.org/jammy/ llvm-toolchain-jammy-21 main' >> /etc/apt/sources.list \
  && apt update \
  && apt install -y clang-21 lldb-21 lld-21 clangd-21

ENV PATH=/usr/lib/llvm-21/bin:$PATH:/usr/local/go/bin:/root/go/bin:/root/.cargo/bin

RUN export NODEARCH="$TARGETARCH" \
  && if [ "$TARGETARCH" = "amd64" ]; then export NODEARCH="x64"; fi \
  && curl -L --fail https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-$NODEARCH.tar.xz -o node.tar.gz \
  && tar -xf node.tar.gz \
  && cp -r node-v$NODE_VERSION-linux-$NODEARCH/* /usr/local \
  && rm -rf node.tar.gz node-v$NODE_VERSION-linux-$NODEARCH

RUN npm install -g corepack@latest

ARG ANDROID_SDK_VERSION=11076708
ENV ANDROID_HOME=/opt/android-sdk
RUN mkdir -p ${ANDROID_HOME}/cmdline-tools && \
  curl -L -O https://dl.google.com/android/repository/commandlinetools-linux-${ANDROID_SDK_VERSION}_latest.zip && \
  unzip *tools*linux*.zip -d ${ANDROID_HOME}/cmdline-tools && \
  mv ${ANDROID_HOME}/cmdline-tools/cmdline-tools ${ANDROID_HOME}/cmdline-tools/tools && \
  rm *tools*linux*.zip && \
  curl -L https://raw.githubusercontent.com/thyrlian/AndroidSDK/bfcbf0cdfd6bb1ef45579e6ddc4d3876264cbdd1/android-sdk/license_accepter.sh | bash

RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs > rustup.sh \
  && bash rustup.sh -y \
  && rustup target add aarch64-unknown-linux-gnu \
  && rustup target add x86_64-unknown-linux-gnu \
  && rustup target add x86_64-apple-darwin \
  && rustup target add aarch64-apple-darwin \
  && rustup target add wasm32-wasip1 \
  && rustup target add wasm32-unknown-unknown \
  && rm rustup.sh

RUN go env -w GOTOOLCHAIN=go$GO_VERSION

RUN curl -L https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64.tar.gz -o golangci-lint.tar.gz \
  && tar -xf golangci-lint.tar.gz \
  && mv golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64/golangci-lint /usr/local/bin/ \
  && rm -rf golangci-lint.tar.gz golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64

RUN gem install fpm

# Use the portable binary so compiler caching does not need its own Rust build.
RUN curl -fsSL https://github.com/mozilla/sccache/releases/download/v0.19.0/sccache-v0.19.0-x86_64-unknown-linux-musl.tar.gz -o /tmp/sccache.tar.gz \
  && echo 'aa73c7270c997894038c0d8f0d297865f57569e1687883322bc431713d4bb9dd  /tmp/sccache.tar.gz' | sha256sum -c - \
  && tar -xzf /tmp/sccache.tar.gz -C /tmp \
  && install /tmp/sccache-v0.19.0-x86_64-unknown-linux-musl/sccache /usr/local/bin/sccache \
  && rm -rf /tmp/sccache.tar.gz /tmp/sccache-v0.19.0-x86_64-unknown-linux-musl

ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=false
ENV STREAMPLACE_TEST_POSTGRES_COMMAND="install -d -o postgres -g postgres -m 3775 /var/run/postgresql/14-main.pg_stat_tmp && sudo -u postgres /usr/lib/postgresql/14/bin/postgres -D /etc/postgresql/14/main/"
ENV STREAMPLACE_TEST_POSTGRES_URL="postgresql://postgres:postgres@localhost:5432/streamplace"
# allow all postgres connections
RUN bash -c 'echo -en "local   all             postgres                                peer\nhost    all             all             0.0.0.0/0            trust\n" > /etc/postgresql/14/main/pg_hba.conf'

FROM builder-no-darwin AS builder

WORKDIR /osxcross

RUN git clone https://github.com/tpoechtrager/osxcross.git .
# RUN UNATTENDED=1 ./build_apple_clang.sh
ENV MAC_SDK_VERSION=15.4
RUN curl -L --fail https://github.com/joseluisq/macosx-sdks/releases/download/$MAC_SDK_VERSION/MacOSX$MAC_SDK_VERSION.sdk.tar.xz -o /osxcross/tarballs/MacOSX$MAC_SDK_VERSION.sdk.tar.xz
RUN UNATTENDED=1 ./build.sh
RUN cargo install apple-codesign
ENV PATH=/osxcross/target/bin:$PATH

LABEL org.opencontainers.image.authors="support@stream.place"
