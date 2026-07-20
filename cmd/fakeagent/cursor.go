package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func runCursor(args []string, scenario *Scenario) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(os.Stdout, "2026.07.17-3e2a980")
		return 0
	}
	prompt, err := io.ReadAll(io.LimitReader(os.Stdin, (16<<20)+1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: read cursor stdin: %v\n", err)
		return 1
	}
	if len(prompt) > 16<<20 {
		fmt.Fprintln(os.Stderr, "fakeagent: cursor prompt exceeds 16 MiB")
		return 1
	}
	logInvocation("cursor", string(prompt), args)
	action := scenario.Match(string(prompt))
	if err := applyAction(action); err != nil {
		return 1
	}
	body := action.textOrDefault()
	if action.hasStructuredOutput() {
		body = string(action.structuredJSON())
	}
	modelID := argAfter(args, "--model")
	model := map[string]string{
		"cursor-grok-4.5-low":    "Cursor Grok 4.5 Low",
		"cursor-grok-4.5-medium": "Cursor Grok 4.5 Medium",
		"cursor-grok-4.5-high":   "Cursor Grok 4.5 High",
	}[modelID]
	if model == "" {
		model = "Auto"
	}
	const session = "00000000-0000-4000-8000-000000000001"
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": model, "permissionMode": "default"})
	_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": body}}}, "session_id": session, "timestamp_ms": 1})
	_ = enc.Encode(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": body}}}, "session_id": session})
	_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": body, "session_id": session, "usage": map[string]int{"inputTokens": 100, "outputTokens": 50, "cacheReadTokens": 0, "cacheWriteTokens": 0}})
	return 0
}
