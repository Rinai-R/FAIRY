package conversation

import (
	"errors"
	"fmt"
	"strings"

	interactionruntime "fairy/agent/conversation/interaction"
	"fairy/transport/session"
)

const OutputCapabilityLeaseCapacity = interactionruntime.DefaultCapabilityLeaseCapacity

var ErrOutputCapabilityCapacity = interactionruntime.ErrCapabilityCapacity

func (s *Service) BindInteraction(conversationID string, binding session.Binding) error {
	if s == nil {
		return errors.New("companion service is nil")
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return errors.New("conversation_id is required")
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	return s.interactions.Put(conversationID, binding)
}

func (s *Service) ResolveInteraction(conversationID string) (session.Resolved, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return session.Resolved{}, errors.New("conversation_id is required")
	}
	if s == nil || s.interactions == nil {
		return session.Resolved{}, ErrTurnRuntimeUnavailable
	}
	binding, found := s.interactions.Get(conversationID)
	if !found {
		if s.memory.ambient.bindings == nil {
			return session.Resolved{}, ErrTurnRuntimeUnavailable
		}
		var err error
		binding, found, err = s.memory.ambient.bindings.LookupEndpointForConversation(conversationID)
		if err != nil {
			return session.Resolved{}, fmt.Errorf("looking up durable interaction binding: %w", err)
		}
		if !found {
			return session.Resolved{}, errors.New("conversation has no interaction binding")
		}
		if err := s.BindInteraction(conversationID, binding); err != nil {
			return session.Resolved{}, err
		}
	}
	ownerBound := false
	if binding.Endpoint == session.EndpointIM && binding.Facts.Audience == session.AudienceSingle {
		if s.identities == nil {
			return session.Resolved{}, errors.New("owner identity resolver is required for single-user IM interaction")
		}
		var err error
		ownerBound, err = s.identities.IsOwner(binding.Facts.PrincipalNamespace, binding.Facts.PrincipalDigest)
		if err != nil {
			return session.Resolved{}, fmt.Errorf("resolving interaction principal: %w", err)
		}
	}
	return session.ResolveBinding(binding, ownerBound)
}

func (s *Service) BoundInteraction(conversationID string) (session.Binding, bool) {
	if s == nil || s.interactions == nil {
		return session.Binding{}, false
	}
	return s.interactions.Get(conversationID)
}

func (s *Service) clearInteractionBindings() {
	if s != nil && s.interactions != nil {
		s.interactions.Clear()
	}
}

// BindOutputCapabilities records the explicitly advertised output support of a
// live Surface connection. The process-local interaction package owns leases.
func (s *Service) BindOutputCapabilities(ownerID, conversationID string, capabilities session.OutputCapabilities) error {
	if s == nil || s.outputCapabilities == nil {
		return errors.New("companion service is nil")
	}
	return s.outputCapabilities.Bind(ownerID, conversationID, capabilities)
}

func (s *Service) UnbindOutputCapabilities(ownerID, conversationID string) {
	if s != nil && s.outputCapabilities != nil {
		s.outputCapabilities.Unbind(ownerID, conversationID)
	}
}

func (s *Service) OutputCapabilities(conversationID string) session.OutputCapabilities {
	if s == nil || s.outputCapabilities == nil {
		return session.OutputCapabilities{}
	}
	return s.outputCapabilities.Resolve(conversationID)
}

func (s *Service) clearOutputCapabilities() {
	if s != nil && s.outputCapabilities != nil {
		s.outputCapabilities.Clear()
	}
}
