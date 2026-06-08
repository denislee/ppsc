package scheduler

import "testing"

func TestExtractStreet(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"street with number", "Apartamento à venda na Rua Augusta, 123 - Consolação", "Rua Augusta, 123"},
		{"avenida trims prose", "Cobertura na Avenida Paulista próximo ao metrô", "Avenida Paulista"},
		{"abbreviation expands", "Lindo apto Av. Brigadeiro Faria Lima, 2000", "Avenida Brigadeiro Faria Lima, 2000"},
		{"connector kept", "Imóvel na Avenida Nove de Julho", "Avenida Nove de Julho"},
		{"lowercase prefix", "Excelente apto na rua Haddock Lobo", "Rua Haddock Lobo"},
		{"all caps", "AP 2 DORM RUA AUGUSTA", "Rua AUGUSTA"},
		{"no real name is rejected", "Lindo apto em rua tranquila e arborizada", ""},
		{"no street at all", "Apartamento 2 quartos no Centro", ""},
		{"number without space", "Studio na Rua Augusta,123", "Rua Augusta, 123"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractStreet(c.in); got != c.want {
				t.Errorf("extractStreet(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestMetroQuery(t *testing.T) {
	// A usable address is preferred and free text is ignored.
	if got := metroQuery("Rua Oscar Freire, 500", "Jardins", "na Avenida Paulista", ""); got != "Rua Oscar Freire, 500, Jardins, São Paulo, SP, Brasil" {
		t.Errorf("address case: got %q", got)
	}
	// No address: the title's street is mined and scoped to the city.
	if got := metroQuery("", "Consolação", "Apto na Rua Augusta, 123", ""); got != "Rua Augusta, 123, Consolação, São Paulo, SP, Brasil" {
		t.Errorf("title case: got %q", got)
	}
	// Title has no street, description does: fall back to the description.
	if got := metroQuery("", "", "Apartamento reformado", "fica na Alameda Santos esquina"); got != "Alameda Santos, São Paulo, SP, Brasil" {
		t.Errorf("description fallback: got %q", got)
	}
	// Nothing to go on.
	if got := metroQuery("", "", "Apartamento 2 quartos", ""); got != "" {
		t.Errorf("empty case: got %q", got)
	}
}
