package dispatch

import (
	"context"
	"ambigo-backend/internal/metrics"
	"ambigo-backend/internal/retry"
	"ambigo-backend/internal/ride"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

// RouteClient handles communication with Google Routes API
type RouteClient struct {
	APIKey string
	APIURL string
	Client *http.Client
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
	}
}

type computeRoutesRequest struct {
	Origin      waypoint `json:"origin"`
	Destination waypoint `json:"destination"`
	TravelMode  string   `json:"travelMode"` // "DRIVE"
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

// CalculateETA uses Google Routes API to find the exact driving ETA and distance
func (rc *RouteClient) CalculateETA(ctx context.Context, originLat, originLng, destLat, destLng float64) (*ride.Route, error) {
	if rc.APIKey == "" {
		R := 6371.0
		dlat := (destLat - originLat) * (math.Pi / 180.0)
		dlon := (destLng - originLng) * (math.Pi / 180.0)
		a := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(originLat*(math.Pi/180.0))*math.Cos(destLat*(math.Pi/180.0))*math.Sin(dlon/2)*math.Sin(dlon/2)
		c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
		distance := R * c
		durationSeconds := int((distance / 40.0) * 3600.0)
		return &ride.Route{
			DistanceKm:      distance,
			DurationSeconds: durationSeconds,
		}, nil
	}

	var result *ride.Route
	err := retry.Do(ctx, retry.Default, func(ctx context.Context) error {
		reqBody := computeRoutesRequest{
			Origin: waypoint{
				Location: locationPayload{LatLng: latLngPayload{Latitude: originLat, Longitude: originLng}},
			},
			Destination: waypoint{
				Location: locationPayload{LatLng: latLngPayload{Latitude: destLat, Longitude: destLng}},
			},
			TravelMode: "DRIVE",
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
	return result, err
}
