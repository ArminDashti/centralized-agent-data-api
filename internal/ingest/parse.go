package ingest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type TokenSupplement struct {
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
	Totals           *struct {
		InputTokens      *int64 `json:"input_tokens"`
		OutputTokens     *int64 `json:"output_tokens"`
		CacheReadTokens  *int64 `json:"cache_read_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"totals"`
}

type SessionRow struct {
	UUID                 string
	Name                 string
	Subtitle             string
	Status               string
	UnifiedMode          string
	WorkspaceID          string
	WorkspacePath        string
	ContextUsagePercent  *float64
	InputTokens          *int64
	OutputTokens         *int64
	CacheReadTokens      *int64
	CacheWriteTokens     *int64
	ModelConfigJSON      []byte
	TotalLinesAdded      int
	TotalLinesRemoved    int
	FilesChangedCount    int
	CreatedAtMs          *int64
	LastUpdatedAtMs      *int64
	ExtractedAt          *time.Time
	RawJSON              []byte
}

type TurnRow struct {
	BubbleID            string
	Ordinal             int
	TurnType            string
	Text                string
	ToolName            string
	MCPName             string
	Status              string
	DurationMs          *int64
	HasThinking         bool
	ThinkingDurationMs  *int64
	ThinkingText        string
	InputTokens         *int64
	OutputTokens        *int64
	CacheReadTokens     *int64
	CacheWriteTokens    *int64
	BubbleJSON          []byte
}

type Parsed struct {
	Session SessionRow
	Turns   []TurnRow
}

func ParseExtract(raw []byte, tokens *TokenSupplement) (*Parsed, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	uuid := asString(root["uuid"])
	if uuid == "" {
		return nil, fmt.Errorf("uuid is required")
	}

	summary, _ := root["summary"].(map[string]any)
	header, _ := root["header"].(map[string]any)
	headerValue, _ := mapAny(header, "value")
	composer, _ := root["composerData"].(map[string]any)

	modelCfg := firstMap(summary, "modelConfig")
	if modelCfg == nil {
		modelCfg = firstMap(composer, "modelConfig")
	}
	modelJSON, _ := json.Marshal(modelCfg)
	if modelCfg == nil {
		modelJSON = []byte("{}")
	}

	wsID := firstString(header, "workspaceId")
	wsPath := ""
	if headerValue != nil {
		if wi, ok := headerValue["workspaceIdentifier"].(map[string]any); ok {
			if uri, ok := wi["uri"].(map[string]any); ok {
				wsPath = asString(uri["fsPath"])
			}
		}
	}

	ctxPct := firstFloat(headerValue, "contextUsagePercent")
	if ctxPct == nil {
		ctxPct = firstFloat(composer, "contextUsagePercent")
	}

	createdAt := firstInt64(summary, "createdAt")
	if createdAt == nil {
		createdAt = firstInt64(composer, "createdAt")
	}
	if createdAt == nil {
		createdAt = firstInt64(header, "createdAt")
	}
	updatedAt := firstInt64(summary, "lastUpdatedAt")
	if updatedAt == nil {
		updatedAt = firstInt64(composer, "lastUpdatedAt")
	}
	if updatedAt == nil {
		updatedAt = firstInt64(header, "lastUpdatedAt")
	}

	var extractedAt *time.Time
	if s := asString(root["extractedAt"]); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			extractedAt = &t
		} else if t, err := time.Parse(time.RFC3339, s); err == nil {
			extractedAt = &t
		}
	}

	sess := SessionRow{
		UUID:                uuid,
		Name:                firstNonEmpty(asString(summary["name"]), asString(composer["name"])),
		Subtitle:            firstNonEmpty(asString(summary["subtitle"]), asString(composer["subtitle"])),
		Status:              firstNonEmpty(asString(summary["status"]), asString(composer["status"])),
		UnifiedMode:         firstNonEmpty(asString(summary["unifiedMode"]), asString(composer["unifiedMode"])),
		WorkspaceID:         wsID,
		WorkspacePath:       wsPath,
		ContextUsagePercent: ctxPct,
		ModelConfigJSON:     modelJSON,
		TotalLinesAdded:     int(asInt64(headerValue["totalLinesAdded"])),
		TotalLinesRemoved:   int(asInt64(headerValue["totalLinesRemoved"])),
		FilesChangedCount:   int(asInt64(headerValue["filesChangedCount"])),
		CreatedAtMs:         createdAt,
		LastUpdatedAtMs:     updatedAt,
		ExtractedAt:         extractedAt,
		RawJSON:             raw,
	}

	applyTokenSupplement(&sess, tokens)
	applyTokenFromMap(&sess, composer)
	applyTokenFromMap(&sess, summary)

	conv, _ := root["conversation"].([]any)
	turns := make([]TurnRow, 0, len(conv))
	for i, item := range conv {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		bubble, _ := m["bubble"].(map[string]any)
		if bubble == nil {
			bubble = map[string]any{}
		}
		grouping, _ := bubble["grouping"].(map[string]any)
		bubbleJSON, _ := json.Marshal(bubble)

		turnType := classifyTurn(bubble, grouping)
		text := extractText(bubble)
		toolName := firstNonEmpty(
			asString(grouping["toolCallCase"]),
			asString(grouping["mcpToolName"]),
			asString(bubble["tool"]),
		)
		mcpName := firstNonEmpty(asString(grouping["mcpName"]), asString(grouping["mcpProviderIdentifier"]))
		status := firstNonEmpty(asString(grouping["toolFormerStatus"]), asString(bubble["status"]))

		var duration *int64
		if start := asInt64Ptr(bubble["startedAtMs"]); start != nil {
			if end := asInt64Ptr(bubble["completedAtMs"]); end != nil && *end >= *start {
				d := *end - *start
				duration = &d
			}
		}

		hasThinking := asBool(grouping["hasThinking"])
		thinkingDur := asInt64Ptr(grouping["thinkingDurationMs"])
		thinkingText := firstNonEmpty(
			asString(bubble["thinking"]),
			asString(bubble["thinkingText"]),
			asString(bubble["reasoning"]),
			nestedString(bubble, "thinking", "text"),
		)
		if thinkingText != "" {
			hasThinking = true
		}

		inTok := asInt64Ptr(bubble["inputTokens"])
		if inTok == nil {
			inTok = asInt64Ptr(bubble["input_tokens"])
		}
		outTok := asInt64Ptr(bubble["outputTokens"])
		if outTok == nil {
			outTok = asInt64Ptr(bubble["output_tokens"])
		}
		cacheRead := asInt64Ptr(bubble["cacheReadTokens"])
		if cacheRead == nil {
			cacheRead = asInt64Ptr(bubble["cache_read_tokens"])
		}
		cacheWrite := asInt64Ptr(bubble["cacheWriteTokens"])
		if cacheWrite == nil {
			cacheWrite = asInt64Ptr(bubble["cache_write_tokens"])
		}

		turns = append(turns, TurnRow{
			BubbleID:           firstNonEmpty(asString(m["bubbleId"]), asString(bubble["bubbleId"]), fmt.Sprintf("ord-%d", i)),
			Ordinal:            i,
			TurnType:           turnType,
			Text:               text,
			ToolName:           toolName,
			MCPName:            mcpName,
			Status:             status,
			DurationMs:         duration,
			HasThinking:        hasThinking,
			ThinkingDurationMs: thinkingDur,
			ThinkingText:       thinkingText,
			InputTokens:        inTok,
			OutputTokens:       outTok,
			CacheReadTokens:    cacheRead,
			CacheWriteTokens:   cacheWrite,
			BubbleJSON:         bubbleJSON,
		})
	}

	if sess.InputTokens == nil && sess.OutputTokens == nil {
		var in, out, cr, cw int64
		var has bool
		for _, t := range turns {
			if t.InputTokens != nil {
				in += *t.InputTokens
				has = true
			}
			if t.OutputTokens != nil {
				out += *t.OutputTokens
				has = true
			}
			if t.CacheReadTokens != nil {
				cr += *t.CacheReadTokens
				has = true
			}
			if t.CacheWriteTokens != nil {
				cw += *t.CacheWriteTokens
				has = true
			}
		}
		if has {
			sess.InputTokens = &in
			sess.OutputTokens = &out
			sess.CacheReadTokens = &cr
			sess.CacheWriteTokens = &cw
		}
	}

	return &Parsed{Session: sess, Turns: turns}, nil
}

func applyTokenSupplement(sess *SessionRow, tokens *TokenSupplement) {
	if tokens == nil {
		return
	}
	if tokens.Totals != nil {
		if tokens.Totals.InputTokens != nil {
			sess.InputTokens = tokens.Totals.InputTokens
		}
		if tokens.Totals.OutputTokens != nil {
			sess.OutputTokens = tokens.Totals.OutputTokens
		}
		if tokens.Totals.CacheReadTokens != nil {
			sess.CacheReadTokens = tokens.Totals.CacheReadTokens
		}
		if tokens.Totals.CacheWriteTokens != nil {
			sess.CacheWriteTokens = tokens.Totals.CacheWriteTokens
		}
	}
	if tokens.InputTokens != nil {
		sess.InputTokens = tokens.InputTokens
	}
	if tokens.OutputTokens != nil {
		sess.OutputTokens = tokens.OutputTokens
	}
	if tokens.CacheReadTokens != nil {
		sess.CacheReadTokens = tokens.CacheReadTokens
	}
	if tokens.CacheWriteTokens != nil {
		sess.CacheWriteTokens = tokens.CacheWriteTokens
	}
}

func applyTokenFromMap(sess *SessionRow, m map[string]any) {
	if m == nil {
		return
	}
	if v := asInt64Ptr(m["inputTokens"]); v != nil {
		sess.InputTokens = v
	}
	if v := asInt64Ptr(m["input_tokens"]); v != nil {
		sess.InputTokens = v
	}
	if v := asInt64Ptr(m["outputTokens"]); v != nil {
		sess.OutputTokens = v
	}
	if v := asInt64Ptr(m["output_tokens"]); v != nil {
		sess.OutputTokens = v
	}
	if v := asInt64Ptr(m["cacheReadTokens"]); v != nil {
		sess.CacheReadTokens = v
	}
	if v := asInt64Ptr(m["cache_read_tokens"]); v != nil {
		sess.CacheReadTokens = v
	}
	if v := asInt64Ptr(m["cacheWriteTokens"]); v != nil {
		sess.CacheWriteTokens = v
	}
	if v := asInt64Ptr(m["cache_write_tokens"]); v != nil {
		sess.CacheWriteTokens = v
	}
}

func classifyTurn(bubble, grouping map[string]any) string {
	if asBool(grouping["hasThinking"]) {
		return "thinking"
	}
	if asString(grouping["toolCallCase"]) != "" || asString(grouping["mcpToolName"]) != "" {
		return "tool"
	}
	switch asInt64(bubble["type"]) {
	case 1:
		return "user"
	case 2:
		return "assistant"
	}
	t := strings.ToLower(asString(bubble["type"]))
	if t != "" {
		return t
	}
	return "unknown"
}

func extractText(bubble map[string]any) string {
	candidates := []string{
		asString(bubble["text"]),
		asString(bubble["rawText"]),
		asString(bubble["richText"]),
		nestedString(bubble, "text", "value"),
	}
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return ""
}

func mapAny(m map[string]any, key string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key].(map[string]any)
	return v, ok
}

func firstMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func firstString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return asString(m[key])
}

func firstFloat(m map[string]any, key string) *float64 {
	if m == nil {
		return nil
	}
	return asFloatPtr(m[key])
}

func firstInt64(m map[string]any, key string) *int64 {
	if m == nil {
		return nil
	}
	return asInt64Ptr(m[key])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nestedString(m map[string]any, keys ...string) string {
	cur := any(m)
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[k]
	}
	return asString(cur)
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		s := string(b)
		if len(s) >= 2 && s[0] == '"' {
			return strings.Trim(s, `"`)
		}
		return s
	}
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

func asInt64(v any) int64 {
	if p := asInt64Ptr(v); p != nil {
		return *p
	}
	return 0
}

func asInt64Ptr(v any) *int64 {
	switch t := v.(type) {
	case float64:
		n := int64(t)
		return &n
	case int64:
		return &t
	case int:
		n := int64(t)
		return &n
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return nil
		}
		return &n
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return nil
		}
		return &n
	default:
		return nil
	}
}

func asFloatPtr(v any) *float64 {
	switch t := v.(type) {
	case float64:
		return &t
	case int64:
		f := float64(t)
		return &f
	case int:
		f := float64(t)
		return &f
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return nil
		}
		return &f
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}
