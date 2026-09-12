// Package storefactory 从配置/环境变量构建按租户策略路由的对象存储 Registry（G3-6）。
//
// 支持的后端：
//   - local：PPTS_OBJECT_ROOT（+ PPTS_OBJECT_SECRET 用于本地签名链接）
//   - s3：PPTS_S3_ENDPOINT/BUCKET/ACCESS_KEY/SECRET_KEY/REGION/USE_SSL（S3 兼容）
//
// 默认后端由 PPTS_OBJECT_BACKEND 指定（缺省 local）。租户策略 storage_backend
// 通过 resolver 决定实际后端；未注册的后端会显式失败。
package storefactory

import (
	"errors"
	"os"
	"strconv"

	"github.com/F31/ppts/internal/integrations/objectstore"
	"github.com/F31/ppts/internal/integrations/objectstore/s3"
)

// Config 描述可注册的后端集合。
type Config struct {
	Default     string
	LocalRoot   string
	LocalSecret string
	S3          *S3Config
}

// S3Config 是 S3 兼容后端配置。
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

// New 按配置构建 Registry；默认后端未注册时返回错误。
func New(cfg Config, resolver objectstore.BackendResolver) (*objectstore.Registry, error) {
	stores := map[string]objectstore.ObjectStore{}
	if cfg.LocalRoot != "" {
		stores["local"] = objectstore.NewLocal(cfg.LocalRoot, []byte(cfg.LocalSecret))
	}
	if cfg.S3 != nil {
		st, err := s3.New(s3.Config{
			Endpoint: cfg.S3.Endpoint, Bucket: cfg.S3.Bucket,
			AccessKey: cfg.S3.AccessKey, SecretKey: cfg.S3.SecretKey,
			Region: cfg.S3.Region, UseSSL: cfg.S3.UseSSL,
		})
		if err != nil {
			return nil, err
		}
		stores["s3"] = st
	}
	if len(stores) == 0 {
		return nil, errors.New("objectstore: no backend configured")
	}
	def := cfg.Default
	if def == "" {
		def = "local"
	}
	return objectstore.NewRegistry(def, stores, resolver)
}

// FromEnv 从环境变量构建 Registry。
func FromEnv(resolver objectstore.BackendResolver) (*objectstore.Registry, error) {
	cfg := Config{
		Default:     os.Getenv("PPTS_OBJECT_BACKEND"),
		LocalRoot:   os.Getenv("PPTS_OBJECT_ROOT"),
		LocalSecret: os.Getenv("PPTS_OBJECT_SECRET"),
	}
	if endpoint := os.Getenv("PPTS_S3_ENDPOINT"); endpoint != "" {
		useSSL := false
		if raw := os.Getenv("PPTS_S3_USE_SSL"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, err
			}
			useSSL = parsed
		}
		cfg.S3 = &S3Config{
			Endpoint:  endpoint,
			Bucket:    os.Getenv("PPTS_S3_BUCKET"),
			AccessKey: os.Getenv("PPTS_S3_ACCESS_KEY"),
			SecretKey: os.Getenv("PPTS_S3_SECRET_KEY"),
			Region:    os.Getenv("PPTS_S3_REGION"),
			UseSSL:    useSSL,
		}
	}
	return New(cfg, resolver)
}
