package main

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The caller holds the session lock. A marker preserves even intentionally empty
// days, so deleting every section never brings the factory curriculum back.
func (app *application) seedPracticeDay(ctx context.Context, tx pgx.Tx, userID, sessionID uuid.UUID, input bootstrapBlocksRequest) error {
	mode := input.Mode
	if mode == "" {
		// Compatibility with tabs opened before the initialize/add distinction.
		mode = "add"
		for _, block := range input.Blocks {
			if !strings.HasPrefix(block.BlockKey, "custom-") {
				mode = "initialize"
				break
			}
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO practice_day_layouts(session_id,user_id,practice_date) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, sessionID, userID, input.PracticeDate)
	if err != nil {
		return err
	}
	first := tag.RowsAffected() == 1
	seeds := []blockDefinition{}
	if first {
		var previousSession uuid.UUID
		var previousDate string
		err = tx.QueryRow(ctx, `SELECT d.session_id,d.practice_date::text FROM practice_day_layouts d JOIN practice_sessions s ON s.id=d.session_id WHERE d.user_id=$1 AND d.practice_date<$2 ORDER BY d.practice_date DESC,s.started_at DESC LIMIT 1`, userID, input.PracticeDate).Scan(&previousSession, &previousDate)
		if errors.Is(err, pgx.ErrNoRows) {
			if mode == "initialize" {
				for _, block := range input.Blocks {
					if !block.DayOnly {
						seeds = append(seeds, block)
					}
				}
			}
		} else if err != nil {
			return err
		} else {
			rows, err := tx.Query(ctx, `SELECT block_key,position,title,COALESCE(instructions,''),category,track,target_minutes FROM practice_blocks WHERE session_id=$1 AND user_id=$2 AND practice_date=$3 AND removed_at IS NULL AND carry_forward ORDER BY position,id`, previousSession, userID, previousDate)
			if err != nil {
				return err
			}
			for rows.Next() {
				var b blockDefinition
				if err := rows.Scan(&b.BlockKey, &b.Position, &b.Title, &b.Instructions, &b.Category, &b.Track, &b.TargetMinutes); err != nil {
					rows.Close()
					return err
				}
				seeds = append(seeds, b)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
	}
	for _, block := range seeds {
		if err := insertPracticeSeed(ctx, tx, userID, sessionID, input.PracticeDate, block); err != nil {
			return err
		}
	}
	// Daily curriculum refreshes must never overwrite user settings or resurrect
	// removed sections. Explicit additions and dated appointments are additive.
	for _, block := range input.Blocks {
		if mode != "add" && !block.DayOnly {
			continue
		}
		var position int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(position)+1,0) FROM practice_blocks WHERE session_id=$1 AND practice_date=$2 AND removed_at IS NULL`, sessionID, input.PracticeDate).Scan(&position); err != nil {
			return err
		}
		block.Position = position
		if err := insertPracticeSeed(ctx, tx, userID, sessionID, input.PracticeDate, block); err != nil {
			return err
		}
	}
	return nil
}

func insertPracticeSeed(ctx context.Context, tx pgx.Tx, userID, sessionID uuid.UUID, date string, b blockDefinition) error {
	_, err := tx.Exec(ctx, `INSERT INTO practice_blocks(id,session_id,user_id,practice_date,block_key,position,title,instructions,category,track,target_minutes,carry_forward) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12) ON CONFLICT(session_id,practice_date,block_key) DO NOTHING`, uuid.New(), sessionID, userID, date, b.BlockKey, b.Position, b.Title, b.Instructions, b.Category, b.Track, b.TargetMinutes, !b.DayOnly)
	return err
}
