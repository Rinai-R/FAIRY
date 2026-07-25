package observation

import interaction "fairy/internal/domain/interaction"

type (
	Resolved      = interaction.Resolved
	Facts         = interaction.Facts
	EndpointKind  = interaction.EndpointKind
	AudienceKind  = interaction.AudienceKind
	InitiationKind = interaction.InitiationKind
	PresentationKind = interaction.PresentationKind
	PrincipalKind = interaction.PrincipalKind
	MemoryPolicy  = interaction.MemoryPolicy
)

const (
	EndpointDesktop = interaction.EndpointDesktop
	EndpointIM      = interaction.EndpointIM

	AudienceSingle = interaction.AudienceSingle
	AudienceMulti  = interaction.AudienceMulti

	InitiationDirect  = interaction.InitiationDirect
	InitiationAmbient = interaction.InitiationAmbient

	PresentationEmbodied = interaction.PresentationEmbodied
	PresentationChat     = interaction.PresentationChat

	PrincipalOwner    = interaction.PrincipalOwner
	PrincipalExternal = interaction.PrincipalExternal
	PrincipalNone     = interaction.PrincipalNone

	MemoryPersonal = interaction.MemoryPersonal
	MemoryPublic   = interaction.MemoryPublic
)
