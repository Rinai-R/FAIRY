package cmd

import (
	"encoding/json"
	"fmt"

	fairyruntime "fairy/runtime"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print Core bootstrap and readiness status",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := configRootFlag(cmd)
			if err != nil {
				return err
			}
			rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root})
			if err != nil {
				return err
			}
			defer rt.Close()
			bootstrap, err := rt.Bootstrap.Status()
			if err != nil {
				return err
			}
			web, err := rt.Config.WebSearchStatus()
			if err != nil {
				return err
			}
			semantic, err := rt.Config.SemanticEmbeddingStatus()
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(map[string]any{
				"bootstrap":            bootstrap,
				"configRoot":           rt.ConfigRoot,
				"webSearch":            web,
				"semanticEmbedding":    semantic,
				"activeBackgroundJobs": rt.Companion.ActiveBackgroundJobs(),
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}
