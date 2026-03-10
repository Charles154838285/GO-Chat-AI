package ai

import (
	"kama_chat_server/internal/dao"
	"kama_chat_server/internal/model"
	"kama_chat_server/pkg/zlog"
	"time"
)

const (
	// AI_USER_UUID AI用户的固定UUID
	AI_USER_UUID      = "ai-assistant"
	AI_USER_NICKNAME  = "AI助手"
	AI_USER_PHONE     = "10000000000"
	AI_USER_AVATAR    = "https://img.icons8.com/3d-fluency/94/chatbot.png"
	AI_USER_SIGNATURE = "我是智能AI助手，输入 @ai 即可与我对话～"
)

// InitAIUser 初始化AI虚拟用户
func InitAIUser() error {
	// 检查AI用户是否已存在
	var existingUser model.UserInfo
	result := dao.GormDB.Where("uuid = ?", AI_USER_UUID).First(&existingUser)

	if result.Error == nil {
		zlog.Info("AI虚拟用户已存在，跳过初始化")
		return nil
	}

	// 创建AI用户
	aiUser := model.UserInfo{
		Uuid:      AI_USER_UUID,
		Nickname:  AI_USER_NICKNAME,
		Telephone: AI_USER_PHONE,
		Avatar:    AI_USER_AVATAR,
		Gender:    0,
		Signature: AI_USER_SIGNATURE,
		Password:  "no_pass",
		Birthday:  "20240101",
		CreatedAt: time.Now(),
		IsAdmin:   0,
		Status:    0, // 正常状态
		IsAI:      1, // 标记为AI用户
	}

	if err := dao.GormDB.Create(&aiUser).Error; err != nil {
		zlog.Error("创建AI虚拟用户失败: " + err.Error())
		return err
	}

	zlog.Info("AI虚拟用户创建成功, UUID: " + AI_USER_UUID)
	return nil
}

// GetAIUserUUID 获取AI用户UUID
func GetAIUserUUID() string {
	return AI_USER_UUID
}

// IsAIUser 判断是否是AI用户
func IsAIUser(uuid string) bool {
	return uuid == AI_USER_UUID
}

// GetAIUserInfo 获取AI用户信息
func GetAIUserInfo() (*model.UserInfo, error) {
	var aiUser model.UserInfo
	if err := dao.GormDB.Where("uuid = ?", AI_USER_UUID).First(&aiUser).Error; err != nil {
		zlog.Error("获取AI用户信息失败: " + err.Error())
		return nil, err
	}
	return &aiUser, nil
}

// EnsureAIUserExists 确保AI用户存在（在服务启动时调用）
func EnsureAIUserExists() {
	if err := InitAIUser(); err != nil {
		zlog.Error("AI用户初始化检查失败: " + err.Error())
	}
}
