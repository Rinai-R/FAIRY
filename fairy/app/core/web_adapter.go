package core

import (
	"context"

	turn "fairy/agent/conversation"
	initiative "fairy/agent/presence"
	"fairy/transport/session"
	api "fairy/transport/web"
)

// turnAPIAdapter keeps transport concerns out of turn while preserving the
// existing Session protocol projection.
type turnAPIAdapter struct{ service *turn.Service }

func (a turnAPIAdapter) OutputCapabilities(conversationID string) session.OutputCapabilities {
	return a.service.OutputCapabilities(conversationID)
}

func (a turnAPIAdapter) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	return a.service.ReportExpressionDelivery(result)
}

func (a turnAPIAdapter) BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	return a.service.BindOutputCapabilities(ownerID, conversationID, capabilities)
}

func (a turnAPIAdapter) UnbindOutputCapabilities(ownerID, conversationID string) {
	a.service.UnbindOutputCapabilities(ownerID, conversationID)
}

func (a turnAPIAdapter) SubmitTurn(request api.TurnSubmission) (any, error) {
	return a.service.SubmitTurn(turn.SubmitTurnRequest{
		ConversationID: request.ConversationID,
		Input:          request.Input,
	})
}

func (a turnAPIAdapter) CancelTurn(conversationID, turnID string) error {
	return a.service.CancelTurn(conversationID, turnID)
}

func (a turnAPIAdapter) BindInteraction(conversationID string, binding session.Binding) error {
	return a.service.BindInteraction(conversationID, binding)
}

func (a turnAPIAdapter) ActiveBackgroundJobs() int64 { return a.service.ActiveBackgroundJobs() }

func (a turnAPIAdapter) AgentLoopMetrics() api.AgentLoopMetrics {
	metrics := a.service.AgentLoopMetrics()
	return api.AgentLoopMetrics{
		ProviderFirstByte: latencyMetricsProjection(metrics.ProviderFirstByte),
		ReplyPreview:      latencyMetricsProjection(metrics.ReplyPreview),
		FirstBeat:         latencyMetricsProjection(metrics.FirstBeat),
		Completed:         latencyMetricsProjection(metrics.Completed),
	}
}

func latencyMetricsProjection(metrics turn.LatencyMetrics) api.LatencyMetrics {
	return api.LatencyMetrics{
		Observations: metrics.Observations, TotalDurationMS: metrics.TotalDurationMS,
		MaxDurationMS: metrics.MaxDurationMS,
	}
}

// initiativeAPIAdapter translates public Session facts into Presence inputs
// and projects private control data away from responses.
type initiativeAPIAdapter struct{ service *initiative.Service }

func (a initiativeAPIAdapter) ObserveAmbient(conversationID string, observation session.AmbientObservation) error {
	return a.service.ObserveAmbient(conversationID, initiative.AmbientObservation{
		MessageID: observation.MessageID, SenderID: observation.SenderID,
		SenderName: observation.SenderName, Text: observation.Text,
		Mentions:      append([]session.MessageMention(nil), observation.Mentions...),
		DirectedToBot: observation.DirectedToBot, IsNew: observation.IsNew,
		TimestampUnixMS: observation.TimestampUnixMS,
	})
}

func (a initiativeAPIAdapter) ObserveDesktop(conversationID string, observation session.DesktopObservation) (api.DesktopObservationResult, error) {
	result, err := a.service.ObserveDesktop(conversationID, observation)
	if err != nil {
		return api.DesktopObservationResult{}, err
	}
	projection := api.DesktopObservationResult{
		Action: string(result.Action), OmitReasons: append([]string(nil), result.OmitReasons...),
		Nodes:       make([]api.DesktopObservationStep, len(result.Nodes)),
		Diagnostics: make([]api.DesktopObservationDiagnostic, len(result.Diagnostics)),
	}
	for index, node := range result.Nodes {
		projection.Nodes[index] = api.DesktopObservationStep{
			ID: node.ID, Kind: node.Kind, Depends: append([]string(nil), node.Depends...), OmitCode: node.OmitCode,
		}
	}
	for index, diagnostic := range result.Diagnostics {
		projection.Diagnostics[index] = api.DesktopObservationDiagnostic{
			Node: diagnostic.Node, Kind: diagnostic.Kind, Status: diagnostic.Status,
		}
	}
	return projection, nil
}

func (a initiativeAPIAdapter) DecideParticipation(ctx context.Context, conversationID string, request session.ParticipationRequest) (session.ParticipationResponse, error) {
	messages := make([]initiative.AmbientObservation, len(request.Messages))
	for index, observation := range request.Messages {
		messages[index] = initiative.AmbientObservation{
			MessageID: observation.MessageID, SenderID: observation.SenderID,
			SenderName: observation.SenderName, Text: observation.Text,
			Mentions:      append([]session.MessageMention(nil), observation.Mentions...),
			DirectedToBot: observation.DirectedToBot, IsNew: observation.IsNew,
			TimestampUnixMS: observation.TimestampUnixMS,
		}
	}
	result, err := a.service.DecideParticipation(ctx, initiative.ParticipationRequest{
		ConversationID: conversationID, EvaluationReason: initiative.ParticipationEvaluationReason(request.EvaluationReason),
		Messages: messages,
	})
	if err != nil {
		return session.ParticipationResponse{}, err
	}
	return session.ParticipationResponse{
		Action: string(result.Action), TargetMessageID: result.TargetMessageID, WaitSeconds: result.WaitSeconds,
	}, nil
}

func (a initiativeAPIAdapter) ExperienceStats() api.ExperienceStats {
	stats := a.service.ExperienceStats()
	return api.ExperienceStats{
		Learning: api.LearningQueueStats{
			Enqueued: stats.Learning.Enqueued, Dropped: stats.Learning.Dropped,
			Succeeded: stats.Learning.Succeeded, Failed: stats.Learning.Failed,
		},
		Feedback: api.FeedbackQueueStats{
			Registered: stats.Feedback.Registered, Dropped: stats.Feedback.Dropped,
			Succeeded: stats.Feedback.Succeeded, Failed: stats.Feedback.Failed,
		},
		CacheIdentityVersion: stats.CacheIdentityVersion,
	}
}

var _ api.TurnRuntime = turnAPIAdapter{}
var _ api.InitiativeRuntime = initiativeAPIAdapter{}
