#!/bin/bash

set -euo pipefail

rm -f /tmp/release-build-pid
for BIN in ${BINARIES}; do
    for TARGET in ${REL_TARGET}; do
        (
            BINARY=${BUILD_DIR}/release/${BIN}-${VERSION}-${TARGET}
            rm -f ${BINARY} ${BINARY}.gz
            export GOOS=$(echo ${TARGET} | cut -f 1 -d \-)
            export GOARCH=$(echo ${TARGET} | cut -f 2 -d \-)
            ${GO} build ${GOFLAGS} \
                -ldflags "${LDFLAGS} -X ${PKG_PREFIX}/internal/version.source=github" \
                -o ${BINARY} ./cmd/${BIN}
            gzip ${BINARY}
            echo "    ${BIN} ${TARGET} built"
        ) &
        echo $! >> /tmp/release-build-pid
    done
done

for PID in $(cat /tmp/release-build-pid); do
    wait $PID
done
