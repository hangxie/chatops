package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

const (
	// maxIdentLine is RFC 4253's limit on an identification line, CRLF included.
	maxIdentLine  = 255
	maxIdentBytes = 8 << 10
)

// sshIdent matches an SSH 2 identification: protocol 2.0, or 1.99 for a server that also speaks 2.0.
var sshIdent = regexp.MustCompile(`^SSH-(?:2\.0|1\.99)-([\x21-\x2c\x2e-\x7e]+(?: .*)?)$`)

// dialFunc is (*net.Dialer).DialContext, replaced in tests.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// checkSSH connects and reads the server's identification without sending anything or authenticating.
func checkSSH(ctx context.Context, dial dialFunc, timeout time.Duration, ip netip.Addr, port int) Check {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", netip.AddrPortFrom(ip, uint16(port)).String())
	if err != nil {
		return netFailure(CheckSSH, err, timeout)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	line, err := readIdentification(conn)
	if errors.Is(ctx.Err(), context.Canceled) {
		err = ctx.Err()
	}
	var bad identError
	switch {
	case errors.As(err, &bad):
		return Check{Name: CheckSSH, Outcome: OutcomeFail, Detail: string(bad), Responded: true}
	case err != nil:
		check := netFailure(CheckSSH, err, timeout)
		if check.Outcome == OutcomeFail {
			check.Detail, check.Responded = "connected, but "+check.Detail, true
		}
		return check
	}
	match := sshIdent.FindStringSubmatch(line)
	if match == nil {
		return Check{Name: CheckSSH, Outcome: OutcomeFail, Detail: fmt.Sprintf("not an SSH 2 identification: %q", line), Responded: true}
	}
	return Check{Name: CheckSSH, Outcome: OutcomePass, Detail: match[1]}
}

// identError is a reply that is not a usable SSH identification, so the host answered but SSH failed.
type identError string

func (e identError) Error() string { return string(e) }

// readIdentification skips the lines a server may send first and returns the one starting with "SSH-".
func readIdentification(r io.Reader) (string, error) {
	reader := bufio.NewReaderSize(io.LimitReader(r, maxIdentBytes), maxIdentLine+1)
	read := 0
	for {
		raw, err := reader.ReadSlice('\n')
		read += len(raw)
		switch {
		case errors.Is(err, bufio.ErrBufferFull) || len(raw) > maxIdentLine:
			return "", identError(fmt.Sprintf("line longer than %d bytes", maxIdentLine))
		case errors.Is(err, io.EOF) && read >= maxIdentBytes:
			return "", identError(fmt.Sprintf("no SSH identification in the first %d bytes", maxIdentBytes))
		case errors.Is(err, io.EOF):
			return "", identError("connection closed before an SSH identification")
		case err != nil:
			return "", err
		}
		if line := strings.TrimRight(string(raw), "\r\n"); strings.HasPrefix(line, "SSH-") {
			return line, nil
		}
	}
}
