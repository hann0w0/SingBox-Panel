package panel

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hann0w0/singbox-panel/internal/domain/singbox"
)

func (a *App) getSchemas(c *gin.Context) {
	c.JSON(http.StatusOK, singbox.GetSchemaBundle())
}
