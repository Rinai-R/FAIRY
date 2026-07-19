package cmd

import (
	"encoding/json"
	"fmt"

	"fairy/companion"
	fairyruntime "fairy/runtime"
	"github.com/spf13/cobra"
)

func newTurnCmd() *cobra.Command {
	var conversationID string
	var speech bool
	cmd := &cobra.Command{
		Use:   "turn [text]",
		Short: "Submit a companion turn (harness events print as JSONL on stdout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := configRootFlag(cmd)
			if err != nil {
				return err
			}
			rt, err := fairyruntime.Open(fairyruntime.Options{ConfigRoot: root, LogEventsJSONL: true})
			if err != nil {
				return err
			}
			defer rt.Close()
			if conversationID == "" {
				catalog, err := rt.Character.ListCharacters()
				if err != nil {
					return err
				}
				if catalog.Active == nil {
					return fmt.Errorf("no active character")
				}
				bootstrap, err := rt.Memory.OpenOrCreateCharacterConversation(catalog.Active.CharacterID)
				if err != nil {
					return err
				}
				conversationID = bootstrap.Conversation.ID
			}
			outcome, err := rt.Companion.SubmitTurn(companion.SubmitTurnRequest{
				ConversationID: conversationID,
				Input:          args[0],
				SpeechEnabled:  speech,
			})
			if err != nil {
				return err
			}
			line, _ := json.Marshal(map[string]any{"type": "outcome", "outcome": outcome})
			fmt.Println(string(line))
			return nil
		},
	}
	cmd.Flags().StringVar(&conversationID, "conversation", "", "conversation id (default: active character session)")
	cmd.Flags().BoolVar(&speech, "speech", false, "enable TTS synthesis when configured")
	return cmd
}
