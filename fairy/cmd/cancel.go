package cmd

import (
	"fmt"

	fairyruntime "fairy/runtime"
	"github.com/spf13/cobra"
)

func newCancelCmd() *cobra.Command {
	var conversationID string
	var turnID string
	cmd := &cobra.Command{
		Use:   "cancel",
		Short: "Cancel an in-flight companion turn",
		RunE: func(cmd *cobra.Command, args []string) error {
			if conversationID == "" || turnID == "" {
				return fmt.Errorf("--conversation and --turn are required")
			}
			root, err := configRootFlag(cmd)
			if err != nil {
				return err
			}
			rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root})
			if err != nil {
				return err
			}
			defer rt.Close()
			return rt.Companion.CancelTurn(conversationID, turnID)
		},
	}
	cmd.Flags().StringVar(&conversationID, "conversation", "", "conversation id")
	cmd.Flags().StringVar(&turnID, "turn", "", "turn id")
	return cmd
}
