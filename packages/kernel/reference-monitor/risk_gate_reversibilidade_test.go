package referencemonitor

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestLeituraLocalDeclaradaReversivelSaiGray fecha a cadeia inteira e responde à única pergunta
// que interessa: a taxonomia de autonomia L0–L5 tem mais do que dois estados?
//
// A cadeia tem quatro elos, e dois estavam partidos:
//
//	registry declara reversibilidade  →  ToolInvocation  →  Call.Context  →  RiskGate classifica
//	         (não existia o campo)                            (o adaptador RM→PDP não a copiava)
//
// Com qualquer um partido, `IsIrreversible()` devolve true (o desconhecido conta como
// irreversível), a primeira regra dispara, e TODA a acção sai `danger`. A L3 comporta-se como a
// L1, a L4 escala tudo, e só a L5 deixa correr — dois estados num mecanismo de seis.
//
// O VEREDICTO É `gray`, E NÃO `safe`. Escrevi este teste à espera de `safe` e ele apanhou-me:
// falta ainda a SENSIBILIDADE, e `SensitivityUnknown` resolve para o TOPO (fail-closed). Não a
// acrescentei ao registry de propósito — a sensibilidade é propriedade do DADO, não da tool. Uma
// `doc_read` que se declarasse "interna" estaria a afirmar algo sobre conteúdo que não controla,
// e trocaria uma classificação honesta por uma conveniente.
//
// `gray` chega para o que importa: a L4 ("corre por omissão, só escala em danger") deixa a
// leitura correr, e o `web_post` — danger por egress externo — continua a escalar. É a graduação
// que não existia.
func TestLeituraLocalDeclaradaReversivelSaiGray(t *testing.T) {
	gate := RiskGate{}

	call := &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://notes"}}
	call.Context.Reversibility = "reversible"
	if _, err := gate.Evaluate(context.Background(), call); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := call.Context.RiskClass; got != risk.ClassGray.String() {
		t.Fatalf("leitura local declarada reversível saiu %q, quero %q", got, risk.ClassGray.String())
	}

	// CONTROLO — a MESMA acção SEM a declaração tem de continuar a sair `danger`.
	//
	// É o que impede a leitura errada deste trabalho: não se afrouxou o classificador, deu-se ao
	// registry a possibilidade de DECLARAR. O silêncio continua a valer o pior caso. Sem este
	// caso, o assert acima seria compatível com "baixámos o rigor".
	mudo := &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://notes"}}
	if _, err := gate.Evaluate(context.Background(), mudo); err != nil {
		t.Fatalf("Evaluate (controlo): %v", err)
	}
	if got := mudo.Context.RiskClass; got != risk.ClassDanger.String() {
		t.Fatalf("acção SEM declaração saiu %q, quero %q — não declarar tem de continuar a valer o pior caso",
			got, risk.ClassDanger.String())
	}
}

// TestEgressExternoNaoEscapaPelaDeclaracao — declarar-se reversível NÃO é um passe livre.
//
// Uma tool que sai da máquina com dados sensíveis continua `danger` mesmo declarando-se reversível:
// a regra de egress é independente da de reversibilidade. Sem esta guarda, o campo novo seria uma
// porta — bastaria a um registry mal escrito declarar `reversible` num `web_post` para o tirar do
// escrutínio humano.
func TestEgressExternoNaoEscapaPelaDeclaracao(t *testing.T) {
	gate := RiskGate{}
	call := &Call{Capability: "cap:http.post", Resource: Resource{Type: "url", Value: "https://exemplo.test/x"}}
	call.Context.Reversibility = "reversible"
	call.Context.Sensitivity = "confidential"
	if _, err := gate.Evaluate(context.Background(), call); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := call.Context.RiskClass; got == risk.ClassSafe.String() {
		t.Fatal("egress externo de dados sensíveis saiu SAFE por se declarar reversível — o campo virou uma porta")
	}
}
