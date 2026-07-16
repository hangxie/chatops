#!/bin/bash

set -euo pipefail

rm -f /tmp/release-build-pid
for TARGET in ${REL_TARGET}; do
    (
        BINARY=${BUILD_DIR}/release/chatops-${VERSION}-${TARGET}
        rm -f ${BINARY} ${BINARY}.gz
        export GOOS=$(echo ${TARGET} | cut -f 1 -d \-)
        export GOARCH=$(echo ${TARGET} | cut -f 2 -d \-)
        ${GO} build ${GOFLAGS} \
            -ldflags "${LDFLAGS} -X ${PKG_PREFIX}/cmd/version.source=github" \
            -o ${BINARY} ./
        gzip ${BINARY}
        echo "    ${TARGET} built"
    ) &
    echo $! >> /tmp/release-build-pid
done

for PID in $(cat /tmp/release-build-pid); do
    wait $PID
done
