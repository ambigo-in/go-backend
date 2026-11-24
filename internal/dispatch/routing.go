package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"ambigo-backend/internal/logger"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"
	"ambigo-backend/internal/ride"

	"github.com/sony/gobreaker"
)

// RouteClient handles communication with Google Routes API
type RouteClient struct {
	APIKey  string
	APIURL  string
	Client  *http.Client
	breaker *gobreaker.CircuitBreaker
}

func newRoutesBreaker() *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "routes",
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

func NewRouteClient(apiKey, apiURL string) *RouteClient {
	return &RouteClient{
		APIKey: apiKey,
		APIURL: apiURL,
		Client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    20,
				IdleConnTimeout: 90 * time.Second,
			},
		},
		breaker: newRoutesBreaker(),
	}
}

type computeRoutesRequest struct {
	Origin            waypoint `json:"origin"`
	Destination       waypoint `json:"destination"`
	TravelMode        string   `json:"travelMode"` // "DRIVE"
	RoutingPreference string   `json:"routingPreference,omitempty"` // "TRAFFIC_AWARE"
}

type waypoint struct {
	Location locationPayload `json:"location"`
}

type locationPayload struct {
	LatLng latLngPayload `json:"latLng"`
}

type latLngPayload struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type computeRoutesResponse struct {
	Routes []struct {
		DistanceMeters int    `json:"distanceMeters"`
		Duration       string `json:"duration"` // e.g. "500s"
		Polyline       struct {
			EncodedPolyline string `json:"encodedPolyline"`
		} `json:"polyline"`
	} `json:"routes"`
}

func haversineRoute(originLat, originLng, destLat, destLng float64) *ride.Route {
	R := 6371.0
	dlat := (destLat - originLat) * (math.Pi / 180.0)
	dlon := (destLng - originLng) * (math.Pi / 180.0)
	a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(originLat*(math.Pi/180.0))*math.Cos(destLat*(math.Pi/180.0))*math.Sin(dlon/2)*math.Sin(dlon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	distance := R * c
	// Factor in realistic urban road detour index (~1.35x) and average ambulance transit speed (38 km/h)
	// so that circuit-breaker fallbacks remain consistent with Google Routes API.
	roadDistance := distance * 1.35
	durationSeconds := int((roadDistance / 38.0) * 3600.0)
	return &ride.Route{
		DistanceKm:      roadDistance,
		DurationSeconds: durationSeconds,
	}
}

// CalculateETA uses Google Routes API to find the exact driving ETA and distance
func (rc *RouteClient) CalculateETA(ctx context.Context, originLat, originLng, destLat, destLng float64) (*ride.Route, error) {
	if rc.APIKey == "" {
		return haversineRoute(originLat, originLng, destLat, destLng), nil
	}

	// Fallback to haversine when circuit breaker is open - without calling Google
	if rc.breaker != nil && rc.breaker.State() == gobreaker.StateOpen {
		logger.Log.Warn().Str("client", "routes").Msg("circuit breaker open, falling back to haversine without calling Google")
		return haversineRoute(originLat, originLng, destLat, destLng), nil
	}

	var result *ride.Route
	_, err := rc.breaker.Execute(func() (interface{}, error) {
		innerErr := retry.Do(ctx, retry.Default, func(ctx context.Context) error {
			reqBody := computeRoutesRequest{
				Origin: waypoint{
					Location: locationPayload{LatLng: latLngPayload{Latitude: originLat, Longitude: originLng}},
				},
				Destination: waypoint{
					Location: locationPayload{LatLng: latLngPayload{Latitude: destLat, Longitude: destLng}},
				},
				TravelMode:        "DRIVE",
				RoutingPreference: "TRAFFIC_AWARE",
			}

			jsonData, err := json.Marshal(reqBody)
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(ctx, "POST", rc.APIURL, bytes.NewBuffer(jsonData))
			if err != nil {
				return err
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Goog-Api-Key", rc.APIKey)
			req.Header.Set("X-Goog-FieldMask", "routes.duration,routes.distanceMeters,routes.polyline.encodedPolyline")

			start := time.Now()
			resp, err := rc.Client.Do(req)
			metrics.ObserveGoogleAPI(time.Since(start))
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("google routes api returned status: %d", resp.StatusCode)
			}

			var resData computeRoutesResponse
			if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
				return err
			}

			if len(resData.Routes) == 0 {
				return errors.New("no routes found")
			}

			r := resData.Routes[0]
			durationSeconds := 0
			fmt.Sscanf(r.Duration, "%ds", &durationSeconds)

			result = &ride.Route{
				DistanceKm:      float64(r.DistanceMeters) / 1000.0,
				DurationSeconds: durationSeconds,
				Polyline:        r.Polyline.EncodedPolyline,
			}
			return nil
		})
		return nil, innerErr
	})
	if err != nil {
		// Any upstream failure (open breaker, quota/billing 4xx-5xx, timeout,
		// malformed body) degrades to the haversine estimate instead of
		// surfacing a 500. Ride screens poll this endpoint for ETAs — a hard
		// failure there leaves every accepted/ongoing screen stuck on
		// "Calculating..." until Google recovers.
		logger.Log.Warn().Err(err).Str("client", "routes").Msg("routes api failed, falling back to haversine")
		return haversineRoute(originLat, originLng, destLat, destLng), nil
	}
	if result == nil {
		// Defensive: a nil result without an error would nil-deref callers.
		return haversineRoute(originLat, originLng, destLat, destLng), nil
	}
	return result, nil
}
