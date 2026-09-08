package places

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"

	"github.com/sony/gobreaker"
)

// Place is a normalized hospital result returned by the Google Places API (New).
type Place struct {
	ID            string   `json:"id"`
	DisplayName   string   `json:"display_name"`
	FormattedAddr string   `json:"formatted_address"`
	Lat           float64  `json:"lat"`
	Lng           float64  `json:"lng"`
	Types         []string `json:"types,omitempty"`
}

// PlacesClient talks to the Google Places API (New) nearby-search and text-search endpoints.
// It mirrors the pattern of dispatch.RouteClient.
type PlacesClient struct {
	APIKey     string
	APIURL     string // searchNearby
	TextAPIURL string // searchText (derived from APIURL if empty)
	Client     *http.Client
	breaker    *gobreaker.CircuitBreaker
}

func newPlacesBreaker() *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "places",
		MaxRequests: 20,
		Interval:    60 * time.Second,
		Timeout:     30 * time.Second,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < 10 {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Log.Info().Str("client", name).Str("from", from.String()).Str("to", to.String()).Msg("circuit breaker state changed")
			var val float64
			switch to {
			case gobreaker.StateClosed:
				val = 0
			case gobreaker.StateOpen:
				val = 1
			case gobreaker.StateHalfOpen:
				val = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name).Set(val)
			metrics.CircuitBreakerTransitions.WithLabelValues(name, from.String(), to.String()).Inc()
		},
	})
}

func NewPlacesClient(apiKey, apiURL string) *PlacesClient {
	textURL := ""
	if apiURL != "" {
		if len(apiURL) >= len("searchNearby") && contains(apiURL, "searchNearby") {
			textURL = replaceSearchNearbyWithText(apiURL)
		} else {
			textURL = "https://places.googleapis.com/v1/places:searchText"
		}
	}
	return &PlacesClient{
		APIKey:     apiKey,
		APIURL:     apiURL,
		TextAPIURL: textURL,
		Client: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    20,
				IdleConnTimeout: 90 * time.Second,
			},
		},
		breaker: newPlacesBreaker(),
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func replaceSearchNearbyWithText(url string) string {
	// Replace searchNearby with searchText
	for i := 0; i <= len(url)-len("searchNearby"); i++ {
		if url[i:i+len("searchNearby")] == "searchNearby" {
			return url[:i] + "searchText" + url[i+len("searchNearby"):]
		}
	}
	return "https://places.googleapis.com/v1/places:searchText"
}

type searchNearbyRequest struct {
	IncludedTypes       []string `json:"includedTypes"`
	MaxResultCount      int      `json:"maxResultCount"`
	RankPreference      string   `json:"rankPreference"`
	LocationRestriction struct {
		Circle struct {
			Center struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"center"`
			Radius int64 `json:"radius"`
		} `json:"circle"`
	} `json:"locationRestriction"`
}

type searchNearbyResponse struct {
	Places []struct {
		ID          string `json:"id"`
		DisplayName struct {
			Text string `json:"text"`
		} `json:"displayName"`
		FormattedAddress string `json:"formattedAddress"`
		Location         struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		Types []string `json:"types"`
	} `json:"places"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

type textSearchRequest struct {
	TextQuery  string `json:"textQuery"`
	PageSize   int    `json:"pageSize,omitempty"`
	PageToken  string `json:"pageToken,omitempty"`
	LocationBias *struct {
		Circle struct {
			Center struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"center"`
			Radius float64 `json:"radius"`
		} `json:"circle"`
	} `json:"locationBias,omitempty"`
}

type textSearchResponse struct {
	Places []struct {
		ID          string `json:"id"`
		DisplayName struct {
			Text string `json:"text"`
		} `json:"displayName"`
		FormattedAddress string `json:"formattedAddress"`
		Location         struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		Types []string `json:"types"`
	} `json:"places"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

// SearchText returns hospitals matching textQuery within radiusM of the given coordinates,
// with pagination via pageToken. Returns page of places and nextPageToken. Handles breaker and retry.
func (p *PlacesClient) SearchText(ctx context.Context, textQuery string, lat, lng float64, radiusM int64, pageSize int, pageToken string) ([]Place, string, error) {
	if p.APIKey == "" {
		return nil, "", nil
	}
	if p.breaker != nil && p.breaker.State() == gobreaker.StateOpen {
		logger.Log.Warn().Str("client", "places").Str("query", textQuery).Msg("circuit breaker open, falling back to empty without calling Google")
		return []Place{}, "", nil
	}
	if pageSize <= 0 || pageSize > 20 {
		pageSize = 20
	}
	if p.TextAPIURL == "" {
		p.TextAPIURL = "https://places.googleapis.com/v1/places:searchText"
	}
	var result []Place
	var nextToken string
	_, err := p.breaker.Execute(func() (interface{}, error) {
		innerErr := retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			body := textSearchRequest{
				TextQuery: textQuery,
				PageSize:  pageSize,
				PageToken: pageToken,
			}
			bias := &struct {
				Circle struct {
					Center struct {
						Latitude  float64 `json:"latitude"`
						Longitude float64 `json:"longitude"`
					} `json:"center"`
					Radius float64 `json:"radius"`
				} `json:"circle"`
			}{}
			bias.Circle.Center.Latitude = lat
			bias.Circle.Center.Longitude = lng
			bias.Circle.Radius = float64(radiusM)
			body.LocationBias = bias

			jsonData, err := json.Marshal(body)
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(ctx, "POST", p.TextAPIURL, bytes.NewBuffer(jsonData))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Goog-Api-Key", p.APIKey)
			req.Header.Set("X-Goog-FieldMask", "places.id,places.displayName,places.formattedAddress,places.location,places.types,nextPageToken")

			start := time.Now()
			resp, err := p.Client.Do(req)
			metrics.ObserveGoogleAPI(time.Since(start))
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("google places text search api returned status: %d", resp.StatusCode)
			}
			var resData textSearchResponse
			if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
				return err
			}
			if len(resData.Places) == 0 {
				result = []Place{}
				nextToken = resData.NextPageToken
				return nil
			}
			result = make([]Place, 0, len(resData.Places))
			for _, rp := range resData.Places {
				result = append(result, Place{
					ID:            rp.ID,
					DisplayName:   rp.DisplayName.Text,
					FormattedAddr: rp.FormattedAddress,
					Lat:           rp.Location.Latitude,
					Lng:           rp.Location.Longitude,
					Types:         rp.Types,
				})
			}
			nextToken = resData.NextPageToken
			return nil
		})
		return nil, innerErr
	})
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) {
			logger.Log.Warn().Str("client", "places").Msg("circuit breaker open, fallback to empty")
			return []Place{}, "", nil
		}
		return nil, "", err
	}
	if result == nil {
		result = []Place{}
	}
	return result, nextToken, nil
}

// SearchNearby returns hospitals within radiusM of the given coordinates,
// sorted by distance. Returns nil, nil when the client has no API key.
func (p *PlacesClient) SearchNearby(ctx context.Context, lat, lng float64, radiusM int64, maxResults int) ([]Place, error) {
	if p.APIKey == "" {
		return nil, nil
	}

	// Fallback to empty without calling Google when circuit breaker is open
	if p.breaker != nil && p.breaker.State() == gobreaker.StateOpen {
		logger.Log.Warn().Str("client", "places").Msg("circuit breaker open, falling back to empty without calling Google")
		return []Place{}, nil
	}

	var result []Place
	_, err := p.breaker.Execute(func() (interface{}, error) {
		innerErr := retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			body := searchNearbyRequest{
				IncludedTypes:  []string{"hospital"},
				MaxResultCount: maxResults,
				RankPreference: "DISTANCE",
			}
			body.LocationRestriction.Circle.Center.Latitude = lat
			body.LocationRestriction.Circle.Center.Longitude = lng
			body.LocationRestriction.Circle.Radius = radiusM

			jsonData, err := json.Marshal(body)
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(ctx, "POST", p.APIURL, bytes.NewBuffer(jsonData))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Goog-Api-Key", p.APIKey)
			req.Header.Set("X-Goog-FieldMask", "places.id,places.displayName,places.formattedAddress,places.location,places.types")

			start := time.Now()
			resp, err := p.Client.Do(req)
			metrics.ObserveGoogleAPI(time.Since(start))
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("google places api returned status: %d", resp.StatusCode)
			}

			var resData searchNearbyResponse
			if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
				return err
			}

			if len(resData.Places) == 0 {
				result = []Place{}
				return nil
			}

			result = make([]Place, 0, len(resData.Places))
			for _, rp := range resData.Places {
				result = append(result, Place{
					ID:            rp.ID,
					DisplayName:   rp.DisplayName.Text,
					FormattedAddr: rp.FormattedAddress,
					Lat:           rp.Location.Latitude,
					Lng:           rp.Location.Longitude,
					Types:         rp.Types,
				})
			}
			return nil
		})
		return nil, innerErr
	})
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) {
			logger.Log.Warn().Str("client", "places").Msg("circuit breaker open, fallback to empty")
			return []Place{}, nil
		}
		return nil, err
	}
	if result == nil {
		return []Place{}, nil
	}
	return result, nil
}
