FROM debian:13-slim
ARG DEBIAN_FRONTEND=noninteractive

COPY ./build/release/*-linux*.gz /tmp/

# chatops-mcp archives are extracted and removed first because the chatops-*
# glob also matches them.
RUN useradd --home-dir /app --create-home --system app \
 && apt-get update -qq \
 && apt-get -y -qq install ca-certificates \
 && case $(uname -m) in \
        aarch64) ARCH=arm64 ;; \
        x86_64) ARCH=amd64 ;; \
        armv7l) ARCH=arm ;; \
        *) echo Unsupported arch $(uname -m); exit 1 ;; \
    esac \
 && gzip -dc $(ls /tmp/chatops-mcp-*-linux-${ARCH}.gz) > /app/chatops-mcp \
 && rm -f /tmp/chatops-mcp-*-linux*.gz \
 && gzip -dc $(ls /tmp/chatops-*-linux-${ARCH}.gz) > /app/chatops \
 && chmod +x /app/chatops /app/chatops-mcp \
 && chown app /app/chatops /app/chatops-mcp \
 && rm -rf /var/lib/apt/lists/* /tmp/*-linux*.gz

USER app
WORKDIR /app
ENTRYPOINT ["/app/chatops"]
