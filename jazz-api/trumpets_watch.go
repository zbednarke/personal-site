package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed trumpets_sources.json
var trumpetSourceSeedJSON []byte

type trumpetSource struct {
	Domain            string     `json:"domain"`
	Name              string     `json:"name"`
	Geography         string     `json:"geography"`
	Specialty         string     `json:"specialty"`
	FirstSeen         *time.Time `json:"firstSeen"`
	LastSearched      *time.Time `json:"lastSearched"`
	LastLiveCheck     *time.Time `json:"lastLiveCheck"`
	LastUseful        *time.Time `json:"lastUseful"`
	Queries           int        `json:"queries"`
	SuccessfulQueries int        `json:"successfulQueries"`
	PagesOpened       int        `json:"pagesOpened"`
	VerifiedOffers    int        `json:"verifiedOffers"`
	StaleResults      int        `json:"staleResults"`
	LastStatus        string     `json:"lastStatus"`
	LastNote          string     `json:"lastNote"`
}

var trumpetDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,250}[a-z0-9])?\.[a-z]{2,63}$`)

func validateTrumpetSource(s trumpetSourceCheck) error {
	if s.Domain != "" && (!trumpetDomainPattern.MatchString(s.Domain) || strings.Contains(s.Domain, "..") || strings.HasSuffix(s.Domain, ".local") || strings.HasSuffix(s.Domain, ".internal")) {
		return errors.New("invalid source domain")
	}
	if len(s.Query) > 10000 || len(s.Geography) > 200 || len(s.Specialty) > 200 || s.PagesOpened < 0 || s.PagesOpened > 10000 || s.VerifiedOffers < 0 || s.VerifiedOffers > s.PagesOpened || s.StaleResults < 0 || s.StaleResults > s.PagesOpened {
		return errors.New("invalid source evidence counts")
	}
	return nil
}
func (app *application) trumpetSourceUniverse(ctx context.Context, user uuid.UUID) ([]trumpetSource, error) {
	seeds := []trumpetSource{}
	if err := json.Unmarshal(trumpetSourceSeedJSON, &seeds); err != nil {
		return nil, err
	}
	byDomain := map[string]trumpetSource{}
	for _, s := range seeds {
		s.LastStatus = "unsearched"
		byDomain[s.Domain] = s
	}
	rows, err := app.db.Query(ctx, `SELECT domain,name,geography,specialty,first_seen,last_searched,last_live_check,last_useful,queries,successful_queries,pages_opened,verified_offers,stale_results,last_status,last_note FROM trumpet_sources WHERE user_id=$1`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s trumpetSource
		if err = rows.Scan(&s.Domain, &s.Name, &s.Geography, &s.Specialty, &s.FirstSeen, &s.LastSearched, &s.LastLiveCheck, &s.LastUseful, &s.Queries, &s.SuccessfulQueries, &s.PagesOpened, &s.VerifiedOffers, &s.StaleResults, &s.LastStatus, &s.LastNote); err != nil {
			return nil, err
		}
		byDomain[s.Domain] = s
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := make([]trumpetSource, 0, len(byDomain))
	for _, s := range byDomain {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out, nil
}
func accumulateTrumpetSources(ctx context.Context, tx pgx.Tx, user uuid.UUID, sources []trumpetSourceCheck) error {
	for _, s := range sources {
		if s.Domain == "" {
			continue
		}
		if s.Geography == "" {
			s.Geography = "International / unknown"
		}
		if s.Specialty == "" {
			s.Specialty = "Discovered source"
		}
		_, err := tx.Exec(ctx, `INSERT INTO trumpet_sources(user_id,domain,name,geography,specialty,last_searched,last_live_check,last_useful,queries,successful_queries,pages_opened,verified_offers,stale_results,last_status,last_note)
  VALUES($1,$2,$3,$4,$5,CASE WHEN $6<>'' THEN now() END,CASE WHEN $7>0 THEN now() END,CASE WHEN $8>0 THEN now() END,CASE WHEN $6<>'' THEN 1 ELSE 0 END,CASE WHEN $6<>'' AND $9='checked' THEN 1 ELSE 0 END,$7,$8,$10,$9,$11)
  ON CONFLICT(user_id,domain) DO UPDATE SET
  last_searched=COALESCE(EXCLUDED.last_searched,trumpet_sources.last_searched),last_live_check=COALESCE(EXCLUDED.last_live_check,trumpet_sources.last_live_check),last_useful=COALESCE(EXCLUDED.last_useful,trumpet_sources.last_useful),
  queries=trumpet_sources.queries+EXCLUDED.queries,successful_queries=trumpet_sources.successful_queries+EXCLUDED.successful_queries,pages_opened=trumpet_sources.pages_opened+EXCLUDED.pages_opened,verified_offers=trumpet_sources.verified_offers+EXCLUDED.verified_offers,stale_results=trumpet_sources.stale_results+EXCLUDED.stale_results,last_status=EXCLUDED.last_status,last_note=EXCLUDED.last_note,
  geography=CASE WHEN EXCLUDED.geography='International / unknown' THEN trumpet_sources.geography ELSE EXCLUDED.geography END,specialty=CASE WHEN EXCLUDED.specialty='Discovered source' THEN trumpet_sources.specialty ELSE EXCLUDED.specialty END`, user, s.Domain, s.Source, s.Geography, s.Specialty, s.Query, s.PagesOpened, s.VerifiedOffers, s.Status, s.StaleResults, s.Note)
		if err != nil {
			return err
		}
	}
	return nil
}
func significantTrumpetPrice(old, new *float64, oldCurrency, newCurrency string) bool {
	if old == nil || new == nil || oldCurrency != newCurrency || *old <= 0 {
		return false
	}
	fraction := (*old - *new) / *old
	return fraction >= 0.03 || fraction <= -0.15
}
func trumpetSnapshotChanges(old, new json.RawMessage, oldDescription, newDescription string) string {
	a, b := map[string]any{}, map[string]any{}
	_ = json.Unmarshal(old, &a)
	_ = json.Unmarshal(new, &b)
	changes := []string{}
	if identityPart(oldDescription) != identityPart(newDescription) {
		changes = append(changes, "Description / documentation updated")
	}
	if d, ok := a["details"]; ok {
		aa, _ := json.Marshal(d)
		bb, _ := json.Marshal(b["details"])
		if string(aa) != string(bb) {
			changes = append(changes, "Condition, configuration or provenance updated")
		}
	}
	return strings.Join(changes, "; ")
}

// Serial-confirmed promotion retains offers and feedback instead of failing
// on the instrument identity key that belongs to an existing verified offer.
func mergeTrumpetHorn(ctx context.Context, tx pgx.Tx, user, from, to uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO trumpet_feedback(horn_id,user_id,rating,interest_state,notes,favorite,favored_attributes,disliked_attributes)
 SELECT $3,user_id,rating,interest_state,notes,favorite,favored_attributes,disliked_attributes FROM trumpet_feedback WHERE horn_id=$2 AND user_id=$1
 ON CONFLICT(horn_id) DO UPDATE SET favorite=trumpet_feedback.favorite OR EXCLUDED.favorite,
 notes=CASE WHEN EXCLUDED.notes='' OR trumpet_feedback.notes=EXCLUDED.notes THEN trumpet_feedback.notes WHEN trumpet_feedback.notes='' THEN EXCLUDED.notes ELSE trumpet_feedback.notes||E'\n\nRelated listing notes: '||EXCLUDED.notes END,
 favored_attributes=trumpet_feedback.favored_attributes||EXCLUDED.favored_attributes,disliked_attributes=trumpet_feedback.disliked_attributes||EXCLUDED.disliked_attributes`, user, from, to)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE trumpet_listings SET horn_id=$3 WHERE user_id=$1 AND horn_id=$2`, user, from, to)
	return err
}

// Explicit sentiment phrases are bounded ranking evidence. Private notes are
// interpreted locally and are never copied into public search queries.
func trumpetNotePreferences(listings []trumpetListing) map[string]any {
	favored, disliked := map[string]int{}, map[string]int{}
	makerWeights := map[string]int{}
	prices := []map[string]any{}
	seen := map[string]bool{}
	aliases := map[string][]string{
		"raw brass": {"raw brass"}, "raw nickel": {"raw nickel"}, "patina": {"patina", "aged brass"}, "gold plate": {"gold plate", "gold plating", "gold finish"}, "engraving": {"engraving", "engraved"},
		"engineering": {"engineering", "weird construction", "unusual construction"}, "upswept": {"upswept"}, "provenance": {"provenance", "documentation", "history"}, "one-off": {"one-off", "one off"}, "custom": {"custom"}, "mixed metals": {"mixed metals"}, "black hardware": {"black hardware"}, "unusual bell": {"unusual bell", "bell geometry"},
	}
	positive := regexp.MustCompile(`\b(love|like|cool|great|prefer|more|amazing|interesting|beautiful|favorite|favourite)\b`)
	negative := regexp.MustCompile(`\b(dislike|hate|boring|avoid|not interested|not for me|don't like|do not like|don't love|do not love|no more)\b`)
	expensive := regexp.MustCompile(`\b(too expensive|too pricey|overpriced|too much|can't afford|cannot afford)\b`)
	split := regexp.MustCompile(`[.!?;\n]|\bbut\b|\bhowever\b`)
	for _, l := range listings {
		if seen[l.HornID] {
			continue
		}
		seen[l.HornID] = true
		note := identityPart(l.Feedback.Notes)
		if note == "" {
			continue
		}
		if expensive.MatchString(note) && l.Price != nil {
			prices = append(prices, map[string]any{"maker": l.Maker, "currency": l.Currency, "referencePrice": *l.Price, "guidance": "Style retained; prefer better value below this example"})
		}
		for _, clause := range split.Split(note, -1) {
			neg, pos := negative.MatchString(clause), positive.MatchString(clause)
			if !neg && !pos {
				continue
			}
			matched := false
			for term, words := range aliases {
				for _, word := range words {
					if strings.Contains(clause, word) {
						if neg {
							disliked[term]++
						} else {
							favored[term]++
						}
						matched = true
						break
					}
				}
			}
			if !matched && !expensive.MatchString(clause) {
				if neg {
					makerWeights[strings.ToLower(l.Maker)]--
				} else {
					makerWeights[strings.ToLower(l.Maker)]++
				}
			}
		}
	}
	return map[string]any{"favoredAttributes": favored, "dislikedAttributes": disliked, "makerWeights": makerWeights, "pricePreferences": prices, "interpretation": "Explicit sentiment phrases only; bounded ranking adjustments, never hard filters. Exploration remains reserved."}
}
func selectTrumpetAlerts(listings []trumpetListing, raw []json.RawMessage) []json.RawMessage {
	byID := map[string]trumpetListing{}
	for _, l := range listings {
		byID[l.ID] = l
	}
	type signal struct {
		raw   json.RawMessage
		horn  string
		score float64
	}
	signals := []signal{}
	for _, r := range raw {
		var e struct {
			ListingID string `json:"listingId"`
			Kind      string `json:"kind"`
		}
		if json.Unmarshal(r, &e) != nil {
			continue
		}
		l, ok := byID[e.ListingID]
		if !ok || l.Acquired || l.Status == "acquired" || l.VerificationState == "candidate" || l.Feedback.InterestState == "pass" {
			continue
		}
		score := l.SearchScore
		if l.Feedback.Favorite {
			score += 15
		}
		if l.Feedback.Rating != nil {
			score += float64(*l.Feedback.Rating-3) * 5
		}
		if e.Kind == "price drop" {
			score += 12
		}
		if e.Kind == "status change" || e.Kind == "details change" {
			score += 8
		}
		if score < 65 {
			continue
		}
		signals = append(signals, signal{r, l.HornID, score})
	}
	sort.SliceStable(signals, func(i, j int) bool { return signals[i].score > signals[j].score })
	out := []json.RawMessage{}
	seen := map[string]bool{}
	for _, s := range signals {
		if seen[s.horn] {
			continue
		}
		seen[s.horn] = true
		out = append(out, s.raw)
		if len(out) == 8 {
			break
		}
	}
	return out
}
