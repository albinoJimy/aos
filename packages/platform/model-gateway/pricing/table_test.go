package pricing

import (
	"errors"
	"testing"
)

// sampleEntries devolve um conjunto determinista de entradas para os testes.
func sampleEntries() []Entry {
	return []Entry{
		{Model: "m-a", Region: "eu-west", Rate: Rate{InputPerMTokMicroUSD: 3_000_000, OutputPerMTokMicroUSD: 15_000_000, CacheReadPerMTokMicroUSD: 300_000, CacheWritePerMTokMicroUSD: 3_750_000}},
		{Model: "m-a", Region: "us-east", Rate: Rate{InputPerMTokMicroUSD: 2_500_000, OutputPerMTokMicroUSD: 10_000_000, CacheReadPerMTokMicroUSD: 250_000, CacheWritePerMTokMicroUSD: 3_125_000}},
	}
}

func TestLoadEmbedded(t *testing.T) {
	tbl, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if _, ok := tbl.RateFor("claude-sonnet", "eu-west"); !ok {
		t.Fatalf("esperava preco para claude-sonnet/eu-west na tabela embebida")
	}
	if tbl.Version() == "" {
		t.Fatalf("versao vazia")
	}
}

func TestRateForDistinctPerRegionAndFourTypes(t *testing.T) {
	tbl, err := NewTable("v1", sampleEntries())
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	eu, ok := tbl.RateFor("m-a", "eu-west")
	if !ok {
		t.Fatalf("sem preco eu-west")
	}
	us, ok := tbl.RateFor("m-a", "us-east")
	if !ok {
		t.Fatalf("sem preco us-east")
	}
	// Os quatro rates são distintos entre si (cache read < input < output; cache write > input).
	if !(eu.CacheReadPerMTokMicroUSD < eu.InputPerMTokMicroUSD &&
		eu.InputPerMTokMicroUSD < eu.OutputPerMTokMicroUSD &&
		eu.InputPerMTokMicroUSD < eu.CacheWritePerMTokMicroUSD) {
		t.Fatalf("rates nao sao distintos/ordenados como esperado: %+v", eu)
	}
	// O mesmo modelo tem preços diferentes por região.
	if eu.InputPerMTokMicroUSD == us.InputPerMTokMicroUSD {
		t.Fatalf("esperava preco de input distinto por regiao")
	}
}

func TestRateForNoPriceFailClosed(t *testing.T) {
	tbl, _ := NewTable("v1", sampleEntries())
	if _, ok := tbl.RateFor("m-a", "ap-south"); ok {
		t.Fatalf("regiao sem preco devia devolver ok=false (fail-closed)")
	}
	if _, ok := tbl.RateFor("m-desconhecido", "eu-west"); ok {
		t.Fatalf("modelo sem preco devia devolver ok=false (fail-closed)")
	}
}

func TestLoadTableFailClosed(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"json-invalido", `{`},
		{"versao-vazia", `{"version":"","entries":[]}`},
		{"entrada-sem-modelo", `{"version":"v","entries":[{"model":"","region":"r","rate":{}}]}`},
		{"entrada-sem-regiao", `{"version":"v","entries":[{"model":"m","region":"","rate":{}}]}`},
		{"rate-negativo", `{"version":"v","entries":[{"model":"m","region":"r","rate":{"input_per_mtok_micro_usd":-1}}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := LoadTable([]byte(c.doc)); err == nil {
				t.Fatalf("esperava erro fail-closed para %q", c.name)
			}
		})
	}
}

func TestLoadTableDuplicateFailClosed(t *testing.T) {
	doc := `{"version":"v","entries":[
		{"model":"m","region":"r","rate":{}},
		{"model":"m","region":"r","rate":{"input_per_mtok_micro_usd":1}}
	]}`
	_, err := LoadTable([]byte(doc))
	if !errors.Is(err, ErrDuplicateEntry) {
		t.Fatalf("esperava ErrDuplicateEntry, obtive %v", err)
	}
}

func TestVersionTamperEvident(t *testing.T) {
	base := sampleEntries()
	t1, _ := NewTable("2026.07", base)

	// Alterar um rate muda o digest (versão tamper-evident).
	changed := sampleEntries()
	changed[0].Rate.InputPerMTokMicroUSD = 9_999_999
	t2, _ := NewTable("2026.07", changed)
	if t1.Digest() == t2.Digest() {
		t.Fatalf("alterar um rate devia mudar o digest")
	}
	if t1.Version() == t2.Version() {
		t.Fatalf("versoes deviam divergir apos alteracao de rate (mesma tag, digest diferente)")
	}

	// O digest é ESTÁVEL face à ordem das entradas (canónico).
	reordered := []Entry{base[1], base[0]}
	t3, _ := NewTable("2026.07", reordered)
	if t1.Digest() != t3.Digest() {
		t.Fatalf("digest devia ser estavel face a reordenacao das entradas: %s vs %s", t1.Digest(), t3.Digest())
	}
}

func TestKeysOrdered(t *testing.T) {
	tbl, _ := NewTable("v1", sampleEntries())
	keys := tbl.Keys()
	if len(keys) != 2 {
		t.Fatalf("esperava 2 chaves, obtive %d", len(keys))
	}
	if keys[0].Region != "eu-west" || keys[1].Region != "us-east" {
		t.Fatalf("chaves nao ordenadas: %+v", keys)
	}
}
