package panel

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Request-ID", uuid.NewString())
		c.Next()
	}
}
