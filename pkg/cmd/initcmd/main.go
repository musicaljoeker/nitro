package initcmd

import (
	"fmt"

	"github.com/craftcms/nitro/pkg/client"
	"github.com/spf13/cobra"
)

// InitCommand is the command for creating new development environments
var InitCommand = &cobra.Command{
	Use:   "init",
	Short: "Create environment",
	RunE:  initMain,
}

func initMain(cmd *cobra.Command, args []string) error {
	env := cmd.Flag("environment").Value.String()

	// create the new client
	nitro, err := client.NewClient()
	if err != nil {
		return fmt.Errorf("unable to create a client for docker, %w", err)
	}

	return nitro.Init(cmd.Context(), env, args)
}