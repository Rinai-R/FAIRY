package session

import "errors"

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

type Resolved struct {
	Endpoint  EndpointKind  `json:"endpoint"`
	Facts     Facts         `json:"interaction"`
	Principal PrincipalKind `json:"principal"`
	Memory    MemoryPolicy  `json:"memoryPolicy"`
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

func (resolved Resolved) AllowsPersonalMemory() bool {
	return resolved.Memory == MemoryPersonal
}

func (resolved Resolved) AllowsAmbientParticipation() bool {
	return resolved.Facts.Initiation == InitiationAmbient
}

func (resolved Resolved) IsEvaluation() bool {
	return resolved.Facts.Evaluation
}
