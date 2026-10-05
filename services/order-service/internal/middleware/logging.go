package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger logs one structured line per request. Using slog now,
// even for something this simple, is deliberate: Phase 5 (observability)
// ships these same logs to Loki/ELK and correlates them with trace IDs --
// that only works cleanly if logs are structured from day one instead of
// retrofitted from plain fmt.Printf calls later.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		logger.Info("request",
			"method", c.Request.Method,
			"route", c.FullPath(),
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
