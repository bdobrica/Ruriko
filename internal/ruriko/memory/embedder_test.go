package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNoopEmbedder_SatisfiesInterface(t *testing.T) {
	var e Embedder = NoopEmbedder{}
	if e == nil {
		t.Fatal("expected non-nil Embedder")
	}
}

func TestNoopEmbedder_ReturnsNil(t *testing.T) {
	e := NoopEmbedder{}
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed() returned unexpected error: %v", err)
	}
	if vec != nil {
		t.Errorf("expected nil vector, got %v", vec)
	}
}

func TestNoopSummariser_SatisfiesInterface(t *testing.T) {
	var s Summariser = NoopSummariser{}
	if s == nil {
		t.Fatal("expected non-nil Summariser")
	}
}

func TestNoopSummariser_EmptyMessages(t *testing.T) {
	s := NoopSummariser{}
	summary, err := s.Summarise(context.Background(), nil)
	if err != nil {
		t.Fatalf("Summarise() returned unexpected error: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty summary, got %q", summary)
	}
}

func TestNoopSummariser_SingleMessage(t *testing.T) {
	s := NoopSummariser{}
	msgs := []Message{
		{Role: "user", Content: "set up saito", Timestamp: time.Now()},
	}
	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() returned unexpected error: %v", err)
	}
	if summary != "user: set up saito" {
		t.Errorf("expected 'user: set up saito', got %q", summary)
	}
}

func TestNoopSummariser_TwoMessages(t *testing.T) {
	s := NoopSummariser{}
	msgs := []Message{
		{Role: "user", Content: "set up saito", Timestamp: time.Now()},
		{Role: "assistant", Content: "done", Timestamp: time.Now()},
	}
	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() returned unexpected error: %v", err)
	}
	lines := strings.Split(summary, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), summary)
	}
	if lines[0] != "user: set up saito" {
		t.Errorf("line 0: expected 'user: set up saito', got %q", lines[0])
	}
	if lines[1] != "assistant: done" {
		t.Errorf("line 1: expected 'assistant: done', got %q", lines[1])
	}
}

func TestNoopSummariser_TakesLastThreeMessages(t *testing.T) {
	s := NoopSummariser{}
	msgs := []Message{
		{Role: "user", Content: "first", Timestamp: time.Now()},
		{Role: "assistant", Content: "second", Timestamp: time.Now()},
		{Role: "user", Content: "third", Timestamp: time.Now()},
		{Role: "assistant", Content: "fourth", Timestamp: time.Now()},
		{Role: "user", Content: "fifth", Timestamp: time.Now()},
	}
	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() returned unexpected error: %v", err)
	}
	lines := strings.Split(summary, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (last 3 messages), got %d: %q", len(lines), summary)
	}
	if lines[0] != "user: third" {
		t.Errorf("line 0: expected 'user: third', got %q", lines[0])
	}
	if lines[1] != "assistant: fourth" {
		t.Errorf("line 1: expected 'assistant: fourth', got %q", lines[1])
	}
	if lines[2] != "user: fifth" {
		t.Errorf("line 2: expected 'user: fifth', got %q", lines[2])
	}
}

func TestNoopSummariser_ExactlyThreeMessages(t *testing.T) {
	s := NoopSummariser{}
	msgs := []Message{
		{Role: "user", Content: "one", Timestamp: time.Now()},
		{Role: "assistant", Content: "two", Timestamp: time.Now()},
		{Role: "user", Content: "three", Timestamp: time.Now()},
	}
	summary, err := s.Summarise(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Summarise() returned unexpected error: %v", err)
	}
	lines := strings.Split(summary, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), summary)
	}
	if lines[0] != "user: one" {
		t.Errorf("line 0: expected 'user: one', got %q", lines[0])
	}
}
