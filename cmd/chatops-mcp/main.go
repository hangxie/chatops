package main

import (
	"context"
	"os"

	"github.com/alecthomas/kong"

	"github.com/hangxie/chatops/cmd/chatops-mcp/serve"
	"github.com/hangxie/chatops/internal/command"
	"github.com/hangxie/chatops/internal/version"
)

var cli struct {
	Serve   serve.Cmd   `cmd:"" help:"Serve MCP tools over stdio or Streamable HTTP."`
	Version version.Cmd `cmd:"" help:"Show build version."`
}

func main() {
	parser := newParser()
	ctx, stop := command.TerminationContext(context.Background())
	defer stop()
	parser.FatalIfErrorf(command.Run(ctx, parser, os.Args[1:]))
}

func newParser() *kong.Kong {
	return command.NewParser(&cli, "chatops-mcp", "ChatOps built-in MCP server, for full usage see https://github.com/hangxie/chatops/blob/main/README.md")
}
