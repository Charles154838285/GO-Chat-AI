package v1

import (
	"kama_chat_server/internal/service/chat"
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetMetrics 获取 WebSocket 连接和消息的实时监控指标
func GetMetrics(c *gin.Context) {
	snapshot := chat.GetMetricsSnapshot()
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "ok",
		"data": gin.H{
			"active_connections": snapshot.ActiveConnections,
			"total_connections":  snapshot.TotalConnections,
			//"total_disconnections": snapshot.TotalDisconnections,
			"messages_received":  snapshot.MessagesReceived,
			"messages_sent":      snapshot.MessagesSent,
			"ai_requests_total":  snapshot.AIRequestsTotal,
			"ai_requests_failed": snapshot.AIRequestsFailed,
		},
	})
}
