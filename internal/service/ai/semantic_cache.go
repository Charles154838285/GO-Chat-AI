// Package ai — semantic_cache.go
//
// 语义缓存层：利用 Embedding 向量 + 余弦相似度拦截高频重复提问。
//
// 设计思路：
//   - 每条问题先转换为向量（调用外部 Embedding API），向量维度为 1536 维（OpenAI ada-002 / BGE）
//   - 写入时把 (向量, 答案) 存入内存VectorStore；Redis 做跨进程持久化
//   - 读取时计算与所有历史向量的余弦相似度，超过阈值（0.92）时直接返回缓存答案
//   - 命中后响应时延降至 50ms 级别（避免调用 LLM）
//   - 利用 LRU 驱逐 + TTL 避免缓存无限增长
//
// 并发安全：所有公开方法均加 RWMutex 保护。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	myredis "kama_chat_server/internal/service/redis"
	"kama_chat_server/pkg/zlog"
)

// ─────────────────────────────────────────────────────────
// 常量
// ─────────────────────────────────────────────────────────

const (
	// SemanticCacheSimilarityThreshold 余弦相似度阈值，高于此值视为语义匹配
	SemanticCacheSimilarityThreshold = 0.92

	// SemanticCacheMaxEntries 内存向量缓存最大条目数（LRU 驱逐）
	SemanticCacheMaxEntries = 500

	// SemanticCacheTTL 缓存 TTL
	SemanticCacheTTL = 2 * time.Hour

	// SemanticCacheRedisPrefix Redis key 前缀
	SemanticCacheRedisPrefix = "semantic_cache:"

	// SemanticCacheEmbeddingDim Embedding 向量维度（兼容 OpenAI ada-002 / BGE-large）
	SemanticCacheEmbeddingDim = 1536
)

// ─────────────────────────────────────────────────────────
// 数据结构
// ─────────────────────────────────────────────────────────

// CacheEntry 单条缓存记录，存储问题向量与对应答案
type CacheEntry struct {
	Question  string    `json:"question"`   // 原始问题文本
	Answer    string    `json:"answer"`     // LLM 生成的答案
	Vector    []float64 `json:"vector"`     // 问题的 Embedding 向量
	HitCount  int64     `json:"hit_count"`  // 命中次数（可做热点排序）
	CreatedAt time.Time `json:"created_at"` // 写入时间
	UpdatedAt time.Time `json:"updated_at"` // 最近命中时间
}

// CacheStats 缓存运行统计快照
type CacheStats struct {
	TotalEntries  int64   `json:"total_entries"` // 当前缓存条目数
	HitRate       float64 `json:"hit_rate"`      // 命中率（hit / (hit+miss)）
	TotalHits     int64   `json:"total_hits"`
	TotalMisses   int64   `json:"total_misses"`
	AvgSimilarity float64 `json:"avg_similarity"` // 命中时的平均相似度
}

// EmbeddingFunc 向量化函数类型，由外部注入（便于测试 mock 和多模型切换）
type EmbeddingFunc func(ctx context.Context, text string) ([]float64, error)

// ─────────────────────────────────────────────────────────
// SemanticCache
// ─────────────────────────────────────────────────────────

// SemanticCache 语义缓存，对外提供 Get / Set 两个核心接口。
type SemanticCache struct {
	mu         sync.RWMutex
	entries    []*CacheEntry // 内存向量存储
	embedFn    EmbeddingFunc // Embedding 函数（可替换）
	threshold  float64       // 相似度命中阈值
	maxEntries int           // 最大条目数
	ttl        time.Duration // 缓存 TTL
	hits       int64
	misses     int64
	totalSim   float64 // 命中时的累计相似度（用于计算均值）
	simCount   int64   // 命中次数
}

var (
	semanticCacheOnce     sync.Once
	semanticCacheInstance *SemanticCache
)

// GetSemanticCache 获取全局语义缓存单例。
// embedFn 在首次调用时注入；后续调用忽略该参数。
func GetSemanticCache(embedFn EmbeddingFunc) *SemanticCache {
	semanticCacheOnce.Do(func() {
		semanticCacheInstance = &SemanticCache{
			entries:    make([]*CacheEntry, 0, SemanticCacheMaxEntries),
			embedFn:    embedFn,
			threshold:  SemanticCacheSimilarityThreshold,
			maxEntries: SemanticCacheMaxEntries,
			ttl:        SemanticCacheTTL,
		}
		zlog.Info(fmt.Sprintf(
			"[semantic-cache] initialized: threshold=%.2f, maxEntries=%d, ttl=%s",
			SemanticCacheSimilarityThreshold, SemanticCacheMaxEntries, SemanticCacheTTL,
		))
	})
	return semanticCacheInstance
}

// ─────────────────────────────────────────────────────────
// 公开接口
// ─────────────────────────────────────────────────────────

// Get 语义查找：对 question 向量化后与缓存向量做余弦相似度比较。
// 命中时返回 (answer, true, similarity)；未命中返回 ("", false, 0)。
func (c *SemanticCache) Get(ctx context.Context, question string) (answer string, hit bool, similarity float64) {
	if c.embedFn == nil {
		return "", false, 0
	}

	// 1. 向量化当前问题
	vec, err := c.embedFn(ctx, question)
	if err != nil {
		zlog.Error("[semantic-cache] embed failed: " + err.Error())
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return "", false, 0
	}

	// 2. 遍历内存条目，寻找最高相似度
	c.mu.RLock()
	now := time.Now()
	type scored struct {
		entry *CacheEntry
		score float64
	}
	var candidates []scored
	for _, e := range c.entries {
		if now.Sub(e.CreatedAt) > c.ttl {
			continue // 跳过过期条目（懒删除）
		}
		sim := cosineSimilarity(vec, e.Vector)
		if sim >= c.threshold {
			candidates = append(candidates, scored{e, sim})
		}
	}
	c.mu.RUnlock()

	if len(candidates) == 0 {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return "", false, 0
	}

	// 取相似度最高的候选
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	best := candidates[0]

	// 3. 更新命中统计
	c.mu.Lock()
	best.entry.HitCount++
	best.entry.UpdatedAt = now
	c.hits++
	c.totalSim += best.score
	c.simCount++
	c.mu.Unlock()

	zlog.Info(fmt.Sprintf(
		"[semantic-cache] HIT: similarity=%.4f, question=%q, cached_question=%q",
		best.score, question, best.entry.Question,
	))
	return best.entry.Answer, true, best.score
}

// Set 将新问题与答案写入缓存（同时持久化到 Redis）。
func (c *SemanticCache) Set(ctx context.Context, question, answer string) error {
	if c.embedFn == nil {
		return nil
	}

	vec, err := c.embedFn(ctx, question)
	if err != nil {
		return fmt.Errorf("[semantic-cache] embed for set failed: %w", err)
	}

	entry := &CacheEntry{
		Question:  question,
		Answer:    answer,
		Vector:    vec,
		HitCount:  0,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	c.mu.Lock()
	// LRU 驱逐：超出容量时移除最旧的、命中次数最少的条目
	if len(c.entries) >= c.maxEntries {
		c.evict()
	}
	c.entries = append(c.entries, entry)
	c.mu.Unlock()

	// 异步持久化到 Redis（失败不影响主流程）
	go c.persistToRedis(ctx, entry)

	zlog.Info(fmt.Sprintf("[semantic-cache] SET: question=%q, answer_len=%d", question, len(answer)))
	return nil
}

// Stats 返回缓存运行统计快照。
func (c *SemanticCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total)
	}
	avgSim := 0.0
	if c.simCount > 0 {
		avgSim = c.totalSim / float64(c.simCount)
	}
	return CacheStats{
		TotalEntries:  int64(len(c.entries)),
		HitRate:       hitRate,
		TotalHits:     c.hits,
		TotalMisses:   c.misses,
		AvgSimilarity: avgSim,
	}
}

// InvalidateByQuestion 精确删除某条缓存（用于内容更新场景）。
func (c *SemanticCache) InvalidateByQuestion(question string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, e := range c.entries {
		if e.Question == question {
			c.entries = append(c.entries[:i], c.entries[i+1:]...)
			return
		}
	}
}

// ─────────────────────────────────────────────────────────
// 内部方法
// ─────────────────────────────────────────────────────────

// evict 驱逐最旧且命中率最低的条目（容量超限时调用，调用前需持有写锁）。
func (c *SemanticCache) evict() {
	if len(c.entries) == 0 {
		return
	}
	now := time.Now()
	// 移除所有过期条目
	valid := c.entries[:0]
	for _, e := range c.entries {
		if now.Sub(e.CreatedAt) <= c.ttl {
			valid = append(valid, e)
		}
	}
	c.entries = valid

	// 如果仍然超出，按命中次数升序排列后移除末尾（命中次数最少 = 最冷条目）
	if len(c.entries) >= c.maxEntries {
		sort.Slice(c.entries, func(i, j int) bool {
			return c.entries[i].HitCount < c.entries[j].HitCount
		})
		removeCount := len(c.entries) - c.maxEntries + 1
		c.entries = c.entries[removeCount:]
	}
}

// persistToRedis 将单条缓存条目异步写入 Redis。
func (c *SemanticCache) persistToRedis(ctx context.Context, entry *CacheEntry) {
	data, err := json.Marshal(entry)
	if err != nil {
		zlog.Error("[semantic-cache] marshal failed: " + err.Error())
		return
	}
	key := SemanticCacheRedisPrefix + fmt.Sprintf("%x", entry.CreatedAt.UnixNano())
	if err := myredis.SetKeyEx(key, string(data), SemanticCacheTTL); err != nil {
		zlog.Error("[semantic-cache] redis persist failed: " + err.Error())
	}
}

// ─────────────────────────────────────────────────────────
// 数学工具函数
// ─────────────────────────────────────────────────────────

// cosineSimilarity 计算两个向量的余弦相似度，范围 [-1, 1]。
// 向量维度不同时返回 0（容错）。
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// MockEmbeddingFunc 用于测试的 mock Embedding 函数：将字符串散列为固定维度向量。
// 生产环境替换为真实 Embedding API（如 ByteDance Ark / OpenAI）。
func MockEmbeddingFunc(_ context.Context, text string) ([]float64, error) {
	vec := make([]float64, SemanticCacheEmbeddingDim)
	for i, ch := range text {
		vec[i%SemanticCacheEmbeddingDim] += float64(ch) / 1000.0
	}
	// 归一化为单位向量
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}
