package httpapi

import (
	"cfw-policy-llm-agent/server/internal/app"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

type Options struct {
	WebDir          string
	Plans           *app.PlanService
	SensitiveValues []string
}

var spaRoute = regexp.MustCompile(`^/(?:overview|findings|e0|inventory(?:/[A-Za-z0-9_-]{1,128})?|audits(?:/[A-Za-z0-9_-]{1,128})?|reports(?:/[A-Za-z0-9_-]{1,128})?)$`)
var bundledAsset = regexp.MustCompile(`^/assets/[A-Za-z0-9_-]+\.(?:js|css|woff2|svg|png|webp)$`)

func (a *API) configureStatic(options []Options) error {
	if len(options) > 1 {
		return errors.New("one HTTP options object expected")
	}
	if len(options) == 0 || options[0].WebDir == "" {
		return nil
	}
	dir, err := filepath.Abs(options[0].WebDir)
	if err != nil {
		return errors.New("invalid web directory")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return errors.New("web build is unavailable")
	}
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("web index is unavailable")
	}
	a.webDir = dir
	return nil
}

func (a *API) noRoute(c *gin.Context) {
	path := c.Request.URL.Path
	if a.webDir != "" && (c.Request.Method == "GET" || c.Request.Method == "HEAD") {
		file := ""
		if path == "/" || spaRoute.MatchString(path) {
			file = "index.html"
		} else if path == "/favicon.svg" || bundledAsset.MatchString(path) {
			file = strings.TrimPrefix(path, "/")
		}
		if file != "" {
			resolved, err := filepath.EvalSymlinks(filepath.Join(a.webDir, file))
			if err == nil {
				rel, err := filepath.Rel(a.webDir, resolved)
				info, statErr := os.Stat(resolved)
				if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) && statErr == nil && info.Mode().IsRegular() {
					c.Header("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'")
					c.File(resolved)
					return
				}
			}
		}
	}
	fail(c, 404, "NOT_FOUND", "route not found")
}
