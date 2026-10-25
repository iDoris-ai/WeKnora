package middleware

import (
	"fmt"
	"runtime/debug"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Recovery is a middleware that recovers from panics
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// Get request ID from context
				ctx := c.Request.Context()
				requestID, _ := c.Get("RequestID")

				// Print stacktrace
				stacktrace := debug.Stack()
				// Log error with structured logger
				// PanicValue is already reduced to the value's type and length; ErrorWithFields keeps only the
				// length of the error text (A24-OBS-16), so the description goes in its own field.
				panicValue := logger.PanicValue(err)
				logger.ErrorWithFields(ctx, fmt.Errorf("panic: %s", panicValue), logrus.Fields{
					"request_id": requestID,
					"panic":      panicValue,
					"stacktrace": string(stacktrace),
				})

				// 返回500错误。panic 值可能带内部信息或正文，不返回给客户端（A24-OBS-07）
				c.AbortWithStatusJSON(500, gin.H{
					"error":   "Internal Server Error",
					"message": "Internal Server Error",
				})
			}
		}()

		c.Next()
	}
}
