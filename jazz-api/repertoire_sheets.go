package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxRepertoireSheetBytes = 25 << 20

var (
	repertoireSheetParts  = map[string]bool{"concert": true, "bb": true, "score": true, "other": true}
	repertoireSheetRights = map[string]bool{"original": true, "public_domain": true, "licensed": true, "owned_copy": true, "unknown": true}
)

type repertoireSheet struct {
	ID           string    `json:"id"`
	TuneID       string    `json:"tuneId"`
	Title        string    `json:"title"`
	Part         string    `json:"part"`
	Rights       string    `json:"rights"`
	OriginalName string    `json:"originalName,omitempty"`
	SourceURL    string    `json:"sourceUrl,omitempty"`
	SizeBytes    int64     `json:"sizeBytes"`
	SHA256       string    `json:"sha256,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	ObjectName   string    `json:"-"`
}

const repertoireSheetColumns = `id,tune_id,title,part,rights,original_name,COALESCE(source_url,''),size_bytes,sha256,created_at,COALESCE(object_name,'')`

func scanRepertoireSheet(row pgx.Row) (repertoireSheet, error) {
	var sheet repertoireSheet
	err := row.Scan(&sheet.ID, &sheet.TuneID, &sheet.Title, &sheet.Part, &sheet.Rights, &sheet.OriginalName,
		&sheet.SourceURL, &sheet.SizeBytes, &sheet.SHA256, &sheet.CreatedAt, &sheet.ObjectName)
	return sheet, err
}

func loadRepertoireSheets(ctx context.Context, db dbQuerier, userID uuid.UUID, tuneID string) (map[string][]repertoireSheet, error) {
	rows, err := db.Query(ctx, `SELECT `+repertoireSheetColumns+` FROM repertoire_sheets
		WHERE user_id=$1 AND ($2='' OR tune_id=$2) ORDER BY created_at,id`, userID, tuneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]repertoireSheet{}
	for rows.Next() {
		sheet, err := scanRepertoireSheet(rows)
		if err != nil {
			return nil, err
		}
		result[sheet.TuneID] = append(result[sheet.TuneID], sheet)
	}
	return result, rows.Err()
}

func decodeSheetHeader(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(raw)
}

func validateRepertoirePDF(body []byte, contentType string) error {
	if len(body) == 0 {
		return errors.New("choose a PDF to upload")
	}
	if len(body) > maxRepertoireSheetBytes {
		return errors.New("PDFs must be 25 MB or smaller")
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/pdf" && mediaType != "application/octet-stream") {
		return errors.New("the file must be a PDF")
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		return errors.New("the file does not contain a valid PDF header")
	}
	return nil
}

func validSheetSourceURL(value string) bool {
	if value == "" {
		return true
	}
	if strings.HasPrefix(value, "/assets/jazz/sheets/") {
		return len(value) <= 1000
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && len(value) <= 1000
}

func (app *application) uploadRepertoireSheet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRepertoireSheetBytes+1))
	if err != nil || len(body) > maxRepertoireSheetBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "PDFs must be 25 MB or smaller")
		return
	}
	if err := validateRepertoirePDF(body, r.Header.Get("Content-Type")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	tuneID := r.PathValue("tuneId")
	var exists bool
	if err := app.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM repertoire_tunes WHERE user_id=$1 AND tune_id=$2)`, userID, tuneID).Scan(&exists); err != nil {
		app.serverError(w, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "tune not found")
		return
	}
	originalName := cpCleanFileName(r.Header.Get("X-File-Name"))
	title := decodeSheetHeader(r.Header.Get("X-Sheet-Title"))
	if title == "" {
		title = strings.TrimSpace(strings.TrimSuffix(originalName, filepath.Ext(originalName)))
	}
	if title == "" || utf8.RuneCountInString(title) > 160 {
		writeError(w, http.StatusUnprocessableEntity, "sheet title must be between 1 and 160 characters")
		return
	}
	part := strings.ToLower(decodeSheetHeader(r.Header.Get("X-Sheet-Part")))
	if !repertoireSheetParts[part] {
		writeError(w, http.StatusUnprocessableEntity, "sheet part must be concert, bb, score or other")
		return
	}
	rights := strings.ToLower(decodeSheetHeader(r.Header.Get("X-Sheet-Rights")))
	if !repertoireSheetRights[rights] {
		writeError(w, http.StatusUnprocessableEntity, "choose how you are allowed to use this PDF")
		return
	}
	sourceURL := decodeSheetHeader(r.Header.Get("X-Source-URL"))
	if !validSheetSourceURL(sourceURL) {
		writeError(w, http.StatusUnprocessableEntity, "source links must use https")
		return
	}
	id := uuid.NewString()
	digest := sha256.Sum256(body)
	sha := hex.EncodeToString(digest[:])
	objectName := "repertoire/" + userID.String() + "/" + tuneID + "/" + id + "-" + sha[:16] + ".pdf"
	if err := app.inspirationObjects().Put(r.Context(), objectName, "application/pdf", map[string]string{
		"userId": userID.String(), "tuneId": tuneID, "sheetId": id, "sha256": sha,
	}, body); err != nil {
		app.serverError(w, err)
		return
	}
	sheet, err := scanRepertoireSheet(app.db.QueryRow(r.Context(), `INSERT INTO repertoire_sheets
		(user_id,id,tune_id,title,part,rights,original_name,source_url,object_name,size_bytes,sha256)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11)
		RETURNING `+repertoireSheetColumns,
		userID, id, tuneID, title, part, rights, originalName, sourceURL, objectName, len(body), sha))
	if err != nil {
		_ = app.inspirationObjects().Delete(r.Context(), objectName)
		app.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sheet": sheet})
}

func (app *application) openRepertoireSheet(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	sheet, err := scanRepertoireSheet(app.db.QueryRow(r.Context(), `SELECT `+repertoireSheetColumns+`
		FROM repertoire_sheets WHERE user_id=$1 AND id=$2`, userID, r.PathValue("sheetId")))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "sheet not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	destination := sheet.SourceURL
	if sheet.ObjectName != "" {
		destination, err = app.inspirationObjects().SignedGet(r.Context(), sheet.ObjectName, time.Now().Add(10*time.Minute))
		if err != nil {
			app.serverError(w, err)
			return
		}
	}
	if destination == "" {
		writeError(w, http.StatusNotFound, "sheet file not found")
		return
	}
	http.Redirect(w, r, destination, http.StatusFound)
}

func (app *application) deleteRepertoireSheet(w http.ResponseWriter, r *http.Request) {
	userID, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	var objectName *string
	err = app.db.QueryRow(r.Context(), `DELETE FROM repertoire_sheets WHERE user_id=$1 AND id=$2 RETURNING object_name`,
		userID, r.PathValue("sheetId")).Scan(&objectName)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "sheet not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if objectName != nil {
		_ = app.inspirationObjects().Delete(r.Context(), *objectName)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
