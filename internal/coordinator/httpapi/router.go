// Package httpapi wires the coordinator REST routes and middleware.
package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"

	"github.com/kurwall/kurwall/internal/coordinator/domain"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/handler"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/middleware"
	"github.com/kurwall/kurwall/internal/coordinator/httpapi/response"
	"github.com/kurwall/kurwall/internal/coordinator/live"
	"github.com/kurwall/kurwall/internal/coordinator/service"
)

// Deps are the dependencies required to build the HTTP router.
type Deps struct {
	Nodes          *service.Nodes
	Policies       *service.Policies
	Events         *service.Events
	ASN            *service.ASN
	Live           *live.Hub
	Auth           *service.Auth
	Deploy         *service.Deploy
	Version        string
	Settings       domain.AgentSettings
	WebStaticDir   string
	APIToken       string
	PublicURL      string
	GRPCPublicAddr string
	GRPCServerName string
	GRPCAddr       string
	AgentDistDir   string
	AgentCAFile    string
	Logger         zerolog.Logger
	DBHealth       func(ctx context.Context) error
}

// New builds the Echo router.
func New(d Deps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		_ = response.Error(c, err)
	}

	e.Use(middleware.Recover())
	e.Use(middleware.Logger(d.Logger))
	e.Use(middleware.Trace())
	e.Use(middleware.RequestLog())
	e.Use(middleware.BodyLimit(4 << 20))
	e.Use(echomw.GzipWithConfig(echomw.GzipConfig{
		Skipper: func(c echo.Context) bool {
			return websocket.IsWebSocketUpgrade(c.Request())
		},
	}))
	e.Use(securityHeaders())

	h := handler.New(d.Nodes, d.Policies, d.Events, d.ASN, d.Live, d.Version, d.Settings)
	h.Auth = d.Auth
	h.Deploy = d.Deploy
	h.DBHealth = d.DBHealth
	h.PublicURL = d.PublicURL
	h.GRPCPublicAddr = d.GRPCPublicAddr
	h.GRPCServerName = d.GRPCServerName
	h.GRPCAddr = d.GRPCAddr
	h.AgentDistDir = d.AgentDistDir
	h.AgentCAFile = d.AgentCAFile

	e.GET("/ping", h.Ping)
	e.GET("/api/v1/health", h.Health)
	e.GET("/api/v1/meta", h.Meta)
	e.GET("/api/v1/stream", h.Stream)
	e.POST("/api/v1/auth/login", h.Login)
	e.POST("/api/v1/enroll", h.Enroll)
	e.GET("/install.sh", h.InstallScript)
	e.HEAD("/install.sh", h.InstallScript)
	e.GET("/agent/*", h.AgentAsset)
	e.HEAD("/agent/*", h.AgentAsset)

	api := e.Group("/api/v1", middleware.Auth(d.Auth))
	api.GET("/auth/me", h.Me)
	api.POST("/auth/logout", h.Logout)

	api.GET("/nodes", h.ListNodes)
	api.GET("/nodes/:id", h.GetNode)
	api.GET("/nodes/:id/snapshot", h.GetNodeSnapshot)

	api.GET("/policies", h.ListPolicies)
	api.GET("/policies/:id", h.GetPolicy)
	api.GET("/policies/:id/rules", h.ListRules)

	api.GET("/events/flows", h.ListFlows)
	api.GET("/events/dns", h.ListDNS)
	api.GET("/events/blocks", h.ListBlocks)
	api.GET("/events/traffic", h.ListTraffic)
	api.GET("/stats/overview", h.Stats)
	api.POST("/stream-ticket", h.StreamTicket)
	api.GET("/asns", h.ListASNs)
	api.GET("/asns/countries", h.ListASNCountries)
	api.GET("/asns/:asn", h.LookupASN)

	admin := api.Group("", middleware.RequireAdmin())
	admin.PATCH("/nodes/:id", h.UpdateNode)
	admin.PUT("/nodes/:id/policies", h.AssignNodePolicies)
	admin.POST("/policies", h.CreatePolicy)
	admin.PATCH("/policies/:id", h.UpdatePolicy)
	admin.DELETE("/policies/:id", h.DeletePolicy)
	admin.POST("/policies/:id/rules", h.CreateRule)
	admin.POST("/policies/:id/rules/batch", h.CreateRules)
	admin.PATCH("/policies/:id/rules/:ruleId", h.UpdateRule)
	admin.DELETE("/policies/:id/rules/:ruleId", h.DeleteRule)
	admin.POST("/deploy-keys", h.CreateDeployKey)
	admin.GET("/deploy-keys", h.ListDeployKeys)
	admin.DELETE("/deploy-keys/:id", h.DeleteDeployKey)

	api.Any("/*", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusNotFound, "route not found")
	})

	registerStatic(e, d.WebStaticDir)
	return e
}

func securityHeaders() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; font-src 'self'; frame-ancestors 'none'")
			return next(c)
		}
	}
}

func registerStatic(e *echo.Echo, dir string) {
	if dir == "" {
		return
	}
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return
	}
	assets := filepath.Join(dir, "assets")
	if _, err := os.Stat(assets); err == nil {
		e.Static("/assets", assets)
	}
	for _, name := range []string{"favicon-32x32.png", "favicon-16x16.png", "favicon-192x192.png", "favicon.ico", "apple-touch-icon.png", "logo-192.png"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			e.File("/"+name, filepath.Join(dir, name))
		}
	}
	serveIndex := func(c echo.Context) error {
		return c.File(index)
	}
	e.GET("/", serveIndex)
	e.GET("/*", serveIndex)
}
