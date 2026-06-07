package metro

import "testing"

func TestNearestKnownPoints(t *testing.T) {
	cases := []struct {
		name     string
		lat, lon float64
		want     string
	}{
		// A point right on top of Praça da Sé.
		{"se", -23.5506, -46.6328, "Sé"},
		// Largo da Batata, Pinheiros — nearest is the Line 4 Pinheiros station.
		{"pinheiros", -23.5673, -46.7020, "Pinheiros"},
		// Parque Ibirapuera gate 10 area — nearest is Line 5 Moema/AACD-ish.
		{"jabaquara", -23.6463, -46.6410, "Jabaquara"},
	}
	for _, c := range cases {
		st, dist, ok := Nearest(c.lat, c.lon)
		if !ok {
			t.Fatalf("%s: no stations loaded", c.name)
		}
		if st.Name != c.want {
			t.Errorf("%s: got %q (%d m), want %q", c.name, st.Name, dist, c.want)
		}
		if dist < 0 || dist > 5000 {
			t.Errorf("%s: implausible distance %d m to %q", c.name, dist, st.Name)
		}
	}
}

func TestStationsLoaded(t *testing.T) {
	if n := len(Stations()); n < 80 {
		t.Fatalf("expected the full network (~82 stations), got %d", n)
	}
}
