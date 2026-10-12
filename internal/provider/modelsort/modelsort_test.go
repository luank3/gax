package modelsort

import (
	"slices"
	"testing"
)

func TestCompareIDsVersionSnapshotVariantOrder(t *testing.T) {
	ordered := [][2]string{
		{"gpt-5.6", "gpt-5"},
		{"gpt-5", "gpt-5-2025-08-07"},
		{"gpt-5-2025-08-07", "gpt-5-mini"},
		{"gpt-5.6", "gpt-5.6-2026-10-20"},
		{"gpt-5-mini", "gpt-5-mini-2025-08-07"},
		{"claude-opus-4-8", "claude-opus-4-7"},
		{"claude-opus-5", "claude-opus-4-8"},
		{"claude-opus-4-5", "claude-opus-4"},
		{"kimi-k2.7-code", "kimi-k2.6"},
		{"qwen3.6-plus", "qwen3.5-plus"},
		{"minimax-m3", "minimax-m2.7"},
		{"gpt-5.10", "gpt-5.9"},
		{"babbage-002", "babbage-001"},
		{"gpt-4o-2024-11-20", "gpt-4o-2024-08-06"},
		{"grok-4.6", "grok-build-0.1"},
		{"text-embedding-3-large", "text-embedding-ada-002"},
		{"gpt-5.4", "gpt-5-2025-08-07"},
		{"tts-1", "tts-1-1106"},
		{"tts-1-1106", "tts-1-hd"},
		{"claude-opus-4-5", "claude-opus-4-5-20251101"},
		{"GLM-5", "glm-4.6"},
		{"deepseek-v4-pro", "GPT-5"},
	}
	for _, pair := range ordered {
		if got := CompareIDs(pair[0], pair[1]); got >= 0 {
			t.Errorf("CompareIDs(%q, %q) = %d, want < 0", pair[0], pair[1], got)
		}
		if got := CompareIDs(pair[1], pair[0]); got <= 0 {
			t.Errorf("CompareIDs(%q, %q) = %d, want > 0", pair[1], pair[0], got)
		}
	}
}

func TestSortIDsFixtures(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{
			name: "openai",
			got:  []string{"gpt-4", "o3-mini", "gpt-5-mini", "gpt-4.1", "gpt-5.4", "gpt-4o", "gpt-5", "gpt-4-turbo", "gpt-5.4-pro", "o4-mini", "gpt-5-2025-08-07", "gpt-4o-mini", "gpt-5-codex", "o3", "gpt-4.1-nano"},
			want: []string{"gpt-5.4", "gpt-5.4-pro", "gpt-5", "gpt-5-2025-08-07", "gpt-5-codex", "gpt-5-mini", "gpt-4.1", "gpt-4.1-nano", "gpt-4", "gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "o4-mini", "o3", "o3-mini"},
		},
		{
			name: "anthropic",
			got:  []string{"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-opus-4-6", "claude-opus-4-5-20251101", "claude-haiku-4-5-20251001", "claude-sonnet-4-5-20250929"},
			want: []string{"claude-fable-5", "claude-haiku-4-5-20251001", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5-20251101", "claude-sonnet-5", "claude-sonnet-4-6", "claude-sonnet-4-5-20250929"},
		},
		{
			name: "gateway",
			got:  []string{"qwen3.5-plus", "minimax-m2.7", "glm-5", "deepseek-v4-flash-free", "kimi-k3", "glm-5.2", "deepseek-v4-pro", "minimax-m3", "qwen3.6-plus", "kimi-k2.7-code", "deepseek-v4-flash", "glm-5.1", "hy3-preview", "hy3", "grok-build-0.1", "grok-4.6"},
			want: []string{"deepseek-v4-flash", "deepseek-v4-flash-free", "deepseek-v4-pro", "glm-5.2", "glm-5.1", "glm-5", "grok-4.6", "grok-build-0.1", "hy3", "hy3-preview", "kimi-k3", "kimi-k2.7-code", "minimax-m3", "minimax-m2.7", "qwen3.6-plus", "qwen3.5-plus"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := slices.Clone(test.got)
			Sort(got)
			if !slices.Equal(got, test.want) {
				t.Fatalf("Sort() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCompareIDsUsesRawBytesToBreakTokenTies(t *testing.T) {
	if got := CompareIDs("gpt-4.1", "gpt-4.1"); got != 0 {
		t.Fatalf("identical IDs compare as %d, want 0", got)
	}
	if got := CompareIDs("gpt-4-1", "gpt-4.1"); got >= 0 {
		t.Fatalf("punctuation tie compares as %d, want raw-byte order", got)
	}
	if got := CompareIDs("", "gpt-5"); got >= 0 {
		t.Fatalf("empty ID compares as %d, want before gpt-5", got)
	}
}
