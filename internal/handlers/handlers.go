package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ArminDashti/centralized-agent-data-api/internal/auth"
	"github.com/ArminDashti/centralized-agent-data-api/internal/config"
	"github.com/ArminDashti/centralized-agent-data-api/internal/ingest"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	db  *sql.DB
	cfg config.Config
}

func New(db *sql.DB, cfg config.Config) *Handler {
	return &Handler{db: db, cfg: cfg}
}

func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "service": "centralized-agent-data-api"})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) Login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	var id, username, hash string
	err := h.db.QueryRowContext(c.Request.Context(), `
		SELECT id::text, username, password_hash FROM users WHERE username = $1
	`, req.Username).Scan(&id, &username, &hash)
	if err != nil || !auth.CheckPassword(hash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	token, err := auth.IssueToken(h.cfg.JWTSecret, id, username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token issue failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  gin.H{"id": id, "username": username},
	})
}

func (h *Handler) IngestSession(c *gin.Context) {
	raw, tokens, err := readIngestPayload(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	parsed, err := ingest.ParseExtract(raw, tokens)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.persistParsed(c.Request.Context(), parsed); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"uuid":         parsed.Session.UUID,
		"turn_count":   len(parsed.Turns),
		"context_pct":  parsed.Session.ContextUsagePercent,
		"input_tokens": parsed.Session.InputTokens,
		"output_tokens": parsed.Session.OutputTokens,
	})
}

func readIngestPayload(c *gin.Context) ([]byte, *ingest.TokenSupplement, error) {
	ct := c.ContentType()
	if strings.HasPrefix(ct, "multipart/form-data") {
		file, err := c.FormFile("file")
		if err != nil {
			return nil, nil, err
		}
		f, err := file.Open()
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		raw, err := io.ReadAll(f)
		if err != nil {
			return nil, nil, err
		}
		var tokens *ingest.TokenSupplement
		if ts := c.PostForm("tokens"); ts != "" {
			var t ingest.TokenSupplement
			if err := json.Unmarshal([]byte(ts), &t); err == nil {
				tokens = &t
			}
		}
		return raw, tokens, nil
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, nil, err
	}
	var wrapper struct {
		Extract json.RawMessage         `json:"extract"`
		Tokens  *ingest.TokenSupplement `json:"tokens"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Extract) > 0 {
		return wrapper.Extract, wrapper.Tokens, nil
	}
	return body, nil, nil
}

func (h *Handler) persistParsed(ctx context.Context, parsed *ingest.Parsed) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	s := parsed.Session
	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (
			uuid, name, subtitle, status, unified_mode, workspace_id, workspace_path,
			context_usage_percent, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			model_config, total_lines_added, total_lines_removed, files_changed_count,
			created_at_ms, last_updated_at_ms, extracted_at, raw_json, updated_at
		) VALUES (
			$1::uuid, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12,
			$13::jsonb, $14, $15, $16,
			$17, $18, $19, $20::jsonb, NOW()
		)
		ON CONFLICT (uuid) DO UPDATE SET
			name = EXCLUDED.name,
			subtitle = EXCLUDED.subtitle,
			status = EXCLUDED.status,
			unified_mode = EXCLUDED.unified_mode,
			workspace_id = EXCLUDED.workspace_id,
			workspace_path = EXCLUDED.workspace_path,
			context_usage_percent = EXCLUDED.context_usage_percent,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			cache_write_tokens = EXCLUDED.cache_write_tokens,
			model_config = EXCLUDED.model_config,
			total_lines_added = EXCLUDED.total_lines_added,
			total_lines_removed = EXCLUDED.total_lines_removed,
			files_changed_count = EXCLUDED.files_changed_count,
			created_at_ms = EXCLUDED.created_at_ms,
			last_updated_at_ms = EXCLUDED.last_updated_at_ms,
			extracted_at = EXCLUDED.extracted_at,
			raw_json = EXCLUDED.raw_json,
			updated_at = NOW()
	`, s.UUID, s.Name, s.Subtitle, s.Status, s.UnifiedMode, s.WorkspaceID, s.WorkspacePath,
		s.ContextUsagePercent, s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens,
		string(s.ModelConfigJSON), s.TotalLinesAdded, s.TotalLinesRemoved, s.FilesChangedCount,
		s.CreatedAtMs, s.LastUpdatedAtMs, s.ExtractedAt, string(s.RawJSON))
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM turns WHERE session_uuid = $1::uuid`, s.UUID); err != nil {
		return err
	}
	for _, t := range parsed.Turns {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO turns (
				session_uuid, bubble_id, ordinal, turn_type, text, tool_name, mcp_name, status,
				duration_ms, has_thinking, thinking_duration_ms, thinking_text,
				input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, bubble_json
			) VALUES (
				$1::uuid, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, $11, $12,
				$13, $14, $15, $16, $17::jsonb
			)
		`, s.UUID, t.BubbleID, t.Ordinal, t.TurnType, t.Text, t.ToolName, t.MCPName, t.Status,
			t.DurationMs, t.HasThinking, t.ThinkingDurationMs, t.ThinkingText,
			t.InputTokens, t.OutputTokens, t.CacheReadTokens, t.CacheWriteTokens, string(t.BubbleJSON)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *Handler) ListSessions(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	workspace := strings.TrimSpace(c.Query("workspace_id"))
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	args := []any{}
	where := []string{"1=1"}
	if q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, "name ILIKE $"+strconv.Itoa(len(args)))
	}
	if workspace != "" {
		args = append(args, workspace)
		where = append(where, "workspace_id = $"+strconv.Itoa(len(args)))
	}
	args = append(args, limit)
	sqlStr := `
		SELECT uuid::text, name, subtitle, status, unified_mode, workspace_id, workspace_path,
			context_usage_percent, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			model_config, total_lines_added, total_lines_removed, files_changed_count,
			created_at_ms, last_updated_at_ms, extracted_at, updated_at
		FROM sessions
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY updated_at DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := h.db.QueryContext(c.Request.Context(), sqlStr, args...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	out := []gin.H{}
	for rows.Next() {
		item, err := scanSessionSummary(rows)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"sessions": out})
}

func (h *Handler) GetSession(c *gin.Context) {
	uuid := c.Param("uuid")
	row := h.db.QueryRowContext(c.Request.Context(), `
		SELECT uuid::text, name, subtitle, status, unified_mode, workspace_id, workspace_path,
			context_usage_percent, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			model_config, total_lines_added, total_lines_removed, files_changed_count,
			created_at_ms, last_updated_at_ms, extracted_at, updated_at
		FROM sessions WHERE uuid = $1::uuid
	`, uuid)
	item, err := scanSessionSummary(row)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var turnCount, thinkingCount int
	_ = h.db.QueryRowContext(c.Request.Context(), `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE has_thinking)
		FROM turns WHERE session_uuid = $1::uuid
	`, uuid).Scan(&turnCount, &thinkingCount)
	item["turn_count"] = turnCount
	item["thinking_count"] = thinkingCount
	c.JSON(http.StatusOK, item)
}

func (h *Handler) GetThinking(c *gin.Context) {
	uuid := c.Param("uuid")
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT bubble_id, ordinal, thinking_duration_ms, thinking_text, duration_ms, status
		FROM turns
		WHERE session_uuid = $1::uuid AND has_thinking = TRUE
		ORDER BY ordinal ASC
	`, uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var bubbleID string
		var ordinal int
		var thinkDur, dur sql.NullInt64
		var text, status string
		if err := rows.Scan(&bubbleID, &ordinal, &thinkDur, &text, &dur, &status); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"bubble_id":            bubbleID,
			"ordinal":              ordinal,
			"thinking_duration_ms": nullInt(thinkDur),
			"thinking_text":        text,
			"duration_ms":          nullInt(dur),
			"status":               status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"thinking": items})
}

func (h *Handler) GetTurns(c *gin.Context) {
	uuid := c.Param("uuid")
	rows, err := h.db.QueryContext(c.Request.Context(), `
		SELECT bubble_id, ordinal, turn_type, text, tool_name, mcp_name, status, duration_ms,
			has_thinking, thinking_duration_ms, thinking_text,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		FROM turns
		WHERE session_uuid = $1::uuid
		ORDER BY ordinal ASC
	`, uuid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var bubbleID, turnType, text, toolName, mcpName, status, thinkingText string
		var ordinal int
		var hasThinking bool
		var dur, thinkDur, inTok, outTok, cr, cw sql.NullInt64
		if err := rows.Scan(&bubbleID, &ordinal, &turnType, &text, &toolName, &mcpName, &status, &dur,
			&hasThinking, &thinkDur, &thinkingText, &inTok, &outTok, &cr, &cw); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items = append(items, gin.H{
			"bubble_id":            bubbleID,
			"ordinal":              ordinal,
			"turn_type":            turnType,
			"text":                 text,
			"tool_name":            toolName,
			"mcp_name":             mcpName,
			"status":               status,
			"duration_ms":          nullInt(dur),
			"has_thinking":         hasThinking,
			"thinking_duration_ms": nullInt(thinkDur),
			"thinking_text":        thinkingText,
			"input_tokens":         nullInt(inTok),
			"output_tokens":        nullInt(outTok),
			"cache_read_tokens":    nullInt(cr),
			"cache_write_tokens":   nullInt(cw),
		})
	}
	c.JSON(http.StatusOK, gin.H{"turns": items})
}

type scannable interface {
	Scan(dest ...any) error
}

func scanSessionSummary(row scannable) (gin.H, error) {
	var (
		uuid, name, subtitle, status, mode, wsID, wsPath string
		ctxPct                                           sql.NullFloat64
		inTok, outTok, cr, cw                            sql.NullInt64
		modelJSON                                        []byte
		linesAdd, linesRem, filesChanged                 int
		createdMs, updatedMs                             sql.NullInt64
		extractedAt, updatedAt                           sql.NullTime
	)
	if err := row.Scan(&uuid, &name, &subtitle, &status, &mode, &wsID, &wsPath,
		&ctxPct, &inTok, &outTok, &cr, &cw, &modelJSON,
		&linesAdd, &linesRem, &filesChanged, &createdMs, &updatedMs, &extractedAt, &updatedAt); err != nil {
		return nil, err
	}
	var model any
	_ = json.Unmarshal(modelJSON, &model)
	return gin.H{
		"uuid":                   uuid,
		"name":                   name,
		"subtitle":               subtitle,
		"status":                 status,
		"unified_mode":           mode,
		"workspace_id":           wsID,
		"workspace_path":         wsPath,
		"context_usage_percent":  nullFloat(ctxPct),
		"input_tokens":           nullInt(inTok),
		"output_tokens":          nullInt(outTok),
		"cache_read_tokens":      nullInt(cr),
		"cache_write_tokens":     nullInt(cw),
		"model_config":           model,
		"total_lines_added":      linesAdd,
		"total_lines_removed":    linesRem,
		"files_changed_count":    filesChanged,
		"created_at_ms":          nullInt(createdMs),
		"last_updated_at_ms":     nullInt(updatedMs),
		"extracted_at":           nullTime(extractedAt),
		"updated_at":             nullTime(updatedAt),
	}, nil
}

func nullInt(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func nullTime(v sql.NullTime) any {
	if !v.Valid {
		return nil
	}
	return v.Time.Format(time.RFC3339Nano)
}
