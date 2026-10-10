package main

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/http"
)

// Historical reference data only. No guessed prices, URLs, images, sellers or
// availability: the search runner must verify those facts before activating it.
func historicalTrumpets() []trumpetCandidate {
	type seed struct{ maker, model, finish, serial, rationale string }
	rows := []seed{
		{"Taylor", "Chicago 46 II / Harrelson-modified", "", "", "Purchased horn; exclude from discovery alerts. Useful for comparison, provenance, setup and resale."},
		{"Olds", "Super Recording", "", "27907", "1948 reference with known serial; verify provenance and current offer."},
		{"Harrelson", "MUSE", "raw brass", "", "Boutique engineering and raw-brass finish."},
		{"Taylor", "Chicago II one-off upswept", "raw brass", "", "One-off build and unusual upswept bell geometry."},
		{"Adams", "A4", "antique lacquer", "", "Professional A4 with antique finish."},
		{"Lawler", "C7", "", "", "Interesting boutique professional configuration."},
		{"Van Laar", "OIRAM II", "raw brass", "", "Unusual OIRAM geometry in raw brass."},
		{"Del Quadro", "Dorotea", "", "", "Distinctive boutique design."},
		{"Harrelson", "Bravura David Castro", "", "", "Artist-model provenance to verify."},
		{"Yamaha", "YTR-921X", "", "002", "Exceptional Yamaha reference, serial 002; not ordinary production inventory."},
		{"Schilke", "B5", "gold plate", "", "Full gold finish makes this specific offer interesting."},
		{"LOTUS", "Solo Max", "raw brass", "", "Distinctive professional design in raw brass."},
		{"AR Resonance", "Feroce", "raw nickel", "", "Boutique engineering and unusual raw-nickel finish."},
		{"Schilke", "S43HDL-F Faddis three-bell bundle", "", "", "Artist configuration with a three-bell bundle."},
		{"Schilke", "HC1", "", "", "Handcraft professional model."},
		{"Besson", "Meha", "", "", "Historical professional reference."},
		{"Olds", "Opera Premiere", "", "", "Unusual historical professional model."},
		{"Calicchio", "Historical listings (details unverified)", "", "", "Placeholder for notable prior finds; identify individual horns before treating as live offers."},
		{"Van Laar", "Unusual historical listings (details unverified)", "", "", "Placeholder for unusual prior configurations; identify individual horns during revalidation."},
		{"GERDT", "Lars Hjalt custom horns", "", "", "Custom artist-associated builds; verify exact instruments."},
		{"Selmer", "Concept TT", "", "", "Distinctive professional trumpet configuration."},
	}
	out := []trumpetCandidate{}
	for i, s := range rows {
		c := trumpetCandidate{Maker: s.maker, Model: s.model, SerialNumber: s.serial, Title: s.maker + " " + s.model, Source: "Historical search", Status: "stale", DiscoveryType: "newly discovered", SearchRationale: s.rationale, Details: hornDetails{Finish: s.finish}, Tags: []string{"historical", "unverified"}, SeedKey: s.maker + " / " + s.model}
		if s.finish != "" {
			c.Tags = append(c.Tags, s.finish)
		}
		if i == 0 {
			c.Status = "acquired"
			c.Tags = []string{"acquired", "Harrelson modified"}
			c.Details.Provenance = "User-purchased Taylor Chicago 46 II / Harrelson-modified Bb trumpet."
		}
		if i == 1 {
			year := 1948
			c.Details.Year = &year
		}
		out = append(out, c)
	}
	return out
}
func (app *application) seedTrumpets(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if err := decodeTrumpetJSON(w, r, &input); err != nil {
		writeError(w, 400, "expected empty JSON object")
		return
	}
	user, err := app.userID(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	tx, err := app.db.Begin(r.Context())
	if err != nil {
		app.serverError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = lockTrumpetOwner(r.Context(), tx, user); err != nil {
		app.serverError(w, err)
		return
	}
	added := 0
	for _, c := range historicalTrumpets() {
		var existing uuid.UUID
		err = tx.QueryRow(r.Context(), `SELECT id FROM trumpet_listings WHERE user_id=$1 AND seed_key=$2`, user, c.SeedKey).Scan(&existing)
		if err == nil {
			continue
		}
		if err != pgx.ErrNoRows {
			app.serverError(w, err)
			return
		}
		if _, err = upsertTrumpet(r.Context(), tx, user, c, nil); err != nil {
			app.serverError(w, err)
			return
		}
		added++
	}
	if err = tx.Commit(r.Context()); err != nil {
		app.serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"added": added})
}
