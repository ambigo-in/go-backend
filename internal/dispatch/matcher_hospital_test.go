package dispatch

import (
	"context"
	"testing"

	"ambigo-backend/interfaces"
	"ambigo-backend/internal/location"
)

type stubHospitals struct {
	mobiles []string
	err     error
}

func (s stubHospitals) ListActiveDriverMobilesByHospital(ctx context.Context, hospitalID string) ([]string, error) {
	return s.mobiles, s.err
}

type stubDrivers struct {
	ids map[string]string
	err error
}

func (s stubDrivers) FindDriverIDsByMobiles(ctx context.Context, mobiles []string) (map[string]string, error) {
	return s.ids, s.err
}

func testMatcherWithDrivers(t *testing.T, drivers map[string][2]float64, vehicleTypes map[string]string, busy []string) *Matcher {
	t.Helper()
	ls := location.NewMemoryStore()
	for id, ll := range drivers {
		if err := ls.UpdateLocation(id, ll[0], ll[1]); err != nil {
			t.Fatal(err)
		}
		if vt, ok := vehicleTypes[id]; ok {
			if err := ls.SetDriverVehicleType(id, vt); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range busy {
		if err := ls.SetDriverStatus(id, interfaces.StatusBusy); err != nil {
			t.Fatal(err)
		}
	}
	return NewMatcher(ls, NewRouteClient("", ""), nil)
}

func TestFindBestHospitalDriverNearestWithin10km(t *testing.T) {
	m := testMatcherWithDrivers(t,
		map[string][2]float64{
			"d-near": {17.3850, 78.4867},
			"d-far":  {17.3950, 78.4967},
		},
		nil, nil)
	m.SetHospitalDeps(
		stubHospitals{mobiles: []string{"m1", "m2"}},
		stubDrivers{ids: map[string]string{"m1": "d-near", "m2": "d-far"}},
	)
	got, ok := m.FindBestHospitalDriver(context.Background(), "h1", 17.3850, 78.4867, "")
	if !ok {
		t.Fatal("expected hospital candidate")
	}
	if got.DriverID != "d-near" {
		t.Fatalf("expected nearest d-near, got %s", got.DriverID)
	}
}

func TestFindBestHospitalDriverOutside10km(t *testing.T) {
	m := testMatcherWithDrivers(t,
		map[string][2]float64{"d-far": {18.3850, 79.4867}}, // ~150km away
		nil, nil)
	m.SetHospitalDeps(
		stubHospitals{mobiles: []string{"m1"}},
		stubDrivers{ids: map[string]string{"m1": "d-far"}},
	)
	if _, ok := m.FindBestHospitalDriver(context.Background(), "h1", 17.3850, 78.4867, ""); ok {
		t.Fatal("expected no candidate outside 10km")
	}
}

func TestFindBestHospitalDriverRespectsTypeAndAvailability(t *testing.T) {
	m := testMatcherWithDrivers(t,
		map[string][2]float64{
			"d-wrong-type": {17.3850, 78.4867},
			"d-busy":       {17.3851, 78.4868},
			"d-good":       {17.3852, 78.4869},
		},
		map[string]string{"d-wrong-type": "type-b", "d-busy": "type-a", "d-good": "type-a"},
		[]string{"d-busy"})
	m.SetHospitalDeps(
		stubHospitals{mobiles: []string{"m1", "m2", "m3"}},
		stubDrivers{ids: map[string]string{"m1": "d-wrong-type", "m2": "d-busy", "m3": "d-good"}},
	)
	got, ok := m.FindBestHospitalDriver(context.Background(), "h1", 17.3850, 78.4867, "type-a")
	if !ok || got.DriverID != "d-good" {
		t.Fatalf("expected d-good, got %+v ok=%v", got, ok)
	}
}

func TestFindBestHospitalDriverNoDeps(t *testing.T) {
	m := NewMatcher(location.NewMemoryStore(), NewRouteClient("", ""), nil)
	if _, ok := m.FindBestHospitalDriver(context.Background(), "h1", 17.0, 78.0, ""); ok {
		t.Fatal("expected no candidate without deps")
	}
	if _, ok := m.FindBestHospitalDriver(context.Background(), "", 17.0, 78.0, ""); ok {
		t.Fatal("expected no candidate without hospital")
	}
}
