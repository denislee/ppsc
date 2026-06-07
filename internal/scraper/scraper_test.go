package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ppsc/internal/models"
)

func TestFetcherThrottle(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := NewFetcher()
	f.SetMinInterval(time.Second) // 1s is the enforced politeness floor

	start := time.Now()
	for i := range 2 {
		if _, err := f.Get(context.Background(), srv.URL); err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// First request fires immediately; the second waits >= the 1s interval.
	if elapsed < 900*time.Millisecond {
		t.Errorf("2 requests took %v, expected the throttle to space them ~1s apart", elapsed)
	}
	if hits.Load() != 2 {
		t.Errorf("server saw %d hits, want 2", hits.Load())
	}
}

func TestFetcherRetriesOn429(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	f := NewFetcher()
	f.SetMinInterval(time.Second) // clamped minimum; doesn't gate the first call
	body, err := f.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if body != "recovered" || hits.Load() != 2 {
		t.Errorf("body=%q hits=%d; want retry after 429", body, hits.Load())
	}
}

func TestParseCSS(t *testing.T) {
	html := `<html><body>
		<article class="listing-card">
			<h2 class="title">Apto Pinheiros</h2>
			<a class="card-link" href="/imovel/123">link</a>
			<img src="https://img/1.jpg">
			<span class="price">R$ 1.250.000</span>
			<span class="address">Rua X, Pinheiros</span>
			<span class="beds">2 quartos</span>
			<span class="area">75 m²</span>
		</article>
		<article class="listing-card">
			<h2 class="title">Casa Vila Madalena</h2>
			<a class="card-link" href="https://other.com/456">link</a>
			<span class="price">R$ 2,5 milhões</span>
		</article>
	</body></html>`

	site := models.Site{
		Strategy: models.StrategyCSS,
		Selectors: models.Selectors{
			Item: "article.listing-card", Title: "h2.title", URL: "a.card-link", URLPrefix: "https://site.com",
			Image: "img", Price: ".price", Address: ".address", Bedrooms: ".beds", AreaM2: ".area",
		},
	}
	props, err := parseCSS(html, site)
	if err != nil {
		t.Fatalf("parseCSS: %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("want 2 props, got %d", len(props))
	}
	p := props[0]
	if p.Title != "Apto Pinheiros" {
		t.Errorf("title = %q", p.Title)
	}
	if p.URL != "https://site.com/imovel/123" {
		t.Errorf("url = %q (relative should get prefix)", p.URL)
	}
	if p.Price != 1_250_000 {
		t.Errorf("price = %d, want 1250000", p.Price)
	}
	if p.Bedrooms != 2 {
		t.Errorf("bedrooms = %d", p.Bedrooms)
	}
	if p.AreaM2 != 75 {
		t.Errorf("area = %d", p.AreaM2)
	}
	// Absolute URL must be left untouched; "milhões" multiplier applied.
	if props[1].URL != "https://other.com/456" {
		t.Errorf("abs url rewritten: %q", props[1].URL)
	}
	if props[1].Price != 2_000_000 {
		t.Errorf("milhoes price = %d, want 2000000", props[1].Price)
	}
}

func TestParseNextData(t *testing.T) {
	html := `<html><body>
	<script id="__NEXT_DATA__" type="application/json">
	{"props":{"pageProps":{"results":{"listings":[
		{"listing":{"title":"Apto Centro","pricingInfos":[{"price":"480000"}],"bedrooms":[3],"usableAreas":[90],"address":{"neighborhood":"Centro"}},"link":{"href":"/p/1"}},
		{"listing":{"title":"Studio","pricingInfos":[{"price":650000}],"bedrooms":[1],"address":{"neighborhood":"Itaim"}},"link":{"href":"/p/2"}}
	]}}}}
	</script></body></html>`

	site := models.Site{
		Strategy: models.StrategyNextData,
		Selectors: models.Selectors{
			Item: "props.pageProps.results.listings", Title: "listing.title", URL: "link.href",
			URLPrefix: "https://x.com", Price: "listing.pricingInfos.0.price",
			Bedrooms: "listing.bedrooms.0", AreaM2: "listing.usableAreas.0", Neighborhood: "listing.address.neighborhood",
		},
	}
	props, err := parseNextData(html, site)
	if err != nil {
		t.Fatalf("parseNextData: %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("want 2, got %d", len(props))
	}
	if props[0].Title != "Apto Centro" || props[0].Price != 480000 || props[0].Bedrooms != 3 {
		t.Errorf("p0 = %+v", props[0])
	}
	// Numeric JSON price (not a string) must also parse.
	if props[1].Price != 650000 {
		t.Errorf("p1 numeric price = %d", props[1].Price)
	}
	if props[0].URL != "https://x.com/p/1" {
		t.Errorf("url = %q", props[0].URL)
	}
}

func TestParseNextDataMap(t *testing.T) {
	// QuintoAndar-style: listings keyed by ID under a map, not an array.
	html := `<html><body>
	<script id="__NEXT_DATA__" type="application/json">
	{"props":{"pageProps":{"initialState":{"houses":{
		"892820623":{"id":"892820623","salePrice":1000000,"area":105,"bedrooms":3,"regionName":"Santana","address":{"address":"R. Mal. Hermes"}},
		"893183631":{"id":"893183631","salePrice":850000,"area":70,"bedrooms":2,"regionName":"Pinheiros","address":{"address":"Rua X"}}
	}}}}}
	</script></body></html>`

	site := models.Site{
		Strategy: models.StrategyNextData,
		Selectors: models.Selectors{
			Item: "props.pageProps.initialState.houses", Title: "address.address", URL: "id",
			URLPrefix: "https://q.com/imovel", Price: "salePrice", AreaM2: "area",
			Bedrooms: "bedrooms", Neighborhood: "regionName",
		},
	}
	props, err := parseNextData(html, site)
	if err != nil {
		t.Fatalf("parseNextData(map): %v", err)
	}
	if len(props) != 2 {
		t.Fatalf("want 2 from map, got %d", len(props))
	}
	// Keys are iterated sorted, so 892820623 comes first.
	if props[0].Price != 1_000_000 || props[0].Neighborhood != "Santana" {
		t.Errorf("p0 = %+v", props[0])
	}
	if props[0].URL != "https://q.com/imovel/892820623" {
		t.Errorf("url from id = %q", props[0].URL)
	}
}

func TestBuildURL(t *testing.T) {
	got := BuildURL("https://s/sp?max={maxPrice}&beds={minBeds}&q={query}&p={page}",
		models.Filters{MaxPrice: 900000, MinBedrooms: 2, Query: "apartamento mobiliado"}, 3)
	want := "https://s/sp?max=900000&beds=2&q=apartamento+mobiliado&p=3"
	if got != want {
		t.Errorf("BuildURL\n got=%s\nwant=%s", got, want)
	}
}

func TestParseBRL(t *testing.T) {
	cases := map[string]int64{
		"R$ 1.250.000":     1_250_000,
		"R$ 1.250.000,00":  1_250_000,
		"850000":           850_000,
		"R$ 2,5 milhões":   2_000_000, // decimals dropped before multiplier (best effort)
		"":                 0,
		"sob consulta":     0,
	}
	for in, want := range cases {
		if got := parseBRL(in); got != want {
			t.Errorf("parseBRL(%q) = %d, want %d", in, got, want)
		}
	}
}
