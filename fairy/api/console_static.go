package api

import (
	"context"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

//go:embed console/*
var consoleEmbed embed.FS

func (s *Server) registerConsoleRoutes() {
	sub, err := fs.Sub(consoleEmbed, "console")
	if err != nil {
		s.logger.Warn("console embed unavailable")
		return
	}
	s.engine.GET("/", func(ctx context.Context, c *app.RequestContext) {
		c.Redirect(http.StatusFound, []byte("/console/"))
	})
	s.engine.GET("/console", func(ctx context.Context, c *app.RequestContext) {
		c.Redirect(http.StatusFound, []byte("/console/"))
	})
	s.engine.GET("/console/", func(ctx context.Context, c *app.RequestContext) {
		serveConsoleFile(c, sub, "index.html")
	})
	s.engine.GET("/console/*filepath", func(ctx context.Context, c *app.RequestContext) {
		rel := strings.TrimPrefix(string(c.Param("filepath")), "/")
		if rel == "" || strings.HasSuffix(rel, "/") {
			rel = path.Join(rel, "index.html")
		}
		serveConsoleFile(c, sub, rel)
	})
}

func serveConsoleFile(c *app.RequestContext, root fs.FS, name string) {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if strings.Contains(name, "..") {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	f, err := root.Open(name)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	c.Data(http.StatusOK, ctype, data)
}
