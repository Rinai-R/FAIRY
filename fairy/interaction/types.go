package interaction

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type EndpointKind string

const (
	EndpointDesktop EndpointKind = "desktop"
	EndpointIM      EndpointKind = "im"
)

type AudienceKind string

const (
	AudienceSingle AudienceKind = "single"
	AudienceMulti  AudienceKind = "multi"
)

type InitiationKind string

const (
	InitiationDirect  InitiationKind = "direct"
	InitiationAmbient InitiationKind = "ambient"
)

type PresentationKind string

const (
	PresentationEmbodied PresentationKind = "embodied"
	PresentationChat     PresentationKind = "chat"
)

type PrincipalKind string

const (
	PrincipalOwner    PrincipalKind = "owner"
	PrincipalExternal PrincipalKind = "external"
	PrincipalNone     PrincipalKind = "none"
)

type MemoryPolicy string

const (
	MemoryPersonal MemoryPolicy = "personal"
	MemoryPublic   MemoryPolicy = "public"
)

// PrincipalRef is an authenticated Gateway fact. Subject is opaque and must
// be HMAC-digested before persistence or lookup.
type PrincipalRef struct {
	Namespace string `json:"namespace"`
	Subject   string `json:"subject"`
}

type Context struct {
	Audience     AudienceKind     `json:"audience"`
	Initiation   InitiationKind   `json:"initiation"`
	Presentation PresentationKind `json:"presentation"`
	Principal    *PrincipalRef    `json:"principal,omitempty"`
}

// Facts is the durable, raw-subject-free interaction context.
type Facts struct {
	Audience           AudienceKind     `json:"audience"`
	Initiation         InitiationKind   `json:"initiation"`
	Presentation       PresentationKind `json:"presentation"`
	PrincipalNamespace string           `json:"principalNamespace,omitempty"`
	PrincipalDigest    string           `json:"principalDigest,omitempty"`
}

type Binding struct {
	Endpoint EndpointKind `json:"endpoint"`
	Facts    Facts        `json:"interaction"`
}

type Resolved struct {
	Endpoint  EndpointKind  `json:"endpoint"`
	Facts     Facts         `json:"interaction"`
	Principal PrincipalKind `json:"principal"`
	Memory    MemoryPolicy  `json:"memoryPolicy"`
}

func ValidateEndpoint(endpoint EndpointKind) error {
	switch endpoint {
	case EndpointDesktop, EndpointIM:
		return nil
	default:
		return fmt.Errorf("endpoint is invalid: %q", endpoint)
	}
}

func (context Context) Validate(endpoint EndpointKind) error {
	if err := ValidateEndpoint(endpoint); err != nil {
		return err
	}
	switch context.Audience {
	case AudienceSingle, AudienceMulti:
	default:
		return fmt.Errorf("interaction audience is invalid: %q", context.Audience)
	}
	switch context.Initiation {
	case InitiationDirect, InitiationAmbient:
	default:
		return fmt.Errorf("interaction initiation is invalid: %q", context.Initiation)
	}
	switch context.Presentation {
	case PresentationEmbodied, PresentationChat:
	default:
		return fmt.Errorf("interaction presentation is invalid: %q", context.Presentation)
	}

	switch endpoint {
	case EndpointDesktop:
		if context.Principal != nil {
			return errors.New("desktop interaction must not include principal")
		}
	case EndpointIM:
		if context.Audience == AudienceSingle {
			if context.Principal == nil {
				return errors.New("single IM interaction requires principal")
			}
			if err := context.Principal.Validate(); err != nil {
				return err
			}
		} else if context.Principal != nil {
			return errors.New("multi interaction must not include principal")
		}
	}
	return nil
}

func (principal PrincipalRef) Validate() error {
	if err := ValidateNamespace(principal.Namespace); err != nil {
		return err
	}
	if len(principal.Subject) == 0 || len(principal.Subject) > 256 || !utf8.ValidString(principal.Subject) {
		return errors.New("principal subject must be valid UTF-8 with 1-256 bytes")
	}
	if strings.TrimSpace(principal.Subject) != principal.Subject {
		return errors.New("principal subject must not contain surrounding whitespace")
	}
	for _, character := range principal.Subject {
		if character < 0x20 || character == 0x7f {
			return errors.New("principal subject contains control characters")
		}
	}
	return nil
}

func ValidateNamespace(namespace string) error {
	if len(namespace) == 0 || len(namespace) > 64 {
		return errors.New("principal namespace must be 1-64 characters")
	}
	for _, character := range namespace {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return errors.New("principal namespace contains unsupported characters")
	}
	return nil
}

// NewBinding removes the raw subject from a validated session context. The
// caller must supply the HMAC digest for a single-user IM principal.
func NewBinding(endpoint EndpointKind, context Context, principalDigest string) (Binding, error) {
	if err := context.Validate(endpoint); err != nil {
		return Binding{}, err
	}
	facts := Facts{
		Audience: context.Audience, Initiation: context.Initiation, Presentation: context.Presentation,
	}
	if context.Principal != nil {
		if err := ValidateDigest(principalDigest); err != nil {
			return Binding{}, err
		}
		facts.PrincipalNamespace = context.Principal.Namespace
		facts.PrincipalDigest = principalDigest
	} else if principalDigest != "" {
		return Binding{}, errors.New("principal digest requires a principal")
	}
	return Binding{Endpoint: endpoint, Facts: facts}, nil
}

func (binding Binding) Validate() error {
	context := Context{
		Audience: binding.Facts.Audience, Initiation: binding.Facts.Initiation, Presentation: binding.Facts.Presentation,
	}
	if binding.Facts.PrincipalNamespace != "" || binding.Facts.PrincipalDigest != "" {
		if binding.Facts.PrincipalNamespace == "" || binding.Facts.PrincipalDigest == "" {
			return errors.New("principal namespace and digest must be stored together")
		}
		context.Principal = &PrincipalRef{Namespace: binding.Facts.PrincipalNamespace, Subject: "digest-placeholder"}
		if err := ValidateDigest(binding.Facts.PrincipalDigest); err != nil {
			return err
		}
	}
	return context.Validate(binding.Endpoint)
}

// ResolveBinding derives Core-owned principal and memory policy. ownerBound is
// only meaningful for a validated single-user IM principal.
func ResolveBinding(binding Binding, ownerBound bool) (Resolved, error) {
	if err := binding.Validate(); err != nil {
		return Resolved{}, err
	}
	resolved := Resolved{Endpoint: binding.Endpoint, Facts: binding.Facts, Principal: PrincipalNone, Memory: MemoryPublic}
	switch binding.Endpoint {
	case EndpointDesktop:
		if ownerBound {
			return Resolved{}, errors.New("desktop interaction does not use owner identity lookup")
		}
		if binding.Facts.Audience == AudienceSingle {
			resolved.Principal = PrincipalOwner
			resolved.Memory = MemoryPersonal
		}
	case EndpointIM:
		if binding.Facts.Audience == AudienceMulti {
			if ownerBound {
				return Resolved{}, errors.New("multi interaction cannot resolve an owner principal")
			}
			return resolved, nil
		}
		if ownerBound {
			resolved.Principal = PrincipalOwner
			resolved.Memory = MemoryPersonal
		} else {
			resolved.Principal = PrincipalExternal
		}
	}
	return resolved, nil
}

func (resolved Resolved) Validate() error {
	binding := Binding{Endpoint: resolved.Endpoint, Facts: resolved.Facts}
	ownerBound := resolved.Endpoint == EndpointIM && resolved.Principal == PrincipalOwner
	want, err := ResolveBinding(binding, ownerBound)
	if err != nil {
		return err
	}
	if resolved != want {
		return errors.New("resolved interaction policy does not match binding facts")
	}
	return nil
}

func ValidateDigest(value string) error {
	if len(value) != 64 {
		return errors.New("principal digest must be 64 lowercase hexadecimal characters")
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') {
			continue
		}
		return errors.New("principal digest must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func (resolved Resolved) AllowsPersonalMemory() bool {
	return resolved.Memory == MemoryPersonal
}

func (resolved Resolved) AllowsAmbientParticipation() bool {
	return resolved.Facts.Initiation == InitiationAmbient
}
