// Command acp2api runs an OpenAI-compatible HTTP gateway in front of ACP
// agents: clients speak the ordinary OpenAI API, and the gateway drives an ACP
// agent CLI over stdio on their behalf.
//
// The command line is the embedded Lota engine (cli.yml); every command is a
// native Go handler.
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"

	"github.com/quonaro/lota/engine"
)

//go:embed cli.yml
var cliYAML []byte

func main() {
	builder := engine.NewBuilder("acp2api", cliYAML)
	builder.RegisterNative("serve", runServer)
	builder.RegisterNative("version", showVersion)
	builder.WithOptions(engine.Options{AutoHelp: true})

	app, err := builder.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp2api:", err)
		os.Exit(1)
	}

	if err := app.Run(context.Background(), os.Args[1:]); err != nil {
		var groupErr *engine.GroupError
		if errors.As(err, &groupErr) {
			app.PrintGroupHelp(groupErr.Groups)
			return
		}
		fmt.Fprintln(os.Stderr, "acp2api:", err)
		os.Exit(1)
	}
}
