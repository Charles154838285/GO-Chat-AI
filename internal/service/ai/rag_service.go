// Package ai — rag_service.go
//
// RAG（Retrieval-Augmented Generation）检索增强生成服务。
//
// 架构：
//
//	用户问题
//	   │
//	   ├─ Embedding 向量化（BGE-large-zh）
//	   │
//	   ├─ 稠密向量检索（Elasticsearch KNN）
//	   ├─ 稀疏关键词检索（Elasticsearch BM25）
//	   │
//	   └─ 混合重排（RRF: Reciprocal Rank Fusion）
//	          └─ Top-K 文档片段拼接到 Prompt
//
// 特性：
//   - BGE + Elasticsearch 混合检索显著降低 AI 幻觉（兼顾语义相似性与关键词精度）
//   - RRF 重排算法融合两路检索分数，无需调权重超参
//   - 文档分块：最大 512 Token / 块，块间 50 Token 重叠（滑动窗口）
//   - 支持知识库热更新：Add/Delete Document 不停服
//   - 降级策略：ES 不可用时退化为本地向量内存检索
//
// 生产部署时替换以下 Mock 为真实实现：
//   - ElasticsearchClient.Search → 真实 ES 8.x KNN API
//   - EmbedText → 真实 BGE Embedding 服务（如 Hugging Face Inference）
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"kama_chat_server/pkg/zlog"
)

// ─────────────────────────────────────────────────────────
// 文档与检索数据结构
// ─────────────────────────────────────────────────────────

// Document 知识库中的单个文档片段（知识块）
type Document struct {
	ID        string    `json:"id"`       // 唯一 ID（UUID 或哈希）
	Title     string    `json:"title"`    // 文档标题（可选）
	Content   string    `json:"content"`  // 文本内容
	Source    string    `json:"source"`   // 来源（文件名、URL 等）
	Category  string    `json:"category"` // 类别标签（用于过滤）
	Vector    []float64 `json:"vector"`   // BGE Embedding 向量
	Keywords  []string  `json:"keywords"` // 关键词列表（BM25 索引）
	CreatedAt time.Time `json:"created_at"`
}

// SearchResult 检索结果单条记录
type SearchResult struct {
	Document   Document `json:"document"`
	Score      float64  `json:"score"`       // 融合后的 RRF 分数
	DenseRank  int      `json:"dense_rank"`  // 稠密检索排名
	SparseRank int      `json:"sparse_rank"` // 稀疏检索排名
}

// ─────────────────────────────────────────────────────────
// RAG 服务
// ─────────────────────────────────────────────────────────

// RAGService 混合检索增强生成服务
type RAGService struct {
	mu           sync.RWMutex
	docs         []*Document // 内存文档库（降级 / 测试用）
	embedFn      EmbeddingFunc
	esClient     *mockESClient // 生产替换为真实 ES 客户端接口
	chunkSize    int           // 文档分块大小（字符数）
	chunkOverlap int           // 分块重叠字符数
}

var (
	ragServiceOnce     sync.Once
	ragServiceInstance *RAGService
)

// GetRAGService 返回全局 RAG 服务单例。
func GetRAGService() *RAGService {
	ragServiceOnce.Do(func() {
		ragServiceInstance = &RAGService{
			docs:         make([]*Document, 0),
			chunkSize:    512,
			chunkOverlap: 50,
		}
		// 初始化嵌入函数（与语义缓存共用 Mock 实现，生产替换为 BGE API）
		ragServiceInstance.embedFn = MockEmbeddingFunc
		ragServiceInstance.esClient = newMockESClient()
		zlog.Info("[rag-service] initialized with mock ES client")
	})
	return ragServiceInstance
}

// ─────────────────────────────────────────────────────────
// 核心检索接口
// ─────────────────────────────────────────────────────────

// Search 混合检索：稠密检索（向量）+ 稀疏检索（BM25）→ RRF 重排 → Top-K 结果。
func (s *RAGService) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	if topK <= 0 {
		topK = 3
	}

	// 1. 稠密检索（向量相似度）
	denseResults, err := s.denseSearch(ctx, query, topK*2)
	if err != nil {
		zlog.Error("[rag] dense search failed: " + err.Error())
		denseResults = []SearchResult{}
	}

	// 2. 稀疏检索（BM25 关键词）
	sparseResults, err := s.sparseSearch(ctx, query, topK*2)
	if err != nil {
		zlog.Error("[rag] sparse search failed: " + err.Error())
		sparseResults = []SearchResult{}
	}

	// 3. RRF 融合重排
	merged := rrfFusion(denseResults, sparseResults, topK)

	zlog.Info(fmt.Sprintf(
		"[rag] search done: query=%q, dense=%d, sparse=%d, merged=%d",
		query, len(denseResults), len(sparseResults), len(merged),
	))
	return merged, nil
}

// BuildRAGPrompt 将检索结果格式化为 Prompt 片段，注入到 LLM 请求中。
//
// 格式示例：
//
//	【参考资料】
//	[1] 文档标题（来源：xxx）
//	内容片段...
//	[2] ...
//	---
//	请基于以上参考资料回答用户问题，若资料不足请明确说明。
func (s *RAGService) BuildRAGPrompt(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("【参考资料】\n")
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("[%d] %s（来源：%s）\n", i+1, r.Document.Title, r.Document.Source))
		sb.WriteString(r.Document.Content)
		sb.WriteString("\n\n")
	}
	sb.WriteString("---\n请基于以上参考资料回答用户问题，若资料不足请明确说明。\n")
	return sb.String()
}

// ─────────────────────────────────────────────────────────
// 文档管理
// ─────────────────────────────────────────────────────────

// AddDocument 向知识库添加文档（自动分块 + Embedding）。
func (s *RAGService) AddDocument(ctx context.Context, title, content, source, category string) error {
	chunks := s.splitIntoChunks(content)
	for i, chunk := range chunks {
		vec, err := s.embedFn(ctx, chunk)
		if err != nil {
			zlog.Error(fmt.Sprintf("[rag] embed doc chunk failed: %v", err))
			continue
		}

		doc := &Document{
			ID:        fmt.Sprintf("%s_chunk_%d_%d", source, i, time.Now().UnixNano()),
			Title:     title,
			Content:   chunk,
			Source:    source,
			Category:  category,
			Vector:    vec,
			Keywords:  extractKeywords(chunk),
			CreatedAt: time.Now(),
		}

		s.mu.Lock()
		s.docs = append(s.docs, doc)
		s.mu.Unlock()

		// 同步到 ES
		if err := s.esClient.IndexDocument(ctx, doc); err != nil {
			zlog.Error("[rag] ES index failed: " + err.Error())
		}
	}

	zlog.Info(fmt.Sprintf("[rag] added document: title=%s, chunks=%d", title, len(chunks)))
	return nil
}

// DeleteDocument 从知识库删除文档（按 source 匹配）。
func (s *RAGService) DeleteDocument(ctx context.Context, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.docs[:0]
	for _, d := range s.docs {
		if d.Source != source {
			filtered = append(filtered, d)
		}
	}
	s.docs = filtered
	zlog.Info(fmt.Sprintf("[rag] deleted documents: source=%s", source))
}

// GetDocumentCount 返回当前知识库文档片段总数。
func (s *RAGService) GetDocumentCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.docs)
}

// ─────────────────────────────────────────────────────────
// 内部检索方法
// ─────────────────────────────────────────────────────────

// denseSearch 稠密向量检索（余弦相似度）
func (s *RAGService) denseSearch(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	queryVec, err := s.embedFn(ctx, query)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		doc   *Document
		score float64
	}
	var candidates []scored
	for _, doc := range s.docs {
		sim := cosineSimilarity(queryVec, doc.Vector)
		candidates = append(candidates, scored{doc, sim})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]SearchResult, 0, topK)
	for i, c := range candidates {
		if i >= topK {
			break
		}
		results = append(results, SearchResult{
			Document:  *c.doc,
			Score:     c.score,
			DenseRank: i + 1,
		})
	}
	return results, nil
}

// sparseSearch BM25 关键词检索（简化实现：TF-IDF 风格打分）
func (s *RAGService) sparseSearch(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	queryTerms := strings.Fields(strings.ToLower(query))
	if len(queryTerms) == 0 {
		return []SearchResult{}, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		doc   *Document
		score float64
	}
	var candidates []scored
	totalDocs := float64(len(s.docs))

	for _, doc := range s.docs {
		contentLower := strings.ToLower(doc.Content)
		score := 0.0
		for _, term := range queryTerms {
			tf := float64(strings.Count(contentLower, term))
			if tf > 0 {
				// 简化 BM25：用 TF * IDF 近似
				docsWithTerm := 1.0
				for _, d := range s.docs {
					if strings.Contains(strings.ToLower(d.Content), term) {
						docsWithTerm++
					}
				}
				idf := math.Log((totalDocs + 1) / docsWithTerm)
				score += tf * idf
			}
		}
		if score > 0 {
			candidates = append(candidates, scored{doc, score})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	results := make([]SearchResult, 0, topK)
	for i, c := range candidates {
		if i >= topK {
			break
		}
		results = append(results, SearchResult{
			Document:   *c.doc,
			Score:      c.score,
			SparseRank: i + 1,
		})
	}
	return results, nil
}

// ─────────────────────────────────────────────────────────
// RRF 重排算法
// ─────────────────────────────────────────────────────────

// rrfFusion 使用 Reciprocal Rank Fusion 融合稠密和稀疏检索结果。
// RRF 公式：score(d) = Σ 1/(k + rank(d))，k=60（经典超参数）
func rrfFusion(denseResults, sparseResults []SearchResult, topK int) []SearchResult {
	const k = 60.0
	scores := make(map[string]float64)      // docID → RRF score
	docMap := make(map[string]SearchResult) // docID → doc

	for i, r := range denseResults {
		id := r.Document.ID
		scores[id] += 1.0 / (k + float64(i+1))
		docMap[id] = r
	}
	for i, r := range sparseResults {
		id := r.Document.ID
		scores[id] += 1.0 / (k + float64(i+1))
		if _, exists := docMap[id]; !exists {
			docMap[id] = r
		}
	}

	// 排序
	type idScore struct {
		id    string
		score float64
	}
	var ranked []idScore
	for id, score := range scores {
		ranked = append(ranked, idScore{id, score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	results := make([]SearchResult, 0, topK)
	for i, item := range ranked {
		if i >= topK {
			break
		}
		r := docMap[item.id]
		r.Score = item.score
		results = append(results, r)
	}
	return results
}

// ─────────────────────────────────────────────────────────
// 文档处理工具
// ─────────────────────────────────────────────────────────

// splitIntoChunks 将长文本按滑动窗口分块（chunkSize 字符 / 块，chunkOverlap 重叠）。
func (s *RAGService) splitIntoChunks(text string) []string {
	runes := []rune(text)
	if len(runes) <= s.chunkSize {
		return []string{text}
	}

	var chunks []string
	step := s.chunkSize - s.chunkOverlap
	if step <= 0 {
		step = s.chunkSize
	}

	for start := 0; start < len(runes); start += step {
		end := start + s.chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}

// extractKeywords 从文本中提取关键词（简单分词，生产替换为 jieba 等分词器）。
func extractKeywords(text string) []string {
	// 简单实现：按中文标点拆分，取长度 > 1 的词
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '，' || r == '。' || r == '！' || r == '？' ||
			r == '、' || r == ' ' || r == '\n' || r == '\t'
	})
	var keywords []string
	seen := make(map[string]bool)
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if len([]rune(f)) > 1 && !seen[f] {
			keywords = append(keywords, f)
			seen[f] = true
		}
	}
	return keywords
}

// ─────────────────────────────────────────────────────────
// Mock Elasticsearch 客户端（生产替换为 elastic/go-elasticsearch）
// ─────────────────────────────────────────────────────────

type mockESClient struct {
	mu   sync.RWMutex
	docs map[string]*Document
}

func newMockESClient() *mockESClient {
	return &mockESClient{docs: make(map[string]*Document)}
}

func (e *mockESClient) IndexDocument(_ context.Context, doc *Document) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.docs[doc.ID] = doc
	return nil
}

func (e *mockESClient) Search(_ context.Context, query string, topK int) ([]*Document, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var results []*Document
	for _, doc := range e.docs {
		if strings.Contains(doc.Content, query) {
			results = append(results, doc)
		}
	}
	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

// ToJSON 将检索结果序列化为 JSON（供工具调用返回）。
func (r SearchResult) ToJSON() string {
	data, _ := json.Marshal(r)
	return string(data)
}
