package handlers

import (
	"html/template"
	"net/http"

	"github.com/rphpires/facial-device-emulator/assets"

	"github.com/gin-gonic/gin"
)

// pageTemplates are the pages wrapped by the base.html shell. Every new
// page must be listed here, otherwise renderPage finds nil in the cache.
var pageTemplates = []string{
	"devices.html",
	"error.html",
}

// shellTemplates are the partials that make up the shell, parsed together
// with each page.
var shellTemplates = []string{
	"web/templates/base.html",
	"web/templates/header.html",
	"web/templates/footer.html",
	"web/templates/sidebar.html",
}

// templateFuncs are the helpers available inside templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"gt":  func(a, b int) bool { return a > b },
		"eq":  func(a, b interface{}) bool { return a == b },
		"ne":  func(a, b interface{}) bool { return a != b },
		"default": func(defaultValue interface{}, value interface{}) interface{} {
			if value == nil {
				return defaultValue
			}
			return value
		},
	}
}

// buildTemplateCache parses every page once, at startup. Templates are
// embedded and never change at runtime, so parsing per request would be
// waste. It panics on an invalid template on purpose: that is a programming
// error, and failing at boot beats failing on the first request.
func buildTemplateCache() map[string]*template.Template {
	cache := make(map[string]*template.Template, len(pageTemplates))

	for _, page := range pageTemplates {
		files := append(append([]string{}, shellTemplates...), "web/templates/"+page)
		cache[page] = template.Must(
			template.New("").Funcs(templateFuncs()).ParseFS(assets.Templates(), files...),
		)
	}

	return cache
}

// renderPage writes a full page to the response.
func (h *Handler) renderPage(c *gin.Context, name string, status int, data gin.H) {
	tmpl := h.templates[name]
	if tmpl == nil {
		h.tracer.Error("Template %q missing from cache", name)
		c.String(http.StatusInternalServerError, "Template unavailable: %s", name)
		return
	}

	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")

	ctx := h.withBaseContext(data)

	// The header is shared by every page and reads counter_cards. Pages
	// such as error.html have no fleet to count; without this default the
	// header aborts mid-render and the page arrives truncated.
	if _, ok := ctx["counter_cards"]; !ok {
		ctx["counter_cards"] = FleetCounts{}.toMap()
	}

	// Log, never swallow: a truncated page must show up in the log.
	if err := tmpl.ExecuteTemplate(c.Writer, "base.html", ctx); err != nil {
		h.tracer.Error("Failed to render %q: %v", name, err)
	}
}
