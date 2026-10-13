package bancoensaio

import "testing"

// A regra dos factos ignora acentos, maiúsculas e espaços repetidos — e mais nada. Os casos
// «presente» são as formas vistas nas respostas guardadas de 2026-10-13; os casos «ausente»
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

		{"outro nome", "Night shift coordinator: Tomás Albuquerque-Pinto", nome, false},
		{"so o primeiro nome", "coordenado por Tomás", nome, false},
		{"sem o hifen", "Tomás Albergaria Pinto", nome, false},
		{"letra em falta", "Tomás Albergria-Pinto", nome, false},
		{"numero diferente", "o numero interno e 5583", numero, false},
		{"lote diferente", "Batch code: LT-4871", lote, false},
		{"lote sem o hifen", "Batch code: LT4817", lote, false},
		{"saida vazia", "", nome, false},
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
