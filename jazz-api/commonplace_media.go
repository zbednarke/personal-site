package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Media for Commonplace: images (screenshots) and audio (voice memos) stored
// in the private bucket, validated by content sniffing like inspiration images.

const (
	cpImageLimit = 20 << 20
	cpAudioLimit = 30 << 20
)

var cpAudioTypes = map[string]string{"audio/mpeg": "mp3", "audio/mp4": "m4a", "audio/aac": "aac", "audio/wav": "wav", "audio/webm": "webm", "audio/ogg": "ogg"}

// Declared audio types browsers and phones commonly send, mapped to ours.
var cpAudioAliases = map[string]string{"audio/mp3": "audio/mpeg", "audio/x-m4a": "audio/mp4", "audio/m4a": "audio/mp4", "audio/x-wav": "audio/wav", "audio/wave": "audio/wav", "audio/vnd.wave": "audio/wav", "audio/x-aac": "audio/aac", "audio/aacp": "audio/aac", "video/webm": "audio/webm", "application/ogg": "audio/ogg"}

func cpMediaTypeAllowed(contentType string) bool {
	contentType = strings.ToLower(contentType)
	if _, ok := inspirationImageTypes[contentType]; ok {
		return true
	}
	if _, ok := cpAudioTypes[contentType]; ok {
		return true
	}
	_, ok := cpAudioAliases[contentType]
	return ok
}

// cpSniffAudio recognises the audio containers we accept by their magic bytes.
func cpSniffAudio(b []byte) string {
	switch {
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 4 && string(b[0:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3:
		return "audio/webm"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return "audio/mp4"
	case len(b) >= 3 && string(b[0:3]) == "ID3":
		return "audio/mpeg"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xF6 == 0xF0:
		return "audio/aac"
	case len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "audio/mpeg"
	}
	return ""
}

type cpMedia struct {
	Kind          string // image or audio
	ContentType   string
	Width, Height *int
	SHA256        string
	Body          []byte
}

// cpValidateMedia sniffs a file. declared may be empty (bundle files); when
// set it must be the same family (image vs audio) as the contents.
func cpValidateMedia(body []byte, declared string, width, height *int) (cpMedia, error) {
	m := cpMedia{Body: body}
	if len(body) == 0 {
		return m, errors.New("empty file")
	}
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	if alias, ok := cpAudioAliases[declared]; ok {
		declared = alias
	}
	_, isImage := inspirationImageTypes[http.DetectContentType(body)]
	audio := ""
	if !isImage {
		audio = cpSniffAudio(body)
	}
	switch {
	case audio != "":
		if declared != "" && !strings.HasPrefix(declared, "audio/") && declared != "application/octet-stream" {
			return m, errors.New("declared type does not match the audio contents")
		}
		if len(body) > cpAudioLimit {
			return m, errors.New("audio must be 30 MB or smaller")
		}
		m.Kind, m.ContentType = "audio", audio
	case isImage:
		if len(body) > cpImageLimit {
			return m, errors.New("images must be 20 MB or smaller")
		}
		img, err := sniffInspirationImage(body, "")
		if err != nil {
			return m, err
		}
		if declared != "" && declared != "application/octet-stream" && declared != img.ContentType {
			return m, errors.New("declared image type does not match its contents")
		}
		m.Kind, m.ContentType, m.Width, m.Height = "image", img.ContentType, img.Width, img.Height
		if img.ContentType == "image/webp" {
			m.Width, m.Height = width, height
		}
	default:
		return m, errors.New("unsupported file: use a JPEG, PNG, WebP or GIF image, or MP3, M4A, AAC, WAV, WebM or Ogg audio")
	}
	sum := sha256.Sum256(body)
	m.SHA256 = hex.EncodeToString(sum[:])
	return m, nil
}

func cpMediaExtension(contentType string) string {
	if ext, ok := inspirationImageTypes[contentType]; ok {
		return ext
	}
	return cpAudioTypes[contentType]
}

func cpObjectName(user, moment, artifact uuid.UUID, m cpMedia) string {
	return "commonplace/" + user.String() + "/" + moment.String() + "/" + artifact.String() + "-" + m.SHA256[:16] + "." + cpMediaExtension(m.ContentType)
}

func cpPutMedia(ctx context.Context, store objectStore, name string, user, moment, artifact uuid.UUID, m cpMedia) error {
	return store.Put(ctx, name, m.ContentType, map[string]string{"userId": user.String(), "momentId": moment.String(), "artifactId": artifact.String(), "sha256": m.SHA256}, m.Body)
}

var cpSafeName = regexp.MustCompile(`[^\p{L}\p{N} ._()+-]+`)

func cpCleanFileName(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	if i := strings.LastIndexAny(raw, `/\`); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSpace(cpSafeName.ReplaceAllString(raw, "_"))
	if len([]rune(raw)) > 200 {
		raw = string([]rune(raw)[:200])
	}
	return raw
}

// cpUploadArtifact stores one image or audio file sent as the raw request body
// (the inspiration upload pattern) and appends it to the Moment.
func (app *application) cpUploadArtifact(w http.ResponseWriter, r *http.Request) {
	moment, ok := cpPathID(w, r)
	if !ok {
		return
	}
	cpLongUpload(w)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, cpAudioLimit))
	if err != nil {
		writeError(w, 413, "files must be 30 MB or smaller")
		return
	}
	media, err := cpValidateMedia(body, r.Header.Get("Content-Type"), optionalDimension(r.Header.Get("X-Image-Width")), optionalDimension(r.Header.Get("X-Image-Height")))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	var duration *int
	if v, err := strconv.Atoi(r.Header.Get("X-Duration-Ms")); err == nil && v >= 0 && v < 24*3600*1000 {
		duration = &v
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	if !app.cpMomentExists(w, r, user, moment) {
		return
	}
	id := uuid.New()
	name := cpObjectName(user, moment, id, media)
	if err := cpPutMedia(r.Context(), app.inspirationObjects(), name, user, moment, id, media); err != nil {
		app.serverError(w, err)
		return
	}
	// App uploads use the upload- prefix so bundle re-imports keep them.
	_, err = app.db.Exec(r.Context(), `INSERT INTO cp_artifacts(id,moment_id,user_id,artifact_key,position,kind,object_name,content_type,size_bytes,sha256,width,height,duration_ms,original_name)
 SELECT $1,$2,$3,$4,(SELECT coalesce(max(position)+1,0) FROM cp_artifacts WHERE moment_id=$2),$5,$6,$7,$8,$9,$10,$11,$12,$13 WHERE EXISTS(SELECT 1 FROM cp_moments WHERE id=$2 AND user_id=$3)`,
		id, moment, user, "upload-"+id.String()[:8], media.Kind, name, media.ContentType, len(body), media.SHA256, media.Width, media.Height, duration, cpCleanFileName(r.Header.Get("X-File-Name")))
	if err != nil {
		app.deleteObjectsBestEffort(r.Context(), []string{name})
		app.serverError(w, err)
		return
	}
	_, _ = app.db.Exec(r.Context(), `UPDATE cp_moments SET updated_at=now(),source=CASE WHEN source IN ('other','text') AND $2='audio' THEN 'voice' ELSE source END WHERE id=$1`, moment, media.Kind)
	a, err := cpScanArtifact(app.db.QueryRow(r.Context(), `SELECT `+cpArtifactColumns+` FROM cp_artifacts a WHERE a.id=$1`, id))
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"artifact": a})
}

func (app *application) cpDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var name *string
	err := app.db.QueryRow(r.Context(), `DELETE FROM cp_artifacts WHERE id=$1 AND user_id=$2 RETURNING object_name`, id, user).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "artifact not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	if name != nil {
		app.deleteObjectsBestEffort(r.Context(), []string{*name})
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// cpMedia keeps the bucket private: an authenticated 302 to a ten-minute
// signed URL, as for inspiration images.
func (app *application) cpMedia(w http.ResponseWriter, r *http.Request) {
	id, ok := cpPathID(w, r)
	if !ok {
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	var name *string
	err := app.db.QueryRow(r.Context(), `SELECT object_name FROM cp_artifacts WHERE id=$1 AND user_id=$2`, id, user).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && name == nil) {
		writeError(w, 404, "media not found")
		return
	}
	if err != nil {
		app.serverError(w, err)
		return
	}
	signed, err := app.inspirationObjects().SignedGet(r.Context(), *name, time.Now().Add(10*time.Minute))
	if err != nil {
		app.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.Redirect(w, r, signed, http.StatusFound)
}

// cpReadAll reads a multipart part with a limit.
func cpReadAll(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, errors.New("file too large")
	}
	return buf.Bytes(), nil
}
