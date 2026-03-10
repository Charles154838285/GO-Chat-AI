package v1

import (
	"net/http"

	"kama_chat_server/internal/service/ai"
	"kama_chat_server/pkg/constants"
	"kama_chat_server/pkg/zlog"

	"github.com/gin-gonic/gin"
)

// GetAIStatus 获取AI服务状态
func GetAIStatus(c *gin.Context) {
	aiService := ai.GetAIService()

	status := gin.H{
		"enabled": aiService.IsEnabled(),
		"ai_uuid": ai.GetAIUserUUID(),
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    status,
	})
}

// GetAIUserInfo 获取AI用户信息
func GetAIUserInfo(c *gin.Context) {
	aiUser, err := ai.GetAIUserInfo()
	if err != nil {
		zlog.Error("获取AI用户信息失败: " + err.Error())
		c.JSON(http.StatusOK, gin.H{
			"code":    500,
			"message": constants.SYSTEM_ERROR,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"uuid":      aiUser.Uuid,
			"nickname":  aiUser.Nickname,
			"avatar":    aiUser.Avatar,
			"signature": aiUser.Signature,
			"is_ai":     aiUser.IsAI,
		},
	})
}

// ClearAIContext 清除AI上下文
func ClearAIContext(c *gin.Context) {
	userID := c.Query("user_id")
	if userID == "" {
		c.JSON(http.StatusOK, gin.H{
			"code":    400,
			"message": "缺少user_id参数",
		})
		return
	}

	aiService := ai.GetAIService()
	aiService.ClearContext(userID)

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "AI上下文已清除",
	})
}

// GetAIContextInfo 获取AI上下文信息（调试用）
func GetAIContextInfo(c *gin.Context) {
	userID := c.Query("user_id")
	if userID == "" {
		c.JSON(http.StatusOK, gin.H{
			"code":    400,
			"message": "缺少user_id参数",
		})
		return
	}

	aiService := ai.GetAIService()
	rounds, updatedAt := aiService.GetContextInfo(userID)

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"user_id":        userID,
			"context_rounds": rounds,
			"updated_at":     updatedAt.Format("2006-01-02 15:04:05"),
		},
	})
}
