package main

import (
	"os"

	_ "github.com/gj199575/volcano-descheduler/pkg/plugins/nodeutilization"
	"k8s.io/component-base/cli"
	"sigs.k8s.io/descheduler/cmd/descheduler/app"
)

func main() {
	out := os.Stdout
	cmd := app.NewDeschedulerCommand(out)
	cmd.AddCommand(app.NewVersionCommand())

	code := cli.Run(cmd)
	os.Exit(code)
}
