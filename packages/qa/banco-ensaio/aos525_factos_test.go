package bancoensaio

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

// A regra dos factos ignora acentos, maiúsculas, a forma dos espaços e a do hífen — e os
// limites conhecidos ficam presos aqui como ausências. Os casos «presente» são as formas vistas nas respostas guardadas de 2026-10-13; os casos «ausente»
// são o controlo negativo: um facto que de facto não está continua a contar como ausente.
func TestAOS525_RegraDosFactos(t *testing.T) {
	const nome, lote, numero = "Tomas Albergaria-Pinto", "LT-4817", "5538"
	for _, c := range []struct {
		porque, saida, facto string
		quer                 bool
	}{
		{"literal", "coordenado por Tomas Albergaria-Pinto e tem 7 pessoas", nome, true},
		{"acento posto pelo modelo", "Night shift coordinator: **Tomás Albergaria-Pinto**", nome, true},
		{"acento decomposto", "Coordenador: Tomás Albergaria-Pinto", nome, true},
		{"maiusculas", "TOMÁS ALBERGARIA-PINTO coordena o turno", nome, true},
		{"espacos e quebra de linha", "Tomás\n   Albergaria-Pinto", nome, true},
		{"lote em minusculas", "lote lt-4817", lote, true},
		{"numero", "o numero interno e 5538.", numero, true},
		{"espaco nao separavel", "Tomás\u00a0Albergaria-Pinto", nome, true},
		{"hifen nao separavel", "Tomás Albergaria\u2011Pinto", nome, true},
		{"travessao no codigo", "Batch code: LT\u20134817", lote, true},
		{"negrito a volta do nome", "**Tomás Albergaria-Pinto**", nome, true},

		{"outro nome", "Night shift coordinator: Tomás Albuquerque-Pinto", nome, false},
		{"so o primeiro nome", "coordenado por Tomás", nome, false},
		{"sem o hifen", "Tomás Albergaria Pinto", nome, false},
		{"letra em falta", "Tomás Albergria-Pinto", nome, false},
		{"numero diferente", "o numero interno e 5583", numero, false},
		{"lote diferente", "Batch code: LT-4871", lote, false},
		{"lote sem o hifen", "Batch code: LT4817", lote, false},
		{"saida vazia", "", nome, false},
		{"limite: separador de milhares", "2,356 boxes counted", "2356", false},
		{"limite: milhares com espaco", "2 356 caixas", "2356", false},
		{"limite: negrito a meio do nome", "**Tomás** Albergaria-Pinto", nome, false},
		{"limite: ordem trocada", "Albergaria-Pinto, Tomás", nome, false},
		{"facto so de espacos nao casa com tudo", "qualquer texto", "  \n", false},
		{"facto so de marcas nao casa com tudo", "qualquer texto", "\u0301", false},
	} {
		if tem := factoNaSaida(c.saida, c.facto); tem != c.quer {
			t.Errorf("%s: factoNaSaida(%q, %q) = %v; quer %v", c.porque, c.saida, c.facto, tem, c.quer)
		}
	}
}

// A dobra não muda um texto que já está na forma de comparação, e não junta palavras: os
// espaços reduzem-se a um, não a zero.
func TestAOS525_ADobraNaoJuntaPalavras(t *testing.T) {
	for entrada, quer := range map[string]string{
		"tomas albergaria-pinto":      "tomas albergaria-pinto",
		"  Tomás   Albergaria-Pinto ": "tomas albergaria-pinto",
		"LT-4817\t\n5538":             "lt-4817 5538",
		"Ação, Coração, AÇÚCAR":       "acao, coracao, acucar",
	} {
		if tem := dobrarParaComparar(entrada); tem != quer {
			t.Errorf("dobrarParaComparar(%q) = %q; quer %q", entrada, tem, quer)
		}
	}
	if factoNaSaida("Tomas Albergaria-PintoLT-4817", "Pinto LT-4817") {
		t.Error("um espaco do facto nao pode casar com palavras coladas na saida")
	}
}

// O resultado de um run é «presentes» só com TODOS os factos na saída, pela regra dobrada: é
// esta função que o nó de ensaio chama, e a forma acentuada é a que os modelos reais devolvem.
func TestAOS525_VerificarFactos_TodosOuAusentes(t *testing.T) {
	factos := []string{"LT-4817", "Tomas Albergaria-Pinto"}
	for saida, quer := range map[string]string{
		"Batch code: **LT-4817** / Night shift coordinator: **Tomás Albergaria-Pinto**": FactosPresentes,
		"Lote: LT-4817 / Coordenador: Tomas Albergaria-Pinto":                           FactosPresentes,
		"Night shift coordinator: Tomás Albergaria-Pinto":                               FactosAusentes,
		"Batch code: LT-4817": FactosAusentes,
		"":                    FactosAusentes,
	} {
		if tem := verificarFactos(saida, factos); tem != quer {
			t.Errorf("verificarFactos(%q) = %s; quer %s", saida, tem, quer)
		}
	}
}

// A bateria embebida passa a validação nova, e um facto com espaços nas pontas — que os perdia
// na comparação — é recusado no carregamento.
func TestAOS525_ABateriaRecusaFactosQueADobraDesfaz(t *testing.T) {
	b, err := CarregarBateria()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b.Casos {
		for _, n := range c.Nos {
			for _, f := range n.Factos {
				if f != strings.TrimSpace(f) || dobrarParaComparar(f) == "" {
					t.Errorf("%s/%s: o facto %q nao sobrevive a dobra", c.ID, n.ID, f)
				}
			}
		}
	}
	for _, mau := range []string{`" F-1"`, `"F-1 "`, `" "`} {
		fs := bateriaDeTeste()
		cru := string(fs["bateria/casos.json"].Data)
		if strings.Count(cru, `"F-1"`) != 1 {
			t.Fatal("a bateria de teste mudou: o facto F-1 ja nao aparece uma so vez")
		}
		fs["bateria/casos.json"] = &fstest.MapFile{Data: []byte(strings.Replace(cru, `"F-1"`, mau, 1))}
		if _, err := carregarBateria(fs); !errors.Is(err, ErrBateria) {
			t.Errorf("facto %s: err = %v, quer ErrBateria", mau, err)
		}
	}
}
