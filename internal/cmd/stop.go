package cmd

import (
	"github.com/spf13/cobra"

	"github.com/craftcms/nitro/config"
	"github.com/craftcms/nitro/internal/action"
	"github.com/craftcms/nitro/internal/nitro"
)

var stopCommand = &cobra.Command{
	Use:   "stop",
	Short: "Stop machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		name := config.GetString("machine", flagMachineName)

		stopAction, err := action.Stop(name)
		if err != nil {
			return err
		}

		return nitro.RunAction(nitro.NewMultipassRunner("multipass"), []action.Action{*stopAction})
	},
}