package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// APIKeyAuth protects management endpoints. Clients must send either
// `Authorization: Bearer <key>` or `X-API-Key: <key>`.
func APIKeyAuth(apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := extractKey(c)
		if provided == "" || provided != apiKey {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid or missing API key",
			})
			return
		}
		c.Next()
	}
}

// WebhookAuth verifies the shared secret sent by the Cloudflare Worker via the
// `X-Webhook-Secret` header.
func WebhookAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret == "" {
			// If no secret is configured, refuse to process webhooks.
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "webhook secret not configured",
			})
			return
		}
		if c.GetHeader("X-Webhook-Secret") != secret {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid webhook secret",
			})
			return
		}
		c.Next()
	}
}

func extractKey(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return c.GetHeader("X-API-Key")
}
