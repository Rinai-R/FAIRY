package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"fairy/coreclient"
	"fairy/interaction"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func newSessionCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "session", Short: "Manage debug sessions", GroupID: "debug"}
	var endpoint, endpointKey, audience, initiation, presentation, principalNamespace, principalSubject string
	open := &cobra.Command{
		Use: "open", Short: "Open a character conversation", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			interactionContext := interaction.Context{
				Audience: interaction.AudienceKind(audience), Initiation: interaction.InitiationKind(initiation),
				Presentation: interaction.PresentationKind(presentation),
			}
			if principalNamespace != "" || principalSubject != "" {
				interactionContext.Principal = &interaction.PrincipalRef{Namespace: principalNamespace, Subject: principalSubject}
			}
			endpointKind := interaction.EndpointKind(endpoint)
			if err := interactionContext.Validate(endpointKind); err != nil {
				return err
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.OpenSession(command.Context(), coreclient.OpenSessionRequest{Endpoint: endpointKind, EndpointKey: endpointKey, Interaction: interactionContext})
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, result)
		},
	}
	open.Flags().StringVar(&endpoint, "endpoint-kind", "", "endpoint kind: desktop or im")
	open.Flags().StringVar(&endpointKey, "endpoint-key", "", "stable opaque endpoint conversation key")
	open.Flags().StringVar(&audience, "audience", "", "audience shape: single or multi")
	open.Flags().StringVar(&initiation, "initiation", "", "initiation mode: direct or ambient")
	open.Flags().StringVar(&presentation, "presentation", "", "presentation mode: embodied or chat")
	open.Flags().StringVar(&principalNamespace, "principal-namespace", "", "authenticated principal namespace for single-user IM")
	open.Flags().StringVar(&principalSubject, "principal-subject", "", "authenticated principal subject for single-user IM")
	for _, name := range []string{"endpoint-kind", "endpoint-key", "audience", "initiation", "presentation"} {
		_ = open.MarkFlagRequired(name)
	}

	var conversationID, participationFile string
	participate := &cobra.Command{
		Use: "participate", Short: "Evaluate an ambient group snapshot", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			payload, err := readPayload(participationFile, deps.Stdin)
			if err != nil {
				return err
			}
			var request coreclient.ParticipationRequest
			if err := decodeStrictCLIObject(payload, &request); err != nil {
				return fmt.Errorf("decode participation request: %w", err)
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.DecideParticipation(command.Context(), conversationID, request)
			if err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, result)
		},
	}
	participate.Flags().StringVar(&conversationID, "conversation", "", "conversation ID")
	participate.Flags().StringVar(&participationFile, "file", "", "JSON file path, or - for stdin")
	_ = participate.MarkFlagRequired("conversation")
	_ = participate.MarkFlagRequired("file")
	command.AddCommand(open, participate)
	return command
}

func decodeStrictCLIObject(payload []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("payload must contain exactly one JSON object")
	}
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("payload must be a JSON object")
	}
	return nil
}

func newTurnCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "turn", Short: "Send or cancel debug turns", GroupID: "debug"}
	var conversationID, input string
	var speech bool
	send := &cobra.Command{
		Use: "send", Short: "Submit a turn and stream harness events", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(input) == "" {
				return errors.New("conversation and input are required")
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			return sendTurn(command, client, config, conversationID, coreclient.SubmitTurnRequest{
				Input: input, SpeechEnabled: speech,
			})
		},
	}
	send.Flags().StringVar(&conversationID, "conversation", "", "conversation ID")
	send.Flags().StringVar(&input, "input", "", "user input")
	send.Flags().BoolVar(&speech, "speech", false, "request speech synthesis")
	_ = send.MarkFlagRequired("conversation")
	_ = send.MarkFlagRequired("input")

	var cancelConversation, turnID string
	cancel := &cobra.Command{
		Use: "cancel", Short: "Cancel an active turn", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			if err := client.CancelTurn(command.Context(), cancelConversation, turnID); err != nil {
				return err
			}
			return writeOutput(command.OutOrStdout(), config.Output, map[string]bool{"ok": true})
		},
	}
	cancel.Flags().StringVar(&cancelConversation, "conversation", "", "conversation ID")
	cancel.Flags().StringVar(&turnID, "turn", "", "turn ID")
	_ = cancel.MarkFlagRequired("conversation")
	_ = cancel.MarkFlagRequired("turn")
	command.AddCommand(send, cancel)
	return command
}

type eventResult struct {
	event coreclient.HarnessEvent
	err   error
}

type turnResult struct {
	response coreclient.SubmitTurnResponse
	err      error
}

func sendTurn(command *cobra.Command, client APIClient, config ConnectionConfig, conversationID string, request coreclient.SubmitTurnRequest) error {
	stream, err := client.OpenEvents(command.Context(), conversationID, config.Timeout)
	if err != nil {
		return err
	}
	defer stream.Close()
	events := make(chan eventResult, 1)
	go readHarnessEvents(stream, events)
	turns := make(chan turnResult, 1)
	go func() {
		response, err := client.SubmitTurn(command.Context(), conversationID, request)
		turns <- turnResult{response: response, err: err}
	}()

	var terminal string
	var submitted *turnResult
	for {
		if terminal != "" && submitted != nil {
			if terminal != "completed" {
				return terminalError(terminal)
			}
			return submitted.err
		}
		select {
		case <-command.Context().Done():
			return command.Context().Err()
		case result := <-turns:
			submitted = &result
			if result.err != nil && terminal == "" {
				return result.err
			}
		case result := <-events:
			if result.err != nil {
				if command.Context().Err() != nil {
					return command.Context().Err()
				}
				return result.err
			}
			if err := writeJSONLine(command.OutOrStdout(), result.event); err != nil {
				return err
			}
			switch result.event.State {
			case "completed", "failed", "interrupted":
				terminal = result.event.State
			}
		}
	}
}

func readHarnessEvents(stream coreclient.EventStream, results chan<- eventResult) {
	for {
		event, err := stream.Next()
		if err != nil {
			results <- eventResult{err: err}
			return
		}
		decoded, err := coreclient.DecodeHarnessEvent(event)
		if err != nil {
			results <- eventResult{err: err}
			return
		}
		results <- eventResult{event: decoded}
	}
}

func newEventsCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "events", Short: "Follow session harness events", GroupID: "debug"}
	var conversationID string
	follow := &cobra.Command{
		Use: "follow", Short: "Follow events as JSONL", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			stream, err := client.OpenEvents(command.Context(), conversationID, config.Timeout)
			if err != nil {
				return err
			}
			defer stream.Close()
			for {
				event, err := stream.Next()
				if err != nil {
					if command.Context().Err() != nil {
						return nil
					}
					return fmt.Errorf("event stream disconnected: %w", err)
				}
				decoded, err := coreclient.DecodeHarnessEvent(event)
				if err != nil {
					return err
				}
				if err := writeJSONLine(command.OutOrStdout(), decoded); err != nil {
					return err
				}
			}
		},
	}
	follow.Flags().StringVar(&conversationID, "conversation", "", "conversation ID")
	_ = follow.MarkFlagRequired("conversation")
	command.AddCommand(follow)
	return command
}
