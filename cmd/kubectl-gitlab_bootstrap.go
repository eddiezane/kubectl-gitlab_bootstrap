package main

import (
	"os"

	"github.com/spf13/pflag"

	"gitlab.com/eddiezane/kubectl-gitlab_bootstrap/pkg/cmd"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func main() {
	flags := pflag.NewFlagSet("kubectl-gitlab_bootstrap", pflag.ExitOnError)
	pflag.CommandLine = flags

	root := cmd.NewCmdGitLabBootstrap(genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
