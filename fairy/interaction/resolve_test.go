package interaction

import (
	"strings"
	"testing"
)

func TestResolveInteractionPolicies(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   EndpointKind
		context    Context
		ownerBound bool
		principal  PrincipalKind
		memory     MemoryPolicy
	}{
		{
			name: "private desktop", endpoint: EndpointDesktop,
			context:   Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied},
			principal: PrincipalOwner, memory: MemoryPersonal,
		},
		{
			name: "shared desktop", endpoint: EndpointDesktop,
			context:   Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationEmbodied},
			principal: PrincipalNone, memory: MemoryPublic,
		},
		{
			name: "public IM group", endpoint: EndpointIM,
			context:   Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationChat},
			principal: PrincipalNone, memory: MemoryPublic,
		},
		{
			name: "external IM direct", endpoint: EndpointIM,
			context:   Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat, Principal: principal("onebot", "40001")},
			principal: PrincipalExternal, memory: MemoryPublic,
		},
		{
			name: "owner IM direct", endpoint: EndpointIM,
			context:    Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat, Principal: principal("telegram", "owner-1")},
			ownerBound: true, principal: PrincipalOwner, memory: MemoryPersonal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			digest := ""
			if test.context.Principal != nil {
				digest = strings.Repeat("a", 64)
			}
			binding, err := NewBinding(test.endpoint, test.context, digest)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := ResolveBinding(binding, test.ownerBound)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Principal != test.principal || resolved.Memory != test.memory {
				t.Fatalf("resolved = %#v", resolved)
			}
		})
	}
}

func TestInteractionValidationRejectsMissingAndContradictoryFacts(t *testing.T) {
	tests := []struct {
		name     string
		endpoint EndpointKind
		context  Context
	}{
		{name: "missing endpoint", context: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat}},
		{name: "missing audience", endpoint: EndpointIM, context: Context{Initiation: InitiationDirect, Presentation: PresentationChat}},
		{name: "missing initiation", endpoint: EndpointIM, context: Context{Audience: AudienceMulti, Presentation: PresentationChat}},
		{name: "missing presentation", endpoint: EndpointIM, context: Context{Audience: AudienceMulti, Initiation: InitiationAmbient}},
		{name: "single IM missing principal", endpoint: EndpointIM, context: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat}},
		{name: "multi with principal", endpoint: EndpointIM, context: Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationChat, Principal: principal("onebot", "40001")}},
		{name: "desktop with principal", endpoint: EndpointDesktop, context: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied, Principal: principal("macos", "install")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.context.Validate(test.endpoint); err == nil {
				t.Fatal("invalid interaction accepted")
			}
		})
	}
}

func TestPrincipalValidationPreservesOpaqueSubject(t *testing.T) {
	valid := []PrincipalRef{
		{Namespace: "onebot", Subject: "40001"},
		{Namespace: "telegram.bot_1", Subject: "用户-1"},
	}
	for _, principal := range valid {
		if err := principal.Validate(); err != nil {
			t.Fatalf("Validate(%#v): %v", principal, err)
		}
	}
	invalid := []PrincipalRef{
		{},
		{Namespace: "OneBot", Subject: "1"},
		{Namespace: "onebot", Subject: " 1"},
		{Namespace: "onebot", Subject: "1\n"},
	}
	for _, principal := range invalid {
		if err := principal.Validate(); err == nil {
			t.Fatalf("invalid principal accepted: %#v", principal)
		}
	}
}

func TestResolveRejectsImpossibleOwnerLookup(t *testing.T) {
	group := Context{Audience: AudienceMulti, Initiation: InitiationAmbient, Presentation: PresentationChat}
	groupBinding, err := NewBinding(EndpointIM, group, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBinding(groupBinding, true); err == nil {
		t.Fatal("multi owner lookup accepted")
	}
	desktop := Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationEmbodied}
	desktopBinding, err := NewBinding(EndpointDesktop, desktop, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBinding(desktopBinding, true); err == nil {
		t.Fatal("desktop owner lookup accepted")
	}
}

func TestBindingNeverRetainsRawPrincipalSubject(t *testing.T) {
	context := Context{
		Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat,
		Principal: principal("onebot", "raw-user-40001"),
	}
	binding, err := NewBinding(EndpointIM, context, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if binding.Facts.PrincipalNamespace != "onebot" || binding.Facts.PrincipalDigest != strings.Repeat("b", 64) {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestResolvedValidateRejectsPrivilegeEscalation(t *testing.T) {
	resolved := Resolved{
		Endpoint: EndpointIM,
		Facts: Facts{
			Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat,
			PrincipalNamespace: "qq.onebot", PrincipalDigest: strings.Repeat("a", 64),
		},
		Principal: PrincipalExternal,
		Memory:    MemoryPersonal,
	}
	if err := resolved.Validate(); err == nil {
		t.Fatal("external principal received personal memory policy")
	}
	resolved.Memory = MemoryPublic
	if err := resolved.Validate(); err != nil {
		t.Fatalf("valid external interaction: %v", err)
	}
}

func principal(namespace, subject string) *PrincipalRef {
	return &PrincipalRef{Namespace: namespace, Subject: subject}
}
