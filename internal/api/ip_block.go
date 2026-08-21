package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const blockedClientIP = "107.173.42.94"

func blockedClientIPMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if requestHasIP(c.Request, blockedClientIP) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func requestHasIP(request *http.Request, blocked string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(request.RemoteAddr)
	}
	if host == blocked {
		return true
	}
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"} {
		for _, value := range request.Header.Values(header) {
			for _, candidate := range strings.Split(value, ",") {
				if strings.TrimSpace(candidate) == blocked {
					return true
				}
			}
		}
	}
	return false
}
