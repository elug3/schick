package bootstrap

import (
	"net/http"

	"github.com/elug3/schick/pkg/auth/handler"
	"github.com/gin-gonic/gin"
)

func newRouter(h *handler.Handler, debug bool) *gin.Engine {
	if debug {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	v1 := r.Group("/api/v1/auth")
	{
		v1.POST("/login", h.Login)
		v1.POST("/logout", h.Logout)
		v1.POST("/refresh", h.Refresh)
	}

	return r
}
