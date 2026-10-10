package main

import (
	"context"
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Keeping the replayed transcript within what one request may carry.
//
// Images are sent inline (base64) and the whole transcript is replayed on
// every call, so old images would eventually push a thread past the request
// size and image limits for good. Images older than the last few turns are
// replaced, once and permanently, by a short placeholder. That edits earlier
// history, which invalidates thinking blocks produced after it, so those
// blocks are removed in the same step (the requests also send
// prefix_mismatch_behavior "drop_block" as a safety net). The edit costs one
// prompt-cache miss and that reasoning; later turns build on the new prefix.

const (
	wbKeepImageTurns   = 6        // the newest API turns keep their images
	wbReplayByteCap    = 20 << 20 // well under the 32 MB request limit
	wbImagePlaceholder = "[image from earlier in this thread, omitted to keep requests small]"
	wbImageTokens      = 1600 // a ≤1568 px image is at most ~1,600 tokens
)

type wbTurnRow struct {
	Seq   int
	Role  string
	Param []byte
}

func (app *application) wbTurnRows(ctx context.Context, q cpDB, thread uuid.UUID) ([]wbTurnRow, error) {
	return wbCollect(wbRows(q.Query(ctx, `SELECT seq,role,param FROM wb_api_turns WHERE thread_id=$1 ORDER BY seq`, thread)), func(row pgx.Row) (wbTurnRow, error) {
		var t wbTurnRow
		return t, row.Scan(&t.Seq, &t.Role, &t.Param)
	})
}

// wbLoadTurnsRaw returns the transcript as SDK params and as stored JSON.
func (app *application) wbLoadTurnsRaw(ctx context.Context, thread uuid.UUID) ([]anthropic.BetaMessageParam, [][]byte, error) {
	rows, err := app.wbTurnRows(ctx, app.db, thread)
	if err != nil {
		return nil, nil, err
	}
	params := make([]anthropic.BetaMessageParam, 0, len(rows))
	raws := make([][]byte, 0, len(rows))
	for _, r := range rows {
		var p anthropic.BetaMessageParam
		if err := json.Unmarshal(r.Param, &p); err != nil {
			return nil, nil, err
		}
		params = append(params, p)
		raws = append(raws, r.Param)
	}
	return params, raws, nil
}

func (app *application) wbTranscriptBytes(ctx context.Context, thread uuid.UUID) (int64, error) {
	var n int64
	err := app.db.QueryRow(ctx, `SELECT coalesce(sum(octet_length(param::text)),0) FROM wb_api_turns WHERE thread_id=$1`, thread).Scan(&n)
	return n, err
}

// wbCompactTurns is the pure rewrite: images in user turns older than the
// newest `keep` turns become placeholders, and thinking blocks in assistant
// turns after the first edited turn are removed. It returns the indexes of the
// rows that changed.
func wbCompactTurns(rows []wbTurnRow, keep int) ([]wbTurnRow, []int) {
	first := -1
	out := make([]wbTurnRow, len(rows))
	copy(out, rows)
	changed := map[int]bool{}
	for i := 0; i < len(out)-keep; i++ {
		if out[i].Role != "user" {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(out[i].Param, &msg) != nil {
			continue
		}
		content, _ := msg["content"].([]any)
		edited := false
		for j, c := range content {
			if block, _ := c.(map[string]any); block != nil && block["type"] == "image" {
				content[j] = map[string]any{"type": "text", "text": wbImagePlaceholder}
				edited = true
			}
		}
		if edited {
			msg["content"] = content
			out[i].Param, _ = json.Marshal(msg)
			changed[i] = true
			if first < 0 {
				first = i
			}
		}
	}
	if first >= 0 {
		for i := first + 1; i < len(out); i++ {
			if out[i].Role != "assistant" {
				continue
			}
			var msg map[string]any
			if json.Unmarshal(out[i].Param, &msg) != nil {
				continue
			}
			content, _ := msg["content"].([]any)
			kept := make([]any, 0, len(content))
			for _, c := range content {
				if block, _ := c.(map[string]any); block != nil && (block["type"] == "thinking" || block["type"] == "redacted_thinking") {
					continue
				}
				kept = append(kept, c)
			}
			if len(kept) == len(content) {
				continue
			}
			if len(kept) == 0 {
				kept = append(kept, map[string]any{"type": "text", "text": "[…]"})
			}
			msg["content"] = kept
			out[i].Param, _ = json.Marshal(msg)
			changed[i] = true
		}
	}
	idx := []int{}
	for i := range out {
		if changed[i] {
			idx = append(idx, i)
		}
	}
	return out, idx
}

// wbCompactHistory applies wbCompactTurns to the stored transcript. It runs
// between turns, never inside a tool round.
func (app *application) wbCompactHistory(ctx context.Context, thread uuid.UUID, keep int) error {
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := app.wbTurnRows(ctx, tx, thread)
	if err != nil {
		return err
	}
	out, changed := wbCompactTurns(rows, keep)
	if len(changed) == 0 {
		return nil
	}
	for _, i := range changed {
		if _, err := tx.Exec(ctx, `UPDATE wb_api_turns SET param=$3 WHERE thread_id=$1 AND seq=$2`, thread, out[i].Seq, out[i].Param); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// wbPromptEstimate is a rough token count of the stored transcript: text at
// ~3 bytes per token (pessimistic), images at their downscaled maximum.
func wbPromptEstimate(raws [][]byte) int64 {
	var textBytes, images int64
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if t["type"] == "image" {
				images++
				return
			}
			for _, c := range t {
				walk(c)
			}
		case []any:
			for _, c := range t {
				walk(c)
			}
		case string:
			textBytes += int64(len(t))
		}
	}
	for _, raw := range raws {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			walk(v)
		}
	}
	return textBytes/3 + images*wbImageTokens
}

var wbFixedPromptTokens = func() int64 {
	tools, _ := json.Marshal(wbTools())
	return int64(len(wbSystemPrompt)+len(tools)) / 3
}()

// wbEstimateCall prices the next call before making it: the previous call's
// prompt plus its reply plus whatever was appended since, all at the
// uncached input rate, and an output allowance of the larger of the last
// reply and 1,000 tokens.
func (app *application) wbEstimateCall(ctx context.Context, thread uuid.UUID, promptEstimate int64) float64 {
	var lastPrompt, lastOutput, lastEstimate int64
	_ = app.db.QueryRow(ctx, `SELECT last_prompt_tokens,last_output_tokens,last_prompt_estimate FROM wb_threads WHERE id=$1`, thread).Scan(&lastPrompt, &lastOutput, &lastEstimate)
	var tokens int64
	if lastPrompt == 0 {
		tokens = wbFixedPromptTokens + promptEstimate
	} else {
		tokens = lastPrompt + lastOutput + max(0, promptEstimate-lastEstimate)
	}
	price := wbPriceFor(app.wb.cfg.Model)
	return (float64(tokens)*price.input + float64(max(lastOutput, 1000))*price.output) / 1e6
}

func wbMergeSources(a, b []string) []string {
	for _, s := range b {
		found := false
		for _, t := range a {
			found = found || s == t
		}
		if !found {
			a = append(a, s)
		}
	}
	return a
}
