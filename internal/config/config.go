package config

import (
	"log"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

type MainConfig struct {
	AppName string `toml:"appName"`
	Host    string `toml:"host"`
	Port    int    `toml:"port"`
}

type MysqlConfig struct {
	Host         string `toml:"host"`
	Port         int    `toml:"port"`
	User         string `toml:"user"`
	Password     string `toml:"password"`
	DatabaseName string `toml:"databaseName"`
}

type RedisConfig struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Password string `toml:"password"`
	Db       int    `toml:"db"`
}

// type AuthCodeConfig struct {
// 	AccessKeyID     string `toml:"accessKeyID"`
// 	AccessKeySecret string `toml:"accessKeySecret"`
// 	SignName        string `toml:"signName"`
// 	TemplateCode    string `toml:"templateCode"`
// }

type LogConfig struct {
	LogPath string `toml:"logPath"`
}

type KafkaConfig struct {
	MessageMode string        `toml:"messageMode"`
	HostPort    string        `toml:"hostPort"`
	LoginTopic  string        `toml:"loginTopic"`
	LogoutTopic string        `toml:"logoutTopic"`
	ChatTopic   string        `toml:"chatTopic"`
	Partition   int           `toml:"partition"`
	Timeout     time.Duration `toml:"timeout"`
}

type StaticSrcConfig struct {
	StaticAvatarPath string `toml:"staticAvatarPath"`
	StaticFilePath   string `toml:"staticFilePath"`
}

type AIConfig struct {
	Enabled       bool    `toml:"enabled"`
	Provider      string  `toml:"provider"`
	ApiKey        string  `toml:"apiKey"`
	Model         string  `toml:"model"`
	BaseURL       string  `toml:"baseURL"`
	SystemPrompt  string  `toml:"systemPrompt"`
	MaxTokens     int     `toml:"maxTokens"`
	Temperature   float64 `toml:"temperature"`
	StreamEnabled bool    `toml:"streamEnabled"`
	ContextLimit  int     `toml:"contextLimit"`
}

type Config struct {
	MainConfig  `toml:"mainConfig"`
	MysqlConfig `toml:"mysqlConfig"`
	RedisConfig `toml:"redisConfig"`
	//AuthCodeConfig  `toml:"authCodeConfig"`
	LogConfig       `toml:"logConfig"`
	KafkaConfig     `toml:"kafkaConfig"`
	StaticSrcConfig `toml:"staticSrcConfig"`
	AIConfig        `toml:"aiConfig"`
}

var config *Config

func LoadConfig() error {
	// 优先环境变量 CONFIG_PATH，其次使用项目内的 configs/config.toml
	// 兼容旧路径（/root/project/KamaChat/... 和 /root/gochat/KamaChat/...）
	paths := []string{}
	if p := os.Getenv("CONFIG_PATH"); p != "" {
		paths = append(paths, p)
	}
	paths = append(paths,
		"./configs/config.toml",
		"/root/project/KamaChat/configs/config.toml",
		"/root/gochat/KamaChat/configs/config.toml",
	)

	var lastErr error
	for _, p := range paths {
		if _, err := toml.DecodeFile(p, config); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if lastErr != nil {
		log.Fatal(lastErr.Error())
	}
	return lastErr
}

func GetConfig() *Config {
	if config == nil {
		config = new(Config)
		_ = LoadConfig()
	}
	return config
}
