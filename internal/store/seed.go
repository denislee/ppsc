package store

import (
	"context"

	"ppsc/internal/models"
)

// SeedDefaults inserts starter site configs when the sites table is empty.
// These target São Paulo sale listings. Real-estate portals change their
// markup and JSON shape often and several actively block scrapers, so treat
// these as starting points to refine from the UI — not guaranteed-working.
func (s *Store) SeedDefaults(ctx context.Context) error {
	n, err := s.CountSites(ctx)
	if err != nil || n > 0 {
		return err
	}
	for _, st := range defaultSites() {
		site := st
		if err := s.SaveSite(ctx, &site); err != nil {
			return err
		}
	}
	return nil
}

func defaultSites() []models.Site {
	return []models.Site{
		{
			Name:        "VivaReal — SP (venda)",
			Enabled:     true,
			Strategy:    models.StrategyCSS,
			JSRender:    true,
			URLTemplate: "https://www.vivareal.com.br/venda/sp/sao-paulo/?preco-ate={maxPrice}&pagina={page}",
			MaxPages:    2,
			Notes:       "WORKING via headless browser (JSRender) as of last check: ~30 cards/page. The plain HTTP client gets a 403 anti-bot page, so this MUST stay on 'Render with headless browser'. Selectors use VivaReal's stable data-cy attributes.",
			Selectors: models.Selectors{
				Item:         `[data-cy="rp-property-cd"]`,
				Title:        `[data-cy="rp-cardProperty-street-txt"]`,
				URL:          `a[href*="/imovel/"]`,
				AttrURL:      "href",
				URLPrefix:    "https://www.vivareal.com.br",
				Image:        `[data-cy="rp-cardProperty-image-img"]`,
				AttrImage:    "src",
				Price:        `[data-cy="rp-cardProperty-price-txt"]`,
				Address:      `[data-cy="rp-cardProperty-street-txt"]`,
				Neighborhood: `[data-cy="rp-cardProperty-location-txt"]`,
				Bedrooms:     `[data-cy="rp-cardProperty-bedroomQuantity-txt"]`,
				Bathrooms:    `[data-cy="rp-cardProperty-bathroomQuantity-txt"]`,
				ParkingSpots: `[data-cy="rp-cardProperty-parkingSpacesQuantity-txt"]`,
				AreaM2:       `[data-cy="rp-cardProperty-propertyArea-txt"]`,
			},
		},
		{
			Name:        "ZAP Imóveis — SP (venda)",
			Enabled:     true,
			Strategy:    models.StrategyCSS,
			JSRender:    true,
			URLTemplate: "https://www.zapimoveis.com.br/venda/imoveis/sp+sao-paulo/?precoMaximo={maxPrice}&pagina={page}",
			MaxPages:    2,
			Notes:       "WORKING via headless browser (JSRender) as of last check: ~30 cards/page. Same anti-bot wall and same data-cy component markup as VivaReal (its sibling on the Grupo ZAP/OLX stack).",
			Selectors: models.Selectors{
				Item:         `[data-cy="rp-property-cd"]`,
				Title:        `[data-cy="rp-cardProperty-street-txt"]`,
				URL:          `a[href*="/imovel/"]`,
				AttrURL:      "href",
				URLPrefix:    "https://www.zapimoveis.com.br",
				Image:        `[data-cy="rp-cardProperty-image-img"]`,
				AttrImage:    "src",
				Price:        `[data-cy="rp-cardProperty-price-txt"]`,
				Address:      `[data-cy="rp-cardProperty-street-txt"]`,
				Neighborhood: `[data-cy="rp-cardProperty-location-txt"]`,
				Bedrooms:     `[data-cy="rp-cardProperty-bedroomQuantity-txt"]`,
				Bathrooms:    `[data-cy="rp-cardProperty-bathroomQuantity-txt"]`,
				ParkingSpots: `[data-cy="rp-cardProperty-parkingSpacesQuantity-txt"]`,
				AreaM2:       `[data-cy="rp-cardProperty-propertyArea-txt"]`,
			},
		},
		{
			Name:        "OLX — Imóveis SP (venda)",
			Enabled:     true,
			Strategy:    models.StrategyNextData,
			URLTemplate: "https://www.olx.com.br/imoveis/venda/estado-sp/sao-paulo-e-regiao/sao-paulo?pe={maxPrice}&o={page}",
			MaxPages:    2,
			Notes:       "WORKING as of last check: ~50 listings/page from props.pageProps.ads. It does rate-limit if hit too often — keep the request delay reasonable.",
			Selectors: models.Selectors{
				Item:    "props.pageProps.ads",
				Title:   "subject",
				URL:     "url",
				Image:   "thumbnail",
				Price:   "price",
				Address: "location",
			},
		},
		{
			Name:        "QuintoAndar — SP (comprar)",
			Enabled:     true,
			Strategy:    models.StrategyNextData,
			URLTemplate: "https://www.quintoandar.com.br/comprar/imovel/sao-paulo-sp-brasil?preco-max={maxPrice}",
			MaxPages:    1,
			Notes:       "WORKING as of last check: ~14 listings embedded per page (the rest load via XHR on scroll). Listings are keyed by ID under '...houses', which the nextdata strategy iterates as a map.",
			Selectors: models.Selectors{
				Item:         "props.pageProps.initialState.houses",
				Title:        "address.address",
				URL:          "id",
				URLPrefix:    "https://www.quintoandar.com.br/imovel",
				Price:        "salePrice",
				Address:      "address.address",
				Neighborhood: "regionName",
				Bedrooms:     "bedrooms",
				Bathrooms:    "bathrooms",
				ParkingSpots: "parkingSpots",
				AreaM2:       "area",
			},
		},
		{
			Name:        "Lello Imóveis — SP (venda)",
			Enabled:     true,
			Strategy:    models.StrategyNextData,
			URLTemplate: "https://www.lelloimoveis.com.br/venda/imoveis/sp-sao-paulo/?page={page}",
			MaxPages:    2,
			Notes:       "WORKING as of last check: 20 listings/page from props.pageProps.initialPaginatedRealties.list (no browser needed). The listing URL is built from idImovel; if a link 404s, refine URLPrefix to match Lello's current detail-page pattern.",
			Selectors: models.Selectors{
				Item:         "props.pageProps.initialPaginatedRealties.list",
				Title:        "endereco",
				URL:          "idImovel",
				URLPrefix:    "https://www.lelloimoveis.com.br/imovel",
				Image:        "fotos.0.enderecoFoto",
				Price:        "valorVenda",
				Address:      "endereco",
				Neighborhood: "bairro",
				Bedrooms:     "quantidadeDormitorios",
				Bathrooms:    "quantidadeBanheiros",
				ParkingSpots: "quantidadeVagas",
				AreaM2:       "metragemPrincipal",
			},
		},
		{
			Name:        "Pacheco Imóveis — SP (comprar)",
			Enabled:     true,
			Strategy:    models.StrategyCSS,
			JSRender:    true,
			URLTemplate: "https://pacheco.com.br/comprar/page/{page}/",
			MaxPages:    2,
			Notes:       "WORKING via headless browser (JSRender) as of last check: ~16 '.imovel.item' cards/page with title, area, bedrooms and detail URL. Area/bedrooms are injected by JS, so JSRender is required. NOTE: Pacheco does not publish prices on its results page, so listings here have no price (filter by neighborhood/area instead).",
			Selectors: models.Selectors{
				Item:         ".imovel.item",
				Title:        ".box-txt h3",
				URL:          `a[href*="/imoveis/"]`,
				AttrURL:      "href",
				Image:        ".box-img img",
				AttrImage:    "src",
				Neighborhood: ".box-txt h3",
				AreaM2:       ".infos__item:nth-of-type(1)",
				Bedrooms:     ".infos__item:nth-of-type(2)",
			},
		},
		{
			Name:        "Sinai Imobiliária — SP (imóveis)",
			Enabled:     true,
			Strategy:    models.StrategyCSS,
			JSRender:    true,
			URLTemplate: "https://www.sinai.adm.br/imoveis?page={page}",
			MaxPages:    2,
			Notes:       "WORKING via headless browser (JSRender) as of last check: ~12 '.card-with-buttons' cards/page with type, neighbourhood, price and detail URL. NOTE: /imoveis mixes sale + rent + commercial listings, so set a Min price filter (e.g. R$ 100.000) to drop the rentals (priced per month).",
			Selectors: models.Selectors{
				Item:         "a.card-with-buttons",
				AttrURL:      "href",
				URLPrefix:    "https://www.sinai.adm.br",
				Title:        ".card-with-buttons__title",
				Neighborhood: ".card-with-buttons__heading",
				Price:        ".card-with-buttons__value",
			},
		},
		{
			Name:        "Generic CSS example (edit me)",
			Enabled:     false,
			Strategy:    models.StrategyCSS,
			URLTemplate: "https://example.com/imoveis/sao-paulo?max={maxPrice}&page={page}",
			MaxPages:    1,
			Notes:       "Template for any server-rendered site: set 'item' to the card selector and the field selectors relative to it. Use the Test button to iterate.",
			Selectors: models.Selectors{
				Item:      "article.listing-card",
				Title:     "h2.title",
				URL:       "a.card-link",
				AttrURL:   "href",
				Image:     "img",
				AttrImage: "src",
				Price:     ".price",
				Address:   ".address",
				Bedrooms:  ".beds",
				AreaM2:    ".area",
			},
		},
	}
}
