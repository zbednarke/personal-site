package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bundle import: POST /v1/commonplace/import/bundle, multipart/form-data with
// a "manifest" part (JSON, see docs/commonplace/README.md) and one part per
// image or audio file, referenced from the manifest by its form field name (or
// its file name). Idempotent on externalKey: a re-import updates in place.

// Cloud Run rejects request bodies over 32 MiB; stay just under it.
const cpImportLimit = 32<<20 - 256<<10

type cpUpload struct {
	Field, FileName, ContentType string
	Body                         []byte
	used                         bool
}

// Form fields that are settings rather than files.
var cpFormValues = map[string]bool{"manifest": true, "dryRun": true, "kind": true, "gapHours": true, "timezone": true, "me": true, "mediaManifest": true, "channelLabel": true}

type cpMultipart struct {
	Values map[string]string
	Files  []*cpUpload
}

// cpReadMultipart reads the whole request (bounded) into memory.
func cpReadMultipart(w http.ResponseWriter, r *http.Request) (*cpMultipart, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, errors.New("send multipart/form-data")
	}
	r.Body = http.MaxBytesReader(w, r.Body, cpImportLimit)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errors.New("send multipart/form-data")
	}
	out := &cpMultipart{Values: map[string]string{}}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return nil, errors.New("upload is larger than 32 MB; send it in smaller batches")
			}
			return nil, errors.New("unreadable multipart body")
		}
		body, err := cpReadAll(part, cpImportLimit)
		if err != nil {
			return nil, errors.New("upload is larger than 32 MB; send it in smaller batches")
		}
		name := part.FormName()
		if part.FileName() == "" && cpFormValues[name] {
			out.Values[name] = string(body)
			continue
		}
		out.Files = append(out.Files, &cpUpload{Field: name, FileName: part.FileName(), ContentType: part.Header.Get("Content-Type"), Body: body})
		if len(out.Files) > 2000 {
			return nil, errors.New("at most 2000 files per request")
		}
	}
	return out, nil
}

// ---- Manifest -----------------------------------------------------------------

type cpBundle struct {
	Version     int                  `json:"version"`
	ExternalKey string               `json:"externalKey"`
	Moment      cpBundleMoment       `json:"moment"`
	People      []cpBundlePerson     `json:"people"`
	Artifacts   []cpBundleArtifact   `json:"artifacts"`
	Lines       []cpBundleLine       `json:"lines"`
	Annotations []cpBundleAnnotation `json:"annotations"`
	Threads     []cpBundleThread     `json:"threads"`
}

type cpBundleMoment struct {
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Why          string `json:"why"`
	OccurredAt   string `json:"occurredAt"`
	EndedAt      string `json:"endedAt"`
	Timezone     string `json:"timezone"`
	Source       string `json:"source"`
	SourceDetail string `json:"sourceDetail"`
}

type cpBundlePerson struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Role    string   `json:"role"`
	Roles   []string `json:"roles"`
	Special *bool    `json:"special"`
}

type cpBundleArtifact struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	File         string `json:"file"`
	Caption      string `json:"caption"`
	Alt          string `json:"alt"`
	OriginalName string `json:"originalName"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	DurationMS   int    `json:"durationMs"`
	Text         string `json:"text"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	SiteName     string `json:"siteName"`
	CapturedText string `json:"capturedText"`
	CapturedAt   string `json:"capturedAt"`
}

type cpBundleLine struct {
	Key          string          `json:"key"`
	Speaker      string          `json:"speaker"`
	SpeakerLabel string          `json:"speakerLabel"`
	Text         string          `json:"text"`
	At           string          `json:"at"`
	TimeLabel    string          `json:"timeLabel"`
	DayLabel     string          `json:"dayLabel"`
	Meta         json.RawMessage `json:"meta"`
	Artifact     string          `json:"artifact"`
	Rect         []float64       `json:"rect"`
}

type cpBundleAnchor struct {
	Line     string    `json:"line"`
	Artifact string    `json:"artifact"`
	Rect     []float64 `json:"rect"`
}

type cpBundleLink struct {
	URL    string `json:"url"`
	Moment string `json:"moment"`
	Label  string `json:"label"`
}

type cpBundleAnnotation struct {
	Key    string          `json:"key"`
	State  string          `json:"state"`
	Type   string          `json:"type"`
	Title  string          `json:"title"`
	Body   string          `json:"body"`
	Anchor *cpBundleAnchor `json:"anchor"`
	Link   *cpBundleLink   `json:"link"`
}

type cpBundleKnot struct {
	Moment string `json:"moment"`
	Line   string `json:"line"`
	Label  string `json:"label"`
	Style  string `json:"style"`
}

type cpBundleThread struct {
	Key         string         `json:"key"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Knots       []cpBundleKnot `json:"knots"`
}

type cpImportReport struct {
	DryRun       bool           `json:"dryRun"`
	MomentID     uuid.UUID      `json:"momentId"`
	ExternalKey  string         `json:"externalKey"`
	Created      bool           `json:"created"`
	Counts       map[string]int `json:"counts"`
	FilesStored  int            `json:"filesStored"`
	FilesKept    int            `json:"filesKept"`
	MissingFiles []string       `json:"missingFiles"`
	UnusedFiles  []string       `json:"unusedFiles"`
	Removed      map[string]int `json:"removed"`
	Warnings     []string       `json:"warnings"`
}

// cpValidateBundle checks the whole manifest before anything is written, and
// reports every problem with its path.
func cpValidateBundle(b *cpBundle) error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if b.Version != 1 {
		bad("version must be 1")
	}
	if !cpKeyPatten.MatchString(b.ExternalKey) {
		bad("externalKey is required: 1–200 letters, digits or . _ : / @ # + -")
	}
	m := b.Moment
	if m.Kind != "" && !cpKinds[m.Kind] {
		bad("moment.kind must be conversation, dream, idea or quote")
	}
	if m.Source != "" && !cpSources[m.Source] {
		bad("moment.source must be imessage, discord, voice, text, link or other")
	}
	if _, err := cpTimezone(m.Timezone); err != nil {
		bad("moment.timezone: %v", err)
	}
	occurred, err := cpTime(m.OccurredAt, "moment.occurredAt")
	if err != nil {
		errs = append(errs, err)
	}
	ended, err := cpTime(m.EndedAt, "moment.endedAt")
	if err != nil {
		errs = append(errs, err)
	}
	if occurred != nil && ended != nil && ended.Before(*occurred) {
		bad("moment.endedAt must not be before occurredAt")
	}
	if _, err := cpOneLine(m.Title, 300, "moment.title"); err != nil {
		errs = append(errs, err)
	}
	if _, err := cpText(m.Why, 8000, "moment.why"); err != nil {
		errs = append(errs, err)
	}
	if _, err := cpOneLine(m.SourceDetail, 300, "moment.sourceDetail"); err != nil {
		errs = append(errs, err)
	}
	if len(b.People) > 50 || len(b.Artifacts) > 200 || len(b.Lines) > 5000 || len(b.Annotations) > 1000 || len(b.Threads) > 50 {
		bad("too many entries (limits: 50 people, 200 artifacts, 5000 lines, 1000 annotations, 50 threads)")
	}
	people := map[string]bool{}
	for i, p := range b.People {
		at := fmt.Sprintf("people[%d]", i)
		if !cpKeyPatten.MatchString(p.Key) || p.Key == "me" {
			bad("%s.key is required (and \"me\" is reserved for the owner)", at)
		} else if people[p.Key] {
			bad("%s.key %q is repeated", at, p.Key)
		}
		people[p.Key] = true
		if name, err := cpOneLine(p.Name, 120, at+".name"); err != nil || name == "" {
			bad("%s.name is required (at most 120 characters)", at)
		}
		if _, err := cpCleanAliases(p.Aliases); err != nil {
			bad("%s.aliases: %v", at, err)
		}
		for _, role := range append([]string{p.Role}, p.Roles...) {
			if role != "" && !cpRoles[role] {
				bad("%s role %q must be sender, recipient or mentioned", at, role)
			}
		}
	}
	artifacts := map[string]string{}
	for i, a := range b.Artifacts {
		at := fmt.Sprintf("artifacts[%d]", i)
		if !cpKeyPatten.MatchString(a.Key) || strings.HasPrefix(a.Key, "upload-") {
			bad("%s.key is required (the upload- prefix is reserved)", at)
		} else if artifacts[a.Key] != "" {
			bad("%s.key %q is repeated", at, a.Key)
		}
		artifacts[a.Key] = a.Kind
		switch a.Kind {
		case "image", "audio":
			if a.Text != "" || a.URL != "" || a.CapturedText != "" {
				bad("%s: %s artifacts take a file, not text or url", at, a.Kind)
			}
		case "text":
			if a.File != "" {
				bad("%s: text artifacts carry their text inline", at)
			}
		case "link":
			if u, err := cpHTTPURL(a.URL, at+".url"); err != nil || u == "" {
				bad("%s.url must be an http(s) URL", at)
			}
		default:
			bad("%s.kind must be image, audio, text or link", at)
		}
		if _, err := cpText(a.Text, 200000, at+".text"); err != nil {
			errs = append(errs, err)
		}
		if _, err := cpText(a.CapturedText, 200000, at+".capturedText"); err != nil {
			errs = append(errs, err)
		}
		if _, err := cpTime(a.CapturedAt, at+".capturedAt"); err != nil {
			errs = append(errs, err)
		}
		for field, v := range map[string]string{"caption": a.Caption, "title": a.Title} {
			if _, err := cpOneLine(v, 500, at+"."+field); err != nil {
				errs = append(errs, err)
			}
		}
		if _, err := cpText(a.Alt, 2000, at+".alt"); err != nil {
			errs = append(errs, err)
		}
		if a.Width < 0 || a.Height < 0 || a.Width > inspirationMaxPixels || a.Height > inspirationMaxPixels || a.DurationMS < 0 {
			bad("%s width, height or durationMs is out of range", at)
		}
	}
	lines := map[string]bool{}
	for i, l := range b.Lines {
		at := fmt.Sprintf("lines[%d]", i)
		if !cpKeyPatten.MatchString(l.Key) || strings.HasPrefix(l.Key, "owner-") {
			bad("%s.key is required (the owner- prefix is reserved)", at)
		} else if lines[l.Key] {
			bad("%s.key %q is repeated", at, l.Key)
		}
		lines[l.Key] = true
		if l.Speaker != "" && l.Speaker != "me" && !people[l.Speaker] {
			bad("%s.speaker %q must be \"me\" or a key from people", at, l.Speaker)
		}
		if _, err := cpText(l.Text, 40000, at+".text"); err != nil {
			errs = append(errs, err)
		}
		if _, err := cpTime(l.At, at+".at"); err != nil {
			errs = append(errs, err)
		}
		if _, err := cpMeta(l.Meta, at+".meta"); err != nil {
			errs = append(errs, err)
		}
		if l.Artifact != "" && artifacts[l.Artifact] != "image" {
			bad("%s.artifact %q must be the key of an image artifact", at, l.Artifact)
		}
		if _, err := cpRect(l.Rect, at+".rect"); err != nil {
			errs = append(errs, err)
		}
		if l.Rect != nil && l.Artifact == "" {
			bad("%s.rect needs an artifact", at)
		}
		for field, v := range map[string]string{"timeLabel": l.TimeLabel, "dayLabel": l.DayLabel, "speakerLabel": l.SpeakerLabel} {
			if _, err := cpOneLine(v, 120, at+"."+field); err != nil {
				errs = append(errs, err)
			}
		}
	}
	notes := map[string]bool{}
	for i, n := range b.Annotations {
		at := fmt.Sprintf("annotations[%d]", i)
		if !cpKeyPatten.MatchString(n.Key) {
			bad("%s.key is required", at)
		} else if notes[n.Key] {
			bad("%s.key %q is repeated", at, n.Key)
		}
		notes[n.Key] = true
		if n.State != "" && n.State != "pencil" && n.State != "ink" {
			bad("%s.state must be pencil or ink", at)
		}
		if n.Type != "" && !cpNoteType.MatchString(n.Type) {
			bad("%s.type must be a short lowercase word", at)
		}
		if strings.TrimSpace(n.Title) == "" && strings.TrimSpace(n.Body) == "" {
			bad("%s needs a title or a body", at)
		}
		if _, err := cpText(n.Title, 500, at+".title"); err != nil {
			errs = append(errs, err)
		}
		if _, err := cpText(n.Body, 8000, at+".body"); err != nil {
			errs = append(errs, err)
		}
		if a := n.Anchor; a != nil {
			if a.Line != "" && !lines[a.Line] {
				bad("%s.anchor.line %q is not a line key", at, a.Line)
			}
			if a.Artifact != "" && artifacts[a.Artifact] == "" {
				bad("%s.anchor.artifact %q is not an artifact key", at, a.Artifact)
			}
			if _, err := cpRect(a.Rect, at+".anchor.rect"); err != nil {
				errs = append(errs, err)
			}
			if a.Rect != nil && a.Artifact == "" {
				bad("%s.anchor.rect needs anchor.artifact", at)
			}
		}
		if l := n.Link; l != nil {
			if _, err := cpHTTPURL(l.URL, at+".link.url"); err != nil {
				errs = append(errs, err)
			}
			if l.URL == "" && l.Moment == "" {
				bad("%s.link needs a url or a moment externalKey", at)
			}
			if _, err := cpOneLine(l.Label, 200, at+".link.label"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	threads := map[string]bool{}
	for i, t := range b.Threads {
		at := fmt.Sprintf("threads[%d]", i)
		if !cpKeyPatten.MatchString(t.Key) {
			bad("%s.key is required", at)
		} else if threads[t.Key] {
			bad("%s.key %q is repeated", at, t.Key)
		}
		threads[t.Key] = true
		if _, err := cpOneLine(t.Title, 300, at+".title"); err != nil {
			errs = append(errs, err)
		}
		if len(t.Knots) > 500 {
			bad("%s has too many knots", at)
		}
		for k, knot := range t.Knots {
			kat := fmt.Sprintf("%s.knots[%d]", at, k)
			self := knot.Moment == "" || knot.Moment == "self" || knot.Moment == b.ExternalKey
			if self && knot.Line != "" && !lines[knot.Line] {
				bad("%s.line %q is not a line key", kat, knot.Line)
			}
			if knot.Style != "" && knot.Style != "fire" && knot.Style != "plain" {
				bad("%s.style must be fire or plain", kat)
			}
			if _, err := cpOneLine(knot.Label, 200, kat+".label"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// cpFindUpload matches a manifest file reference to an uploaded part: by form
// field name first, then by file name (with or without folders).
func cpFindUpload(files []*cpUpload, ref string) *cpUpload {
	if ref == "" {
		return nil
	}
	for _, f := range files {
		if f.Field == ref {
			return f
		}
	}
	for _, f := range files {
		if f.FileName == ref || path.Base(strings.ReplaceAll(f.FileName, `\`, "/")) == path.Base(ref) {
			return f
		}
	}
	return nil
}

func (app *application) cpImportBundle(w http.ResponseWriter, r *http.Request) {
	form, err := cpReadMultipart(w, r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	raw, ok := form.Values["manifest"]
	if !ok {
		for _, f := range form.Files {
			if f.Field == "manifest" || f.FileName == "manifest.json" {
				raw, ok, f.used = string(f.Body), true, true
				break
			}
		}
	}
	if !ok {
		writeError(w, 400, "the manifest part is missing")
		return
	}
	var bundle cpBundle
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&bundle); err != nil {
		writeError(w, 422, "manifest is not valid: "+err.Error())
		return
	}
	if err := cpValidateBundle(&bundle); err != nil {
		writeJSON(w, 422, map[string]any{"error": "manifest is not valid", "problems": strings.Split(err.Error(), "\n")})
		return
	}
	dryRun := form.Values["dryRun"] == "1" || r.URL.Query().Get("dryRun") == "1"
	// Validate every referenced file before writing anything.
	media := map[string]*cpMedia{}
	var problems []string
	for i, a := range bundle.Artifacts {
		if a.Kind != "image" && a.Kind != "audio" {
			continue
		}
		up := cpFindUpload(form.Files, firstNonEmpty(a.File, a.Key))
		if up == nil {
			continue
		}
		up.used = true
		var wd, ht *int
		if a.Width > 0 && a.Height > 0 {
			wd, ht = &a.Width, &a.Height
		}
		m, err := cpValidateMedia(up.Body, up.ContentType, wd, ht)
		if err != nil {
			problems = append(problems, fmt.Sprintf("artifacts[%d] (%s): %v", i, a.Key, err))
			continue
		}
		if m.Kind != a.Kind {
			problems = append(problems, fmt.Sprintf("artifacts[%d] (%s): the file is %s, not %s", i, a.Key, m.Kind, a.Kind))
			continue
		}
		if a.OriginalName == "" {
			bundle.Artifacts[i].OriginalName = cpCleanFileName(up.FileName)
		}
		media[a.Key] = &m
	}
	if len(problems) > 0 {
		writeJSON(w, 422, map[string]any{"error": "some files are not valid", "problems": problems})
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	report, err := app.cpApplyBundle(r.Context(), user, &bundle, media, dryRun)
	if err != nil {
		var invalid cpInvalid
		if errors.As(err, &invalid) {
			writeError(w, 422, invalid.Error())
			return
		}
		app.serverError(w, err)
		return
	}
	for _, f := range form.Files {
		if !f.used {
			report.UnusedFiles = append(report.UnusedFiles, firstNonEmpty(f.Field, f.FileName))
		}
	}
	status := 200
	if report.Created && !dryRun {
		status = 201
	}
	writeJSON(w, status, report)
}

type cpInvalid struct{ msg string }

func (e cpInvalid) Error() string { return e.msg }

// cpApplyBundle writes the Moment in one transaction. Objects are uploaded
// before commit (and removed again if the transaction fails); superseded
// objects are removed after commit. A dry run rolls back and uploads nothing.
func (app *application) cpApplyBundle(ctx context.Context, user uuid.UUID, b *cpBundle, media map[string]*cpMedia, dryRun bool) (*cpImportReport, error) {
	rep := &cpImportReport{DryRun: dryRun, ExternalKey: b.ExternalKey, Counts: map[string]int{}, Removed: map[string]int{}, MissingFiles: []string{}, UnusedFiles: []string{}, Warnings: []string{}}
	store := app.inspirationObjects()
	var newObjects, oldObjects []string
	committed := false
	defer func() {
		if !committed {
			app.deleteObjectsBestEffort(context.Background(), newObjects)
		}
	}()
	tx, err := app.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	m := b.Moment
	tz, _ := cpTimezone(m.Timezone)
	occurred, _ := cpTime(m.OccurredAt, "")
	ended, _ := cpTime(m.EndedAt, "")
	title, _ := cpOneLine(m.Title, 300, "")
	detail, _ := cpOneLine(m.SourceDetail, 300, "")
	kind, source := firstNonEmpty(m.Kind, "conversation"), firstNonEmpty(m.Source, "other")
	var moment uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM cp_moments WHERE user_id=$1 AND external_key=$2 FOR UPDATE`, user, b.ExternalKey).Scan(&moment)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		moment, rep.Created = uuid.New(), true
		_, err = tx.Exec(ctx, `INSERT INTO cp_moments(id,user_id,kind,occurred_at,ended_at,timezone,source,source_detail,title,why,external_key,imported_from)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'bundle')`, moment, user, kind, occurred, ended, tz, source, detail, title, strings.TrimSpace(m.Why), b.ExternalKey)
	case err == nil:
		_, err = tx.Exec(ctx, `UPDATE cp_moments SET kind=$2,occurred_at=$3,ended_at=$4,timezone=$5,source=$6,source_detail=$7,title=$8,why=$9,imported_from='bundle',revision=revision+1,updated_at=now() WHERE id=$1`,
			moment, kind, occurred, ended, tz, source, detail, title, strings.TrimSpace(m.Why))
	}
	if err != nil {
		return nil, err
	}
	rep.MomentID = moment

	// People: keys are global, so the same friend keeps one identity across bundles.
	personIDs := map[string]uuid.UUID{}
	if _, err := tx.Exec(ctx, `DELETE FROM cp_moment_people WHERE moment_id=$1`, moment); err != nil {
		return nil, err
	}
	for i, p := range b.People {
		name, _ := cpOneLine(p.Name, 120, "")
		aliases, _ := cpCleanAliases(p.Aliases)
		key := "bundle:" + p.Key
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM cp_people WHERE user_id=$1 AND person_key=$2`, user, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// Claim an existing unkeyed person with the same name or alias.
			err = tx.QueryRow(ctx, `SELECT id FROM cp_people WHERE user_id=$1 AND person_key IS NULL AND (lower(display_name)=lower($2) OR lower($2)=ANY(SELECT lower(x) FROM unnest(aliases) x)) ORDER BY created_at LIMIT 1`, user, name).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				id = uuid.New()
				_, err = tx.Exec(ctx, `INSERT INTO cp_people(id,user_id,person_key,display_name,aliases,special) VALUES($1,$2,$3,$4,$5,$6)`, id, user, key, name, aliases, p.Special != nil && *p.Special)
				rep.Counts["peopleCreated"]++
			} else if err == nil {
				_, err = tx.Exec(ctx, `UPDATE cp_people SET person_key=$2 WHERE id=$1`, id, key)
			}
		}
		if err != nil {
			return nil, err
		}
		// Merge aliases; the owner's own edits to the name are kept unless the bundle changes it.
		if _, err := tx.Exec(ctx, `UPDATE cp_people SET display_name=$2,aliases=ARRAY(SELECT DISTINCT x FROM unnest(aliases||$3::text[]) x ORDER BY x),special=coalesce($4,special),updated_at=now() WHERE id=$1`, id, name, aliases, p.Special); err != nil {
			return nil, err
		}
		personIDs[p.Key] = id
		roles := p.Roles
		if p.Role != "" || len(roles) == 0 {
			roles = append([]string{firstNonEmpty(p.Role, "sender")}, roles...)
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, `INSERT INTO cp_moment_people(moment_id,person_id,user_id,role,position) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, moment, id, user, role, i); err != nil {
				return nil, err
			}
		}
	}
	rep.Counts["people"] = len(b.People)

	// Artifacts.
	type existingArtifact struct {
		id     uuid.UUID
		object *string
		sha    *string
	}
	existing := map[string]existingArtifact{}
	rows, err := tx.Query(ctx, `SELECT artifact_key,id,object_name,sha256 FROM cp_artifacts WHERE moment_id=$1`, moment)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var e existingArtifact
		if err := rows.Scan(&k, &e.id, &e.object, &e.sha); err != nil {
			rows.Close()
			return nil, err
		}
		existing[k] = e
	}
	rows.Close()
	artifactIDs := map[string]uuid.UUID{}
	keep := map[string]bool{}
	for pos, a := range b.Artifacts {
		keep[a.Key] = true
		e, found := existing[a.Key]
		id := e.id
		if !found {
			id = uuid.New()
		}
		artifactIDs[a.Key] = id
		var object, contentType, sha *string
		var size *int64
		var wd, ht, dur *int
		if a.DurationMS > 0 {
			dur = &a.DurationMS
		}
		if a.Kind == "image" || a.Kind == "audio" {
			if md := media[a.Key]; md != nil {
				if found && e.sha != nil && *e.sha == md.SHA256 && e.object != nil {
					object = e.object
					rep.FilesKept++
				} else {
					name := cpObjectName(user, moment, id, *md)
					if !dryRun {
						if err := cpPutMedia(ctx, store, name, user, moment, id, *md); err != nil {
							return nil, err
						}
						newObjects = append(newObjects, name)
					}
					if found && e.object != nil && *e.object != name {
						oldObjects = append(oldObjects, *e.object)
					}
					object = &name
					rep.FilesStored++
				}
				ct, s, n := md.ContentType, md.SHA256, int64(len(md.Body))
				contentType, sha, size, wd, ht = &ct, &s, &n, md.Width, md.Height
			} else if found && e.object != nil {
				rep.FilesKept++
			} else {
				rep.MissingFiles = append(rep.MissingFiles, a.Key)
			}
		}
		urlValue, _ := cpHTTPURL(a.URL, "")
		capturedAt, _ := cpTime(a.CapturedAt, "")
		caption, _ := cpOneLine(a.Caption, 500, "")
		linkTitle, _ := cpOneLine(a.Title, 500, "")
		site, _ := cpOneLine(a.SiteName, 200, "")
		if !found {
			_, err = tx.Exec(ctx, `INSERT INTO cp_artifacts(id,moment_id,user_id,artifact_key,position,kind,object_name,content_type,size_bytes,sha256,width,height,duration_ms,original_name,caption,alt,text_content,url,link_title,site_name,captured_text,captured_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
				id, moment, user, a.Key, pos, a.Kind, object, contentType, size, sha, wd, ht, dur, cpCleanFileName(a.OriginalName), caption, a.Alt, a.Text, urlValue, linkTitle, site, a.CapturedText, capturedAt)
		} else if object != nil && contentType != nil {
			_, err = tx.Exec(ctx, `UPDATE cp_artifacts SET position=$2,kind=$3,object_name=$4,content_type=$5,size_bytes=$6,sha256=$7,width=coalesce($8,width),height=coalesce($9,height),duration_ms=coalesce($10,duration_ms),
 original_name=CASE WHEN $11='' THEN original_name ELSE $11 END,caption=$12,alt=$13,text_content=$14,url=$15,link_title=$16,site_name=$17,captured_text=$18,captured_at=$19,updated_at=now() WHERE id=$1`,
				id, pos, a.Kind, object, contentType, size, sha, wd, ht, dur, cpCleanFileName(a.OriginalName), caption, a.Alt, a.Text, urlValue, linkTitle, site, a.CapturedText, capturedAt)
		} else {
			_, err = tx.Exec(ctx, `UPDATE cp_artifacts SET position=$2,kind=$3,duration_ms=coalesce($4,duration_ms),caption=$5,alt=$6,text_content=$7,url=$8,link_title=$9,site_name=$10,captured_text=$11,captured_at=$12,
 object_name=CASE WHEN $3 IN ('image','audio') THEN object_name ELSE NULL END,updated_at=now() WHERE id=$1`,
				id, pos, a.Kind, dur, caption, a.Alt, a.Text, urlValue, linkTitle, site, a.CapturedText, capturedAt)
		}
		if err != nil {
			return nil, err
		}
	}
	for k, e := range existing {
		if keep[k] || strings.HasPrefix(k, "upload-") {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM cp_artifacts WHERE id=$1`, e.id); err != nil {
			return nil, err
		}
		if e.object != nil {
			oldObjects = append(oldObjects, *e.object)
		}
		rep.Removed["artifacts"]++
	}
	rep.Counts["artifacts"] = len(b.Artifacts)

	// Lines.
	lineIDs := map[string]uuid.UUID{}
	rows, err = tx.Query(ctx, `SELECT line_key,id FROM cp_lines WHERE moment_id=$1`, moment)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var id uuid.UUID
		if err := rows.Scan(&k, &id); err != nil {
			rows.Close()
			return nil, err
		}
		lineIDs[k] = id
	}
	rows.Close()
	keepLines := map[string]bool{}
	for pos, l := range b.Lines {
		keepLines[l.Key] = true
		me := l.Speaker == "me"
		var person *uuid.UUID
		if pid, ok := personIDs[l.Speaker]; ok {
			person = &pid
		}
		at, _ := cpTime(l.At, "")
		meta, _ := cpMeta(l.Meta, "")
		rect, _ := cpRect(l.Rect, "")
		var artifact *uuid.UUID
		if aid, ok := artifactIDs[l.Artifact]; ok {
			artifact = &aid
		}
		label, _ := cpOneLine(l.SpeakerLabel, 120, "")
		timeLabel, _ := cpOneLine(l.TimeLabel, 80, "")
		dayLabel, _ := cpOneLine(l.DayLabel, 120, "")
		if id, ok := lineIDs[l.Key]; ok {
			_, err = tx.Exec(ctx, `UPDATE cp_lines SET position=$2,speaker_is_me=$3,speaker_person_id=$4,speaker_label=$5,body=$6,said_at=$7,time_label=$8,day_label=$9,meta=$10,artifact_id=$11,rect=$12,
 revision=revision+1,updated_at=now() WHERE id=$1 AND (position,speaker_is_me,speaker_person_id,speaker_label,body,said_at,time_label,day_label,meta,artifact_id,rect)
 IS DISTINCT FROM ($2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12::jsonb)`, id, pos, me, person, label, l.Text, at, timeLabel, dayLabel, meta, artifact, cpRectJSON(rect))
		} else {
			id := uuid.New()
			lineIDs[l.Key] = id
			_, err = tx.Exec(ctx, `INSERT INTO cp_lines(id,moment_id,user_id,line_key,position,speaker_is_me,speaker_person_id,speaker_label,body,said_at,time_label,day_label,meta,artifact_id,rect)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, moment, user, l.Key, pos, me, person, label, l.Text, at, timeLabel, dayLabel, meta, artifact, cpRectJSON(rect))
		}
		if err != nil {
			return nil, err
		}
	}
	for k, id := range lineIDs {
		if keepLines[k] || strings.HasPrefix(k, "owner-") {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM cp_lines WHERE id=$1`, id); err != nil {
			return nil, err
		}
		delete(lineIDs, k)
		rep.Removed["lines"]++
	}
	rep.Counts["lines"] = len(b.Lines)

	// Annotations: imported notes are pencil unless marked ink. Notes the owner
	// has kept, erased or edited are never overwritten by a re-import.
	keepNotes := map[string]bool{}
	for _, n := range b.Annotations {
		keepNotes[n.Key] = true
		var line, artifact *uuid.UUID
		var rect []float64
		if a := n.Anchor; a != nil {
			if id, ok := lineIDs[a.Line]; ok {
				line = &id
			}
			if id, ok := artifactIDs[a.Artifact]; ok {
				artifact = &id
			}
			rect, _ = cpRect(a.Rect, "")
		}
		var linkURL, linkLabel string
		var linkMoment *uuid.UUID
		if l := n.Link; l != nil {
			linkURL, _ = cpHTTPURL(l.URL, "")
			linkLabel, _ = cpOneLine(l.Label, 200, "")
			if l.Moment != "" && l.Moment != b.ExternalKey {
				var id uuid.UUID
				err := tx.QueryRow(ctx, `SELECT id FROM cp_moments WHERE user_id=$1 AND external_key=$2`, user, l.Moment).Scan(&id)
				if err == nil {
					linkMoment = &id
				} else if errors.Is(err, pgx.ErrNoRows) {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("annotation %s links to %q, which is not imported yet; re-import this bundle after it to connect them", n.Key, l.Moment))
				} else {
					return nil, err
				}
			}
		}
		state := firstNonEmpty(n.State, "pencil")
		typ := firstNonEmpty(n.Type, "note")
		tag, err := tx.Exec(ctx, `UPDATE cp_annotations SET state=$3,note_type=$4,title=$5,body=$6,line_id=$7,artifact_id=$8,rect=$9,link_url=$10,link_moment_id=$11,link_label=$12,revision=revision+1,updated_at=now()
 WHERE moment_id=$1 AND annotation_key=$2 AND owner_touched_at IS NULL AND (state,note_type,title,body,line_id,artifact_id,rect,link_url,link_moment_id,link_label)
 IS DISTINCT FROM ($3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12)`,
			moment, n.Key, state, typ, strings.TrimSpace(n.Title), strings.TrimSpace(n.Body), line, artifact, cpRectJSON(rect), linkURL, linkMoment, linkLabel)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			_, err = tx.Exec(ctx, `INSERT INTO cp_annotations(id,moment_id,user_id,annotation_key,state,author,note_type,title,body,line_id,artifact_id,rect,link_url,link_moment_id,link_label)
 VALUES($1,$2,$3,$4,$5,'import',$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (moment_id,annotation_key) WHERE annotation_key IS NOT NULL DO NOTHING`,
				uuid.New(), moment, user, n.Key, state, typ, strings.TrimSpace(n.Title), strings.TrimSpace(n.Body), line, artifact, cpRectJSON(rect), linkURL, linkMoment, linkLabel)
			if err != nil {
				return nil, err
			}
		}
	}
	rows, err = tx.Query(ctx, `SELECT id,annotation_key FROM cp_annotations WHERE moment_id=$1 AND author='import' AND owner_touched_at IS NULL AND annotation_key IS NOT NULL`, moment)
	if err != nil {
		return nil, err
	}
	var staleNotes []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return nil, err
		}
		if !keepNotes[k] {
			staleNotes = append(staleNotes, id)
		}
	}
	rows.Close()
	if len(staleNotes) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM cp_annotations WHERE id=ANY($1)`, staleNotes); err != nil {
			return nil, err
		}
		rep.Removed["annotations"] = len(staleNotes)
	}
	rep.Counts["annotations"] = len(b.Annotations)

	// Threads: this bundle owns the knots it imported (imported_by), so other
	// bundles' knots on a shared thread are kept.
	if _, err := tx.Exec(ctx, `DELETE FROM cp_thread_knots WHERE imported_by=$1`, moment); err != nil {
		return nil, err
	}
	knots := 0
	for _, t := range b.Threads {
		title, _ := cpOneLine(t.Title, 300, "")
		var thread uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM cp_threads WHERE user_id=$1 AND thread_key=$2`, user, t.Key).Scan(&thread)
		if errors.Is(err, pgx.ErrNoRows) {
			thread = uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO cp_threads(id,user_id,thread_key,title,description) VALUES($1,$2,$3,$4,$5)`, thread, user, t.Key, title, strings.TrimSpace(t.Description))
		} else if err == nil && (title != "" || t.Description != "") {
			_, err = tx.Exec(ctx, `UPDATE cp_threads SET title=CASE WHEN $2='' THEN title ELSE $2 END,description=CASE WHEN $3='' THEN description ELSE $3 END,revision=revision+1,updated_at=now() WHERE id=$1`, thread, title, strings.TrimSpace(t.Description))
		}
		if err != nil {
			return nil, err
		}
		for pos, k := range t.Knots {
			target := moment
			var line *uuid.UUID
			if k.Moment != "" && k.Moment != "self" && k.Moment != b.ExternalKey {
				err := tx.QueryRow(ctx, `SELECT id FROM cp_moments WHERE user_id=$1 AND external_key=$2`, user, k.Moment).Scan(&target)
				if errors.Is(err, pgx.ErrNoRows) {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("thread %s: knot %d points at %q, which is not imported yet; it was skipped", t.Key, pos, k.Moment))
					continue
				}
				if err != nil {
					return nil, err
				}
				if k.Line != "" {
					var lid uuid.UUID
					if err := tx.QueryRow(ctx, `SELECT id FROM cp_lines WHERE moment_id=$1 AND line_key=$2`, target, k.Line).Scan(&lid); err == nil {
						line = &lid
					}
				}
			} else if id, ok := lineIDs[k.Line]; ok {
				line = &id
			}
			label, _ := cpOneLine(k.Label, 200, "")
			if _, err := tx.Exec(ctx, `INSERT INTO cp_thread_knots(id,thread_id,user_id,moment_id,line_id,position,label,style,imported_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				uuid.New(), thread, user, target, line, pos, label, firstNonEmpty(k.Style, "plain"), moment); err != nil {
				return nil, err
			}
			knots++
		}
	}
	rep.Counts["threads"] = len(b.Threads)
	rep.Counts["knots"] = knots

	if dryRun {
		return rep, nil // the deferred rollback discards everything
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	app.deleteObjectsBestEffort(ctx, oldObjects)
	return rep, nil
}
