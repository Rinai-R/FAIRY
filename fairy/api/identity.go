package api

import (
	"context"
	"net/http"

	"fairy/identity"
	"fairy/interaction"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Server) registerIdentityRoutes() {
	v1 := s.engine.Group("/v1")
	v1.Use(s.authMiddleware)
	v1.GET("/identities/owners", s.handleListOwnerIdentities)
	v1.PUT("/identities/owners", s.handleBindOwnerIdentity)
	v1.DELETE("/identities/owners", s.handleUnbindOwnerIdentity)
}

type ownerIdentityBody struct {
	Namespace string `json:"namespace"`
	Subject   string `json:"subject"`
}

func (s *Server) handleListOwnerIdentities(ctx context.Context, c *app.RequestContext) {
	owners, err := s.rt.Identity.ListOwnersContext(ctx)
	if err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, owners)
}

func (s *Server) handleBindOwnerIdentity(ctx context.Context, c *app.RequestContext) {
	var body ownerIdentityBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	principal := interaction.PrincipalRef{Namespace: body.Namespace, Subject: body.Subject}
	digest, err := s.rt.Secret.DigestPrincipal(principal)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if err := s.rt.Identity.BindOwnerContext(ctx, principal.Namespace, digest); err != nil {
		writeErr(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, identity.OwnerIdentity{Namespace: principal.Namespace, PrincipalDigest: digest})
}

func (s *Server) handleUnbindOwnerIdentity(ctx context.Context, c *app.RequestContext) {
	var body ownerIdentityBody
	if err := c.Bind(&body); err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	principal := interaction.PrincipalRef{Namespace: body.Namespace, Subject: body.Subject}
	digest, err := s.rt.Secret.DigestPrincipal(principal)
	if err != nil {
		writeErr(c, http.StatusBadRequest, err)
		return
	}
	if err := s.rt.Identity.UnbindOwnerContext(ctx, principal.Namespace, digest); err != nil {
		status := http.StatusInternalServerError
		if err == identity.ErrOwnerIdentityNotFound {
			status = http.StatusNotFound
		}
		writeErr(c, status, err)
		return
	}
	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}
