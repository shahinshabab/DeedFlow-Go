package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shahinshabab/deedflow-go/internal/ai"
	"github.com/shahinshabab/deedflow-go/internal/config"
	"github.com/shahinshabab/deedflow-go/internal/db"
	"github.com/shahinshabab/deedflow-go/internal/deeds"
	"github.com/shahinshabab/deedflow-go/internal/events"
	"github.com/shahinshabab/deedflow-go/internal/models"
	"github.com/shahinshabab/deedflow-go/internal/store"
	"github.com/shahinshabab/deedflow-go/internal/testdb"
)

func testDB(t *testing.T) *db.DB {
	return testdb.Open(t)
}

func TestProcessDeedPDF(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	st := store.New(d)

	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]any{
			"giver":              []map[string]any{{"name": "Anil Kumar", "age": "40", "pin": "679307"}},
			"recipient":          []map[string]any{{"name": "Latha Devi"}},
			"previous_documents": []map[string]any{{"document_type": "x", "year": 2019}},
			"other_details":      map[string]any{"document_price": 125000, "fair_value": 82500, "deed_type": "തീരാധാരം"},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}}}})
	}))
	defer aiSrv.Close()

	media := t.TempDir()
	pdf, err := os.ReadFile("testdata/deed.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(media, "deeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "deeds", "t.pdf"), pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{MediaRoot: media, OpenAIAPIKey: "k", OpenAIModel: "m"}
	client := ai.New(cfg)
	client.OpenAIURL = aiSrv.URL

	u := &models.User{Username: "pipeline-test-" + store.NewUUID()[:8], IsActive: true}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(ctx, `DELETE FROM auth_user WHERE id = $1`, u.ID) })
	src := "deeds/t.pdf"
	deed := &models.Deed{OwnerID: u.ID, Status: models.StatusExtracting, SourcePDF: &src}
	if err := st.CreateDeed(ctx, deed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Pool.Exec(ctx, `DELETE FROM deeds_deed WHERE id = $1`, deed.ID) })

	env := &Env{Cfg: cfg, Store: st, Hub: events.NewHub(d, slog.Default()), AI: client, Loc: time.UTC}
	if err := env.ProcessDeedPDF(ctx, deed.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.DeedByID(ctx, deed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StatusReadyForReview || got.Progress != 100 || got.Title != "Deed for Anil Kumar" {
		t.Fatalf("deed after processing: %+v", got)
	}
	data, err := st.LoadDeedData(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Executants) != 1 || len(data.Claimants) != 1 || data.Presenter == nil || data.Registration == nil {
		t.Fatalf("rows: %d %d %v %v", len(data.Executants), len(data.Claimants), data.Presenter, data.Registration)
	}
	if len(data.Schedule) == 0 {
		t.Fatalf("no schedule rows extracted from the table")
	}
	t.Logf("schedule rows: %d, first: district=%q old_survey=%q price=%v", len(data.Schedule), data.Schedule[0].District, data.Schedule[0].OldSurvey, data.Schedule[0].Price)
	_ = deeds.DefaultFilingSheets
}
