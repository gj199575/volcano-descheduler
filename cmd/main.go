package main

import (
	"os"

	"github.com/gj199575/volcano-descheduler/cmd/app"
	_ "github.com/gj199575/volcano-descheduler/pkg/plugins/loadAware"

	_ "github.com/gj199575/volcano-descheduler/pkg/plugins/binpack"

	"k8s.io/component-base/cli"
)

func main() {
	out := os.Stdout
	cmd := app.NewDeschedulerCommand(out)
	cmd.AddCommand(app.NewVersionCommand())

	code := cli.Run(cmd)
	os.Exit(code)
}
