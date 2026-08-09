package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"fairy/transport/session"
)

func newSessionCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "session", Short: "Manage debug sessions", GroupID: "debug"}
	var endpoint, endpointKey, audience, initiation, presentation, principalNamespace, principalSubject string
	open := &cobra.Command{
		Use: "open", Short: "Open a character conversation", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			interactionContext := session.Context{
				Audience: session.AudienceKind(audience), Initiation: session.InitiationKind(initiation),
				Presentation: session.PresentationKind(presentation),
			}
			if principalNamespace != "" || principalSubject != "" {
				interactionContext.Principal = &session.PrincipalRef{Namespace: principalNamespace, Subject: principalSubject}
			}
			endpointKind := session.EndpointKind(endpoint)
			if err := interactionContext.Validate(endpointKind); err != nil {
				return err
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			result, err := client.OpenSession(command.Context(), session.OpenSessionRequest{Endpoint: endpointKind, EndpointKey: endpointKey, Interaction: interactionContext})
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
			var request session.ParticipationRequest
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
	send := &cobra.Command{
		Use: "send", Short: "Submit a turn and stream turn events", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(input) == "" {
				return errors.New("conversation and input are required")
			}
			client, config, err := newClient(v, deps)
			if err != nil {
				return err
			}
			return sendTurn(command, client, config, conversationID, session.SubmitTurnRequest{Input: input})
		},
	}
	send.Flags().StringVar(&conversationID, "conversation", "", "conversation ID")
	send.Flags().StringVar(&input, "input", "", "user input")
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
	event session.TurnEvent
	err   error
}

type turnResult struct {
	response session.SubmitTurnResponse
	err      error
}

type expressionDeliveryReporter interface {
	ReportExpressionDelivery(context.Context, session.ExpressionDeliveryResult) error
}

type cliBeatReadyPayload struct {
	Type        string `json:"type"`
	BeatID      string `json:"beatId"`
	Kind        string `json:"kind"`
	DisplayText string `json:"displayText"`
	Part        *struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"part"`
}

func sendTurn(command *cobra.Command, client APIClient, config ConnectionConfig, conversationID string, request session.SubmitTurnRequest) error {
	stream, err := client.OpenEvents(command.Context(), conversationID, config.Timeout)
	if err != nil {
		return err
	}
	defer stream.Close()
	events := make(chan eventResult, 1)
	go readTurnEvents(stream, events)
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
				// A completed stream may close while the submit response is
				// still in flight. The terminal event is authoritative; keep
				// waiting for the correlated submit result instead of turning
				// the post-terminal EOF into a failed command.
				if terminal != "" {
					continue
				}
				if command.Context().Err() != nil {
					return command.Context().Err()
				}
				return result.err
			}
			if err := writeJSONLine(command.OutOrStdout(), result.event); err != nil {
				return err
			}
			if err := acknowledgeCLIFinalUtterance(command.Context(), stream, result.event); err != nil {
				return err
			}
			switch result.event.State {
			case "completed", "failed", "interrupted":
				terminal = result.event.State
			}
		}
	}
}

func acknowledgeCLIFinalUtterance(ctx context.Context, stream session.EventStream, event session.TurnEvent) error {
	delivery, ok := cliFinalUtteranceDelivery(event)
	if !ok {
		return nil
	}
	reporter, ok := stream.(expressionDeliveryReporter)
	if !ok {
		return errors.New("session event stream cannot report expression delivery")
	}
	if err := reporter.ReportExpressionDelivery(ctx, delivery); err != nil {
		return fmt.Errorf("report final expression delivery: %w", err)
	}
	return nil
}

func cliFinalUtteranceDelivery(event session.TurnEvent) (session.ExpressionDeliveryResult, bool) {
	if strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(event.TurnID) == "" {
		return session.ExpressionDeliveryResult{}, false
	}
	var payload cliBeatReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Type != "beat.ready" || payload.Kind != "final" {
		return session.ExpressionDeliveryResult{}, false
	}
	if payload.BeatID == "" || strings.TrimSpace(payload.BeatID) != payload.BeatID {
		return session.ExpressionDeliveryResult{}, false
	}
	if payload.Part != nil && payload.Part.Kind != "utterance" {
		return session.ExpressionDeliveryResult{}, false
	}
	text := strings.TrimSpace(payload.DisplayText)
	if text == "" && payload.Part != nil && payload.Part.Kind == "utterance" {
		text = strings.TrimSpace(payload.Part.Text)
	}
	if text == "" {
		return session.ExpressionDeliveryResult{}, false
	}
	return session.ExpressionDeliveryResult{
		ConversationID: event.ConversationID,
		TurnID:         event.TurnID,
		BeatID:         payload.BeatID,
		Status:         session.ExpressionDeliverySucceeded,
	}, true
}

func readTurnEvents(stream session.EventStream, results chan<- eventResult) {
	for {
		event, err := stream.Next()
		if err != nil {
			results <- eventResult{err: err}
			return
		}
		decoded, err := session.DecodeTurnEvent(event)
		if err != nil {
			results <- eventResult{err: err}
			return
		}
		results <- eventResult{event: decoded}
	}
}

func newEventsCmd(v *viper.Viper, deps Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "events", Short: "Follow session turn events", GroupID: "debug"}
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
				decoded, err := session.DecodeTurnEvent(event)
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
