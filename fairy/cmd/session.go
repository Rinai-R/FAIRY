package cmd

import (
	"encoding/json"
	"fmt"

	fairyruntime "fairy/runtime"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage companion conversations",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "open",
		Short: "Open or create the active character conversation",
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
			catalog, err := rt.Character.ListCharacters()
			if err != nil {
				return err
			}
			if catalog.Active == nil {
				return fmt.Errorf("no active character; configure one under the config root first")
			}
			bootstrap, err := rt.Memory.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(map[string]any{
				"conversationId": bootstrap.Conversation.ID,
				"characterId":    bootstrap.Conversation.CharacterID,
				"messageCount":   len(bootstrap.Messages),
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	})
	return cmd
}
