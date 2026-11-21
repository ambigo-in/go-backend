package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ambigo-backend/internal/logger"
)

const (
	// Control plane (dataset lookup/create). Create is v2, list is v1.
	defaultAxiomAPIBaseURL = "https://api.axiom.co"
	axiomDatasetKind       = "axiom:events:v1"
	axiomRetentionDays     = 30 // matches the 30-day hot-store window
)

// AxiomSink ships NDJSON batches to the dataset's edge ingest endpoint
// (POST {edgeDeploymentUrl}/v1/ingest/{datasetID}). The dataset is resolved by
// name at startup against the api.axiom.co control plane.
type AxiomSink struct {
	client     *http.Client
	apiBaseURL string
	token      string
	dataset    string

	datasetID string
	ingestURL string
}

func NewAxiomSink(token, dataset, apiBaseURL string) *AxiomSink {
	if apiBaseURL == "" {
		apiBaseURL = defaultAxiomAPIBaseURL
	}
	return &AxiomSink{
		client:     &http.Client{Timeout: 15 * time.Second},
		apiBaseURL: strings.TrimRight(apiBaseURL, "/"),
		token:      token,
		dataset:    dataset,
	}
}

func (a *AxiomSink) Name() string { return "axiom" }

func (a *AxiomSink) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.token)
}

type axiomDataset struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	EdgeDeploymentURL string `json:"edgeDeploymentUrl"`
}

// EnsureDataset resolves the dataset by name (creating it when missing) and
// stores the ingest URL used by WriteBatch. Fails when the token cannot list
// or create datasets.
func (a *AxiomSink) EnsureDataset(ctx context.Context) error {
	d, err := a.findDataset(ctx)
	if err != nil {
		return err
	}
	if d == nil {
		if err := a.createDataset(ctx); err != nil {
			return err
		}
		if d, err = a.findDataset(ctx); err != nil {
			return err
		}
		if d == nil {
			return fmt.Errorf("axiom: dataset %q not visible after create", a.dataset)
		}
	}
	if d.ID == "" || d.EdgeDeploymentURL == "" {
		return fmt.Errorf("axiom: dataset %q response missing id/edgeDeploymentUrl", a.dataset)
	}
	a.datasetID = d.ID
	a.ingestURL = strings.TrimRight(d.EdgeDeploymentURL, "/") + "/v1/ingest/" + d.ID
	return nil
}

// findDataset returns nil when the dataset does not exist.
func (a *AxiomSink) findDataset(ctx context.Context) (*axiomDataset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBaseURL+"/v1/datasets", nil)
	if err != nil {
		return nil, err
	}
	a.auth(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("axiom list datasets: status %d", resp.StatusCode)
	}
	var datasets []axiomDataset
	if err := json.NewDecoder(resp.Body).Decode(&datasets); err != nil {
		return nil, fmt.Errorf("axiom list datasets: %w", err)
	}
	for i := range datasets {
		if datasets[i].Name == a.dataset {
			return &datasets[i], nil
		}
	}
	return nil, nil
}

func (a *AxiomSink) createDataset(ctx context.Context) error {
	body, err := json.Marshal(map[string]interface{}{
		"name":          a.dataset,
		"description":   "Ambigo backend business events (30-day hot store)",
		"kind":          axiomDatasetKind,
		"retentionDays": axiomRetentionDays,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBaseURL+"/v2/datasets", bytes.NewReader(body))
	if err != nil {
		return err
	}
	a.auth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict: // another instance created it first; re-find resolves it
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		logger.Log.Error().Str("dataset", a.dataset).Int("status", resp.StatusCode).
			Msg("Axiom: API token cannot create the dataset — create it in the Axiom console or grant dataset-create permission")
		return fmt.Errorf("axiom create dataset %q: status %d (token lacks create permission?)", a.dataset, resp.StatusCode)
	default:
		msg := fmt.Sprintf("axiom create dataset %q: status %d", a.dataset, resp.StatusCode)
		if b, rerr := io.ReadAll(io.LimitReader(resp.Body, 512)); rerr == nil && len(b) > 0 {
			msg += ": " + string(b)
		}
		return fmt.Errorf("%s", msg)
	}
}

func (a *AxiomSink) WriteBatch(ctx context.Context, events [][]byte) error {
	if a.ingestURL == "" {
		return fmt.Errorf("axiom: dataset %q not resolved (EnsureDataset failed at startup)", a.dataset)
	}
	var body bytes.Buffer
	for _, ev := range events {
		body.Write(ev)
		body.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.ingestURL, &body)
	if err != nil {
		return err
	}
	a.auth(req)
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("axiom ingest: status %d", resp.StatusCode)
		if b, rerr := io.ReadAll(io.LimitReader(resp.Body, 512)); rerr == nil && len(b) > 0 {
			msg += ": " + string(b)
		}
		ingestErr := fmt.Errorf("%s", msg)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			logger.Log.Error().Err(ingestErr).Msg("Axiom auth rejected — check AXIOM_API_TOKEN")
		}
		return ingestErr
	}
	var result struct {
		Ingested uint64 `json:"ingested"`
		Failed   uint64 `json:"failed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("axiom ingest: unreadable response: %w", err)
	}
	if result.Failed > 0 {
		return fmt.Errorf("axiom ingest: %d of %d events rejected", result.Failed, len(events))
	}
	return nil
}
