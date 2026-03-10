// Package sms 实现短信验证码发送与验证的完整业务流程。
//
// 流程设计：
//  1. 用户调用 /user/sendSmsCode 接口，传入手机号
//  2. 后端生成 6 位随机验证码，以 sms_code:{telephone} 为 key 存入 Redis（TTL 5 分钟）
//  3. 同时限流：同一手机号 60 秒内只允许发送一次（key: sms_limit:{telephone}）
//  4. 用户用验证码调用 /user/smsLogin，后端比对 Redis 中的验证码
//  5. 验证通过后删除 Redis 验证码（防复用），完成登录/注册
//
// 注意：
//   - 当前 SendSmsCode 使用 mock 实现（直接记录日志），无需真实短信账号即可开发调试
//   - 若需接入真实短信平台（阿里云 SMS / 腾讯云 SMS），只需替换 sendViaSMSGateway 函数
package sms

import (
	"errors"
	"fmt"
	"math/rand"
	"time"

	myredis "kama_chat_server/internal/service/redis"
	"kama_chat_server/pkg/zlog"
)

// 常量定义：Redis Key 前缀 & TTL
const (
	codePrefix  = "sms_code:"      // Redis key 前缀：存储验证码
	limitPrefix = "sms_limit:"     // Redis key 前缀：发送频率限制
	codeTTL     = 5 * time.Minute  // 验证码有效期（5分钟）
	limitTTL    = 60 * time.Second // 发送频率限制（60秒）
)

// smsService 短信验证码服务实现
type smsService struct{}

// SmsService 短信验证码服务单例（全局使用）
var SmsService = new(smsService)

// sendViaSMSGateway 真实短信发送实现
// 当前为 mock：仅打印日志，方便本地开发。
// 生产环境将此函数替换为阿里云 / 腾讯云 SMS SDK 调用即可，其他代码无需修改。
func sendViaSMSGateway(telephone, code string) error {
	// ----- 阿里云 SMS 示例（取消注释替换 mock）-----
	// client, err := dysmsapi.NewClientWithAccessKey("cn-hangzhou", accessKeyID, accessKeySecret)
	// if err != nil { return err }
	// request := dysmsapi.CreateSendSmsRequest()
	// request.PhoneNumbers = telephone
	// request.SignName = "KamaChat"
	// request.TemplateCode = "SMS_XXXXXXX"
	// request.TemplateParam = `{"code":"` + code + `"}`
	// _, err = client.SendSms(request)
	// return err

	// ----- Mock 实现 -----
	zlog.Info(fmt.Sprintf("[SMS MOCK] To: %s | Code: %s | Expires: 5min", telephone, code))
	return nil
}

// SendCode 生成验证码并发送短信
// 返回：
//   - 提示信息 / 0  → 成功
//   - 错误信息 / -1 → 频率限制或发送失败
func (s *smsService) SendCode(telephone string) (string, int) {
	// 1. 频率限制：60 秒内只允许发送一次
	limitKey := limitPrefix + telephone
	limitVal, err := myredis.GetKey(limitKey)
	if err != nil {
		zlog.Error("redis get limit key error: " + err.Error())
		return "系统错误，请稍后重试", -1
	}
	if limitVal != "" {
		return "操作过于频繁，请60秒后重试", -1
	}

	// 2. 生成 6 位数字验证码
	code := fmt.Sprintf("%06d", rand.New(rand.NewSource(time.Now().UnixNano())).Intn(1000000))

	// 3. 存入 Redis（TTL 5 分钟）
	codeKey := codePrefix + telephone
	if err := myredis.SetKeyEx(codeKey, code, codeTTL); err != nil {
		zlog.Error("redis set code error: " + err.Error())
		return "系统错误，请稍后重试", -1
	}

	// 4. 写入频率限制 key（TTL 60 秒）
	if err := myredis.SetKeyEx(limitKey, "1", limitTTL); err != nil {
		zlog.Error("redis set limit key error: " + err.Error())
		return "系统错误，请稍后重试", -1
	}

	// 5. 发送短信（mock 实现：打印日志；生产环境替换为真实短信网关调用）
	if err := sendViaSMSGateway(telephone, code); err != nil {
		zlog.Error("send sms failed: " + err.Error())
		return "短信发送失败，请稍后重试", -1
	}

	zlog.Info(fmt.Sprintf("SMS code sent to %s (mock)", telephone))
	return "验证码发送成功", 0
}

// VerifyCode 校验用户输入的验证码
// 返回 true 表示验证通过，验证后立即删除 Redis 中的验证码（防重放）。
func (s *smsService) VerifyCode(telephone, inputCode string) (bool, error) {
	// 拼接 Redis Key
	codeKey := codePrefix + telephone

	// 从 Redis 获取存储的验证码
	storedCode, err := myredis.GetKey(codeKey)
	if err != nil {
		return false, fmt.Errorf("redis get error: %w", err) // 包装错误，保留原始错误信息
	}

	// 验证码不存在/已过期
	if storedCode == "" {
		return false, errors.New("验证码已过期或不存在")
	}

	// 验证码不匹配
	if storedCode != inputCode {
		return false, errors.New("验证码错误")
	}

	// 验证通过，删除 key 防止二次使用
	if err := myredis.DeleteKey(codeKey); err != nil {
		zlog.Warn(fmt.Sprintf("delete sms code key failed: %s, err: %v", codeKey, err))
		// 此处不返回错误，仅打印警告（验证码已验证通过，删除失败不影响核心流程）
	}

	return true, nil
}
