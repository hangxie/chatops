package main

import (
	"context"
	"os"

	"github.com/alecthomas/kong"

	"github.com/hangxie/chatops/internal/command"
	"github.com/hangxie/chatops/internal/version"
)

var cli struct {
	Version version.Cmd `cmd:"" help:"Show build version."`
}

func main() {
	parser := newParser()
	ctx, stop := command.TerminationContext(context.Background())
	defer stop()
	parser.FatalIfErrorf(command.Run(ctx, parser, os.Args[1:]))
}

func newParser() *kong.Kong {
	return command.NewParser(&cli, "chatops", "ChatOps daemon, for full usage see https://github.com/hangxie/chatops/blob/main/README.md")
}
