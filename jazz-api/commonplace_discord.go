package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// One-time Discord import from a DiscordChatExporter JSON export of one
// channel (optionally exported with --media, in which case attachment URLs
// are relative paths to the downloaded files). Messages are grouped into
// Moments: each top-level post starts a Moment, and replies and follow-ups
// within a gap (default 3 h) attach to it. Idempotent on message ids.

type dceAuthor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
	IsBot    bool   `json:"isBot"`
}

type dceAttachment struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	FileName      string `json:"fileName"`
	FileSizeBytes int64  `json:"fileSizeBytes"`
}

type dceReaction struct {
	Emoji struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Code string `json:"code"`
	} `json:"emoji"`
	Count int `json:"count"`
}

type dceMessage struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	Timestamp       time.Time       `json:"timestamp"`
	TimestampEdited *time.Time      `json:"timestampEdited"`
	IsPinned        bool            `json:"isPinned"`
	Content         string          `json:"content"`
	Author          dceAuthor       `json:"author"`
	Attachments     []dceAttachment `json:"attachments"`
	Reactions       []dceReaction   `json:"reactions"`
	Mentions        []dceAuthor     `json:"mentions"`
	Reference       *struct {
		MessageID string `json:"messageId"`
		ChannelID string `json:"channelId"`
	} `json:"reference"`
}

type dceExport struct {
	Guild struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
	Channel struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Category string `json:"category"`
	} `json:"channel"`
	Messages []dceMessage `json:"messages"`
}

const cpLongPostRunes = 280

// cpGroupDiscord groups message indexes (messages sorted by time) into
// Moments. A reply joins the group of the message it replies to. Another
// message joins the latest group when it comes within gap of that group's
// last message, unless it is a long standalone post by someone other than
// the group's first author, which starts its own Moment.
func cpGroupDiscord(msgs []dceMessage, gap time.Duration) [][]int {
	groupOf := map[string]int{}
	var groups [][]int
	var last []time.Time
	for i, m := range msgs {
		g := -1
		if m.Reference != nil {
			if pg, ok := groupOf[m.Reference.MessageID]; ok {
				g = pg
			}
		}
		if g < 0 && len(groups) > 0 {
			cur := len(groups) - 1
			starter := msgs[groups[cur][0]]
			within := m.Timestamp.Sub(last[cur]) <= gap
			longPost := m.Author.ID != starter.Author.ID && m.Reference == nil && utf8.RuneCountInString(strings.TrimSpace(m.Content)) >= cpLongPostRunes
			if within && !longPost {
				g = cur
			}
		}
		if g < 0 {
			groups = append(groups, nil)
			last = append(last, m.Timestamp)
			g = len(groups) - 1
		}
		groups[g] = append(groups[g], i)
		if m.Timestamp.After(last[g]) {
			last[g] = m.Timestamp
		}
		groupOf[m.ID] = g
	}
	return groups
}

// cpNormPath normalises an attachment URL or uploaded relative path for matching.
func cpNormPath(p string) string {
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimPrefix(p, "./")
	return strings.ToLower(p)
}

// cpMatchMedia finds the uploaded file for an attachment: a path ending in the
// attachment's relative path, else a unique file with the same base name.
func cpMatchMedia(files map[string]*cpUpload, attachmentURL, fileName string) *cpUpload {
	want := cpNormPath(attachmentURL)
	if strings.HasPrefix(want, "http://") || strings.HasPrefix(want, "https://") {
		want = ""
	}
	if want != "" {
		for p, f := range files {
			if p == want || strings.HasSuffix(p, "/"+want) {
				return f
			}
		}
	}
	base := path.Base(firstNonEmpty(want, cpNormPath(fileName)))
	var found *cpUpload
	for p, f := range files {
		if path.Base(p) == base {
			if found != nil {
				return nil // ambiguous
			}
			found = f
		}
	}
	return found
}

type cpDiscordAuthor struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname,omitempty"`
	Messages int    `json:"messages"`
	Me       bool   `json:"me"`
}

type cpDiscordGroup struct {
	ExternalKey  string    `json:"externalKey"`
	MomentID     uuid.UUID `json:"momentId"`
	Existing     bool      `json:"existing"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt"`
	Starter      string    `json:"starter"`
	Excerpt      string    `json:"excerpt"`
	Messages     int       `json:"messages"`
	NewMessages  int       `json:"newMessages"`
	Replies      int       `json:"replies"`
	Authors      []string  `json:"authors"`
	Attachments  int       `json:"attachments"`
	MediaMatched int       `json:"mediaMatched"`
}

type cpDiscordReport struct {
	DryRun   bool              `json:"dryRun"`
	Channel  string            `json:"channel"`
	Guild    string            `json:"guild"`
	Kind     string            `json:"kind"`
	GapHours float64           `json:"gapHours"`
	Timezone string            `json:"timezone"`
	Authors  []cpDiscordAuthor `json:"authors"`
	Groups   []cpDiscordGroup  `json:"groups"`
	Totals   map[string]int    `json:"totals"`
	Warnings []string          `json:"warnings"`
}

func (app *application) cpImportDiscord(w http.ResponseWriter, r *http.Request) {
	form, err := cpReadMultipart(w, r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// The export JSON comes as a part named "export" or inside a zip ("archive").
	var exportBody []byte
	media := map[string]*cpUpload{}
	for _, f := range form.Files {
		switch {
		case f.Field == "export":
			exportBody = f.Body
		case f.Field == "archive" || strings.HasSuffix(strings.ToLower(f.FileName), ".zip"):
			body, files, err := cpUnzipExport(f.Body, cpZipTotalLimit)
			if err != nil {
				writeError(w, 400, err.Error())
				return
			}
			if exportBody == nil {
				exportBody = body
			}
			for k, v := range files {
				media[k] = v
			}
		default:
			media[cpNormPath(firstNonEmpty(f.FileName, f.Field))] = f
		}
	}
	if exportBody == nil {
		writeError(w, 400, "send the DiscordChatExporter JSON as the \"export\" part, or a zip of the export folder as \"archive\"")
		return
	}
	var export dceExport
	if err := json.Unmarshal(exportBody, &export); err != nil || export.Channel.ID == "" {
		writeError(w, 422, "that file is not a DiscordChatExporter JSON export of one channel")
		return
	}
	// Names of media files the browser holds but did not send in this request
	// (dry runs send names only, real imports send files in batches).
	for _, name := range func() []string {
		var names []string
		_ = json.Unmarshal([]byte(form.Values["mediaManifest"]), &names)
		return names
	}() {
		if p := cpNormPath(name); media[p] == nil && len(media) < 50000 {
			media[p] = &cpUpload{Field: "manifest-only", FileName: name}
		}
	}
	opts := cpDiscordOptions{Kind: firstNonEmpty(form.Values["kind"], "conversation"), Me: strings.TrimSpace(form.Values["me"]), DryRun: form.Values["dryRun"] == "1" || r.URL.Query().Get("dryRun") == "1", GapHours: 3}
	if !cpKinds[opts.Kind] {
		writeError(w, 422, "kind must be conversation, dream, idea or quote")
		return
	}
	if v := form.Values["gapHours"]; v != "" {
		g, err := strconv.ParseFloat(v, 64)
		if err != nil || g <= 0 || g > 24*30 {
			writeError(w, 422, "gapHours must be between 0 and 720")
			return
		}
		opts.GapHours = g
	}
	if opts.Timezone, err = cpTimezone(form.Values["timezone"]); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if opts.ChannelLabel, err = cpOneLine(form.Values["channelLabel"], 200, "channelLabel"); err != nil {
		writeError(w, 422, err.Error())
		return
	}
	user, ok := app.cpUser(w, r)
	if !ok {
		return
	}
	rep, err := app.cpApplyDiscord(r.Context(), user, &export, media, opts)
	if err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

// cpZipTotalLimit caps what one zip may expand to (a zip bomb guard); each
// entry is also capped (media at 30 MB, the export JSON at 32 MB).
var cpZipTotalLimit int64 = 512 << 20

// cpUnzipExport returns the channel export (the shallowest .json) and the
// media files of a zipped DiscordChatExporter folder.
func cpUnzipExport(data []byte, totalLimit int64) ([]byte, map[string]*cpUpload, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, errors.New("the zip archive is unreadable")
	}
	media := map[string]*cpUpload{}
	var total int64
	read := func(zf *zip.File, limit int64) ([]byte, error) {
		if int64(zf.UncompressedSize64) > limit {
			return nil, nil // skipped: larger than any file we keep
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, nil
		}
		defer rc.Close()
		body, err := cpReadAll(rc, limit)
		if err != nil {
			return nil, nil // it lied about its size; skip it
		}
		total += int64(len(body))
		if total > totalLimit {
			return nil, fmt.Errorf("the zip expands to more than %d MB; export a smaller range or use the folder picker", totalLimit>>20)
		}
		return body, nil
	}
	var jsonFiles []*zip.File
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || strings.HasPrefix(path.Base(zf.Name), ".") || strings.Contains(zf.Name, "__MACOSX") {
			continue
		}
		if strings.HasSuffix(strings.ToLower(zf.Name), ".json") {
			jsonFiles = append(jsonFiles, zf)
			continue
		}
		body, err := read(zf, cpAudioLimit)
		if err != nil {
			return nil, nil, err
		}
		if body != nil {
			media[cpNormPath(zf.Name)] = &cpUpload{Field: "media", FileName: zf.Name, Body: body}
		}
	}
	// The channel export is the shallowest JSON file in the archive.
	sort.Slice(jsonFiles, func(i, j int) bool {
		di, dj := strings.Count(jsonFiles[i].Name, "/"), strings.Count(jsonFiles[j].Name, "/")
		return di < dj || (di == dj && jsonFiles[i].Name < jsonFiles[j].Name)
	})
	var export []byte
	if len(jsonFiles) > 0 {
		if export, err = read(jsonFiles[0], cpImportLimit); err != nil {
			return nil, nil, err
		}
	}
	return export, media, nil
}

type cpDiscordOptions struct {
	Kind, Me, Timezone, ChannelLabel string
	GapHours                         float64
	DryRun                           bool
}

func cpDiscordAttachmentKind(a dceAttachment) string {
	ext := strings.ToLower(path.Ext(firstNonEmpty(a.FileName, a.URL)))
	if i := strings.IndexAny(ext, "?#"); i >= 0 {
		ext = ext[:i]
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".oga", ".opus", ".weba":
		return "audio"
	}
	return ""
}

func (app *application) cpApplyDiscord(ctx context.Context, user uuid.UUID, export *dceExport, media map[string]*cpUpload, opts cpDiscordOptions) (*cpDiscordReport, error) {
	rep := &cpDiscordReport{DryRun: opts.DryRun, Channel: export.Channel.Name, Guild: export.Guild.Name, Kind: opts.Kind, GapHours: opts.GapHours, Timezone: opts.Timezone,
		Authors: []cpDiscordAuthor{}, Groups: []cpDiscordGroup{}, Totals: map[string]int{}, Warnings: []string{}}
	// Keep real messages (not joins, pins, calls), oldest first.
	var msgs []dceMessage
	for _, m := range export.Messages {
		if m.ID == "" || m.Timestamp.IsZero() {
			continue
		}
		if m.Type != "" && m.Type != "Default" && m.Type != "Reply" && m.Type != "ThreadStarterMessage" {
			rep.Totals["skippedSystem"]++
			continue
		}
		if strings.TrimSpace(m.Content) == "" && len(m.Attachments) == 0 {
			rep.Totals["skippedEmpty"]++
			continue
		}
		msgs = append(msgs, m)
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Timestamp.Before(msgs[j].Timestamp) })
	rep.Totals["messages"] = len(msgs)
	authors := map[string]*cpDiscordAuthor{}
	var authorOrder []string
	for _, m := range msgs {
		a := authors[m.Author.ID]
		if a == nil {
			a = &cpDiscordAuthor{ID: m.Author.ID, Name: m.Author.Name, Nickname: m.Author.Nickname, Me: m.Author.ID == opts.Me}
			authors[m.Author.ID] = a
			authorOrder = append(authorOrder, m.Author.ID)
		}
		a.Messages++
	}
	for _, id := range authorOrder {
		rep.Authors = append(rep.Authors, *authors[id])
	}
	groups := cpGroupDiscord(msgs, time.Duration(opts.GapHours*float64(time.Hour)))
	rep.Totals["groups"] = len(groups)

	store := app.inspirationObjects()
	var newObjects []string
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

	// People come from authors (the owner, if chosen, speaks as "me").
	people := map[string]uuid.UUID{}
	person := func(a dceAuthor) (uuid.UUID, error) {
		if id, ok := people[a.ID]; ok {
			return id, nil
		}
		key := "discord:" + a.ID
		name := firstNonEmpty(strings.TrimSpace(a.Nickname), strings.TrimSpace(a.Name), "Discord user "+a.ID)
		if utf8.RuneCountInString(name) > 120 {
			name = string([]rune(name)[:120])
		}
		aliases, _ := cpCleanAliases([]string{a.Name, a.Nickname})
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM cp_people WHERE user_id=$1 AND person_key=$2`, user, key).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			// The same friend may already be here from another source.
			id, err = cpPersonByName(ctx, tx, user, name, a.Name)
			if err == nil && id != uuid.Nil {
				_, err = tx.Exec(ctx, `UPDATE cp_people SET person_key=coalesce(person_key,$2),aliases=ARRAY(SELECT DISTINCT x FROM unnest(aliases||$3::text[]) x ORDER BY x) WHERE id=$1`, id, key, aliases)
				people[a.ID] = id
				return id, err
			}
			if err != nil {
				return uuid.Nil, err
			}
			id = uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO cp_people(id,user_id,person_key,display_name,imported_name,aliases) VALUES($1,$2,$3,$4,$4,$5)`, id, user, key, name, aliases)
			rep.Totals["peopleCreated"]++
		} else if err == nil {
			_, err = tx.Exec(ctx, `UPDATE cp_people SET aliases=ARRAY(SELECT DISTINCT x FROM unnest(aliases||$2::text[]) x ORDER BY x) WHERE id=$1`, id, aliases)
		}
		people[a.ID] = id
		return id, err
	}
	detail := opts.ChannelLabel
	if detail == "" {
		detail = "#" + export.Channel.Name
		if export.Guild.Name != "" {
			detail += " · " + export.Guild.Name
		}
	}
	if utf8.RuneCountInString(detail) > 300 {
		detail = string([]rune(detail)[:300])
	}
	byID := map[string]dceMessage{}
	for _, m := range msgs {
		byID[m.ID] = m
	}
	for _, g := range groups {
		first := msgs[g[0]]
		key := "discord:" + export.Channel.ID + ":" + first.ID
		info := cpDiscordGroup{ExternalKey: key, StartedAt: first.Timestamp, EndedAt: msgs[g[len(g)-1]].Timestamp, Starter: firstNonEmpty(first.Author.Nickname, first.Author.Name),
			Messages: len(g), Authors: []string{}}
		excerpt := strings.TrimSpace(first.Content)
		if utf8.RuneCountInString(excerpt) > 200 {
			excerpt = string([]rune(excerpt)[:200]) + "…"
		}
		info.Excerpt = excerpt
		ids := make([]string, len(g))
		seenAuthor := map[string]bool{}
		for k, i := range g {
			m := msgs[i]
			ids[k] = "discord:" + m.ID
			if m.Reference != nil {
				info.Replies++
			}
			if !seenAuthor[m.Author.ID] {
				seenAuthor[m.Author.ID] = true
				info.Authors = append(info.Authors, firstNonEmpty(m.Author.Nickname, m.Author.Name))
			}
			for _, a := range m.Attachments {
				if cpDiscordAttachmentKind(a) == "" {
					continue
				}
				info.Attachments++
				if cpMatchMedia(media, a.URL, a.FileName) != nil {
					info.MediaMatched++
				}
			}
		}
		// Target Moment: wherever an earlier import put any of these messages,
		// else this group's external key, else a new Moment.
		var moment uuid.UUID
		err := tx.QueryRow(ctx, `SELECT l.moment_id FROM cp_lines l WHERE l.user_id=$1 AND l.external_id=ANY($2) ORDER BY l.said_at LIMIT 1`, user, ids).Scan(&moment)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `SELECT id FROM cp_moments WHERE user_id=$1 AND external_key=$2`, user, key).Scan(&moment)
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			moment = uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO cp_moments(id,user_id,kind,occurred_at,ended_at,timezone,source,source_detail,external_key,imported_from)
 VALUES($1,$2,$3,$4,$5,$6,'discord',$7,$8,'discord')`, moment, user, opts.Kind, info.StartedAt, info.EndedAt, opts.Timezone, detail, key)
			rep.Totals["momentsCreated"]++
		case err == nil:
			info.Existing = true
			rep.Totals["momentsUpdated"]++
		}
		if err != nil {
			return nil, err
		}
		info.MomentID = moment
		roles := map[uuid.UUID]string{}
		var roleOrder []uuid.UUID
		for _, i := range g {
			m := msgs[i]
			ext := "discord:" + m.ID
			var where uuid.UUID
			err := tx.QueryRow(ctx, `SELECT moment_id FROM cp_lines WHERE user_id=$1 AND external_id=$2`, user, ext).Scan(&where)
			exists := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			if exists && where != moment {
				rep.Totals["messagesElsewhere"]++
				continue
			}
			me := opts.Me != "" && m.Author.ID == opts.Me
			var speaker *uuid.UUID
			if !me {
				pid, err := person(m.Author)
				if err != nil {
					return nil, err
				}
				speaker = &pid
				if _, ok := roles[pid]; !ok {
					roles[pid] = "sender"
					roleOrder = append(roleOrder, pid)
				}
			}
			for _, mention := range m.Mentions {
				if mention.ID == "" || mention.ID == opts.Me || mention.ID == m.Author.ID {
					continue
				}
				pid, err := person(mention)
				if err != nil {
					return nil, err
				}
				if _, ok := roles[pid]; !ok {
					roles[pid] = "mentioned"
					roleOrder = append(roleOrder, pid)
				}
			}
			meta := map[string]any{}
			if m.TimestampEdited != nil {
				meta["edited"] = true
			}
			if m.IsPinned {
				meta["pinned"] = true
			}
			if m.Reference != nil && m.Reference.MessageID != "" {
				meta["replyTo"] = m.Reference.MessageID
				if parent, ok := byID[m.Reference.MessageID]; ok {
					meta["replyToName"] = firstNonEmpty(parent.Author.Nickname, parent.Author.Name)
				}
			}
			if len(m.Reactions) > 0 {
				var reactions []map[string]any
				for _, rc := range m.Reactions {
					reactions = append(reactions, map[string]any{"emoji": firstNonEmpty(rc.Emoji.Name, rc.Emoji.Code), "count": rc.Count})
				}
				meta["reactions"] = reactions
			}
			metaJSON, _ := json.Marshal(meta)
			// Attachments become artifacts; the first image anchors the line.
			var lineArtifact *uuid.UUID
			for _, a := range m.Attachments {
				kind := cpDiscordAttachmentKind(a)
				if kind == "" {
					rep.Totals["attachmentsSkipped"]++
					continue
				}
				akey := "discord-att-" + firstNonEmpty(a.ID, m.ID+"-"+cpCleanFileName(a.FileName))
				if len(akey) > 200 {
					akey = akey[:200]
				}
				var aid uuid.UUID
				var stored bool
				err := tx.QueryRow(ctx, `SELECT id,object_name IS NOT NULL FROM cp_artifacts WHERE moment_id=$1 AND artifact_key=$2`, moment, akey).Scan(&aid, &stored)
				found := err == nil
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err
				}
				if !found {
					aid = uuid.New()
					src := ""
					if strings.HasPrefix(a.URL, "http") {
						src = a.URL
					}
					if _, err := tx.Exec(ctx, `INSERT INTO cp_artifacts(id,moment_id,user_id,artifact_key,position,kind,original_name,source_url)
 VALUES($1,$2,$3,$4,(SELECT coalesce(max(position)+1,0) FROM cp_artifacts WHERE moment_id=$2),$5,$6,$7)`, aid, moment, user, akey, kind, cpCleanFileName(a.FileName), src); err != nil {
						return nil, err
					}
				}
				if kind == "image" && lineArtifact == nil {
					id := aid
					lineArtifact = &id
				}
				if stored {
					rep.Totals["mediaKept"]++
					continue
				}
				up := cpMatchMedia(media, a.URL, a.FileName)
				if up == nil || up.Body == nil {
					rep.Totals["mediaMissing"]++
					continue
				}
				md, err := cpValidateMedia(up.Body, "", nil, nil)
				if err != nil || md.Kind != kind {
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: not a valid %s file", a.FileName, kind))
					rep.Totals["mediaInvalid"]++
					continue
				}
				name := cpObjectName(user, moment, aid, md)
				if !opts.DryRun {
					if err := cpPutMedia(ctx, store, name, user, moment, aid, md); err != nil {
						return nil, err
					}
					newObjects = append(newObjects, name)
				}
				if _, err := tx.Exec(ctx, `UPDATE cp_artifacts SET object_name=$2,content_type=$3,size_bytes=$4,sha256=$5,width=$6,height=$7,updated_at=now() WHERE id=$1`,
					aid, name, md.ContentType, len(md.Body), md.SHA256, md.Width, md.Height); err != nil {
					return nil, err
				}
				rep.Totals["mediaStored"]++
			}
			if exists {
				// Re-import. A line the owner edited is left alone. Otherwise the
				// words change only when Discord's own edit is newer than the one
				// stored; reactions and pins refresh. Any change bumps the revision.
				if _, err := tx.Exec(ctx, `UPDATE cp_lines SET
 body=CASE WHEN $6::timestamptz IS NOT NULL AND (source_edited_at IS NULL OR $6::timestamptz>source_edited_at) THEN $3 ELSE body END,
 source_edited_at=CASE WHEN $6::timestamptz IS NOT NULL AND (source_edited_at IS NULL OR $6::timestamptz>source_edited_at) THEN $6::timestamptz ELSE source_edited_at END,
 meta=$4,artifact_id=coalesce(artifact_id,$5),revision=revision+1,updated_at=now()
 WHERE user_id=$1 AND external_id=$2 AND owner_touched_at IS NULL AND (
  meta IS DISTINCT FROM $4::jsonb OR (artifact_id IS NULL AND $5::uuid IS NOT NULL) OR
  ($6::timestamptz IS NOT NULL AND (source_edited_at IS NULL OR $6::timestamptz>source_edited_at) AND body IS DISTINCT FROM $3))`,
					user, ext, m.Content, string(metaJSON), lineArtifact, m.TimestampEdited); err != nil {
					return nil, err
				}
				rep.Totals["messagesKept"]++
				continue
			}
			label := firstNonEmpty(m.Author.Nickname, m.Author.Name)
			if utf8.RuneCountInString(label) > 120 {
				label = string([]rune(label)[:120])
			}
			body := m.Content
			if utf8.RuneCountInString(body) > 40000 {
				body = string([]rune(body)[:40000])
			}
			if _, err := tx.Exec(ctx, `INSERT INTO cp_lines(id,moment_id,user_id,line_key,external_id,position,speaker_is_me,speaker_person_id,speaker_label,body,said_at,meta,artifact_id,source_edited_at)
 VALUES($1,$2,$3,$4,$5,0,$6,$7,$8,$9,$10,$11,$12,$13)`, uuid.New(), moment, user, m.ID, ext, me, speaker, label, body, m.Timestamp, string(metaJSON), lineArtifact, m.TimestampEdited); err != nil {
				return nil, err
			}
			info.NewMessages++
			rep.Totals["messagesNew"]++
		}
		for k, pid := range roleOrder {
			if _, err := tx.Exec(ctx, `INSERT INTO cp_moment_people(moment_id,person_id,user_id,role,position,imported) VALUES($1,$2,$3,$4,$5,true) ON CONFLICT DO NOTHING`, moment, pid, user, roles[pid], k); err != nil {
				return nil, err
			}
		}
		// Keep lines in time order and the Moment's span in step with them.
		if _, err := tx.Exec(ctx, `UPDATE cp_lines l SET position=o.rn FROM (SELECT id,(row_number() OVER (ORDER BY said_at NULLS LAST,line_key))::int-1 AS rn FROM cp_lines WHERE moment_id=$1) o WHERE l.id=o.id AND l.position<>o.rn`, moment); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE cp_moments SET occurred_at=s.a,ended_at=s.b,updated_at=now() FROM (SELECT min(said_at) a,max(said_at) b FROM cp_lines WHERE moment_id=$1) s
 WHERE id=$1 AND s.a IS NOT NULL AND (occurred_at,ended_at) IS DISTINCT FROM (s.a,s.b)`, moment); err != nil {
			return nil, err
		}
		if len(rep.Groups) < 500 {
			rep.Groups = append(rep.Groups, info)
		}
	}
	if opts.DryRun {
		return rep, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	committed = true
	return rep, nil
}

// cpPersonByName finds the one person whose name or alias matches any of the
// given names (case-insensitive). Ambiguous or no matches return uuid.Nil.
func cpPersonByName(ctx context.Context, q cpDB, user uuid.UUID, names ...string) (uuid.UUID, error) {
	var clean []string
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			clean = append(clean, n)
		}
	}
	if len(clean) == 0 {
		return uuid.Nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id FROM cp_people WHERE user_id=$1 AND (lower(display_name)=ANY($2) OR EXISTS(SELECT 1 FROM unnest(aliases) x WHERE lower(x)=ANY($2))) LIMIT 2`, user, clean)
	ids, err := cpCollect(rows, err, func(row pgx.Row) (uuid.UUID, error) {
		var id uuid.UUID
		return id, row.Scan(&id)
	})
	if err != nil || len(ids) != 1 {
		return uuid.Nil, err
	}
	return ids[0], nil
}
