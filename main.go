package main

import (
	"os"

	"github.com/alecthomas/kong"

	"github.com/hangxie/chatops/cmd/version"
)

var cli struct {
	Version version.Cmd `cmd:"" help:"Show build version."`
}

func main() {
	parser := kong.Must(
		&cli,
		kong.Name("chatops"),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Description("ChatOps utility, for full usage see https://github.com/hangxie/chatops/blob/main/README.md"),
	)

	ctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)
	ctx.FatalIfErrorf(ctx.Run())
}
