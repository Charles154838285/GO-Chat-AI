package ai

import (
	"context"
	"testing"
)

func TestRAGSplitIntoChunks(t *testing.T) {
	s := GetRAGService()
	text := "这是一个很长的文本。"
	for i := 0; i < 200; i++ {
		text += "用于测试分块。"
	}
	chunks := s.splitIntoChunks(text)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
}

func TestRRFFusion(t *testing.T) {
	d1 := SearchResult{Document: Document{ID: "A"}}
	d2 := SearchResult{Document: Document{ID: "B"}}
	s1 := SearchResult{Document: Document{ID: "B"}}
	s2 := SearchResult{Document: Document{ID: "C"}}
	out := rrfFusion([]SearchResult{d1, d2}, []SearchResult{s1, s2}, 3)
	if len(out) == 0 {
		t.Fatal("expected merged results")
	}
}

func TestRAGSearchBasic(t *testing.T) {
	s := GetRAGService()
	_ = s.AddDocument(context.Background(), "Kafka Guide", "kafka message queue and partition", "doc://kafka", "im")
	results, err := s.Search(context.Background(), "kafka", 3)
	if err != nil {
		t.Fatalf("search err: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
}
