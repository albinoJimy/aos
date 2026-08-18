package integration

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestClassificadorDeRiscoVemAntesDaPolitica fixa uma ORDEM da qual depende uma propriedade que
// não é visível em nenhum destes ficheiros isoladamente.
//
// O oráculo de autonomia (AOS-087) vive DENTRO da política e lê `Call.Context.RiskClass`. Quem
// escreve esse campo é o `risk-classify`. Se alguém os trocar de posição — ou remover o
// classificador — o overlay passa a ver vazio, `riskClassFromString("")` resolve para danger
// (fail-closed), e a taxonomia L0–L5 COLAPSA: a L3 comporta-se como a L1, a L4 escala tudo, e só
// a L5 deixa correr. Cinco níveis documentados, dois reais.
//
// E não haveria sintoma. A falha é para o lado SEGURO — mais oversight, não menos — e ninguém
// abre um bilhete porque o sistema pediu autorização a mais. Foi assim que passou despercebida
// até alguém perguntar porque é que o oráculo não estava ligado em produção.
//
// O teste lê o TEXTO do composition root de propósito: o que se quer fixar é a ordem em que os
// hooks são escritos, e essa é uma propriedade do ficheiro. Reconstruir a cadeia aqui exigiria
// verificadores, autoridades e um PDP reais — e um teste que precisa de meio nó para correr é um
// teste que se apaga na primeira vez que estorva.
func TestClassificadorDeRiscoVemAntesDaPolitica(t *testing.T) {
	b, err := os.ReadFile("secured.go")
	if err != nil {
		t.Fatalf("ler o composition root: %v", err)
	}
	src := string(b)

	// A janela é a lista de hooks passada ao RM — não o ficheiro inteiro, senão qualquer menção
	// em comentário satisfazia o teste.
	ini := strings.Index(src, "hooks = append(hooks,")
	if ini < 0 {
		t.Fatal("não encontrei a montagem da cadeia — o varredor ficou cego e o teste vacuoso")
	}
	fim := strings.Index(src[ini:], "\n\t)")
	if fim < 0 {
		t.Fatal("não encontrei o fim da montagem da cadeia")
	}
	janela := src[ini : ini+fim]

	// Só as linhas de código: um comentário a falar de risco não conta como hook.
	var ordem []string
	re := regexp.MustCompile(`(?m)^\s*(referencemonitor\.New\w+|pdp\.New\w+|identity\.New\w+|\w+Hook)\b`)
	for _, l := range strings.Split(janela, "\n") {
		corte := strings.TrimSpace(l)
		if corte == "" || strings.HasPrefix(corte, "//") {
			continue
		}
		if m := re.FindString(l); m != "" {
			ordem = append(ordem, strings.TrimSpace(m))
		}
	}
	if len(ordem) < 5 {
		t.Fatalf("só reconheci %d hooks (%v) — o varredor partiu-se", len(ordem), ordem)
	}

	iClass, iPol := -1, -1
	for i, h := range ordem {
		switch {
		case strings.Contains(h, "NewRiskClassifier"):
			iClass = i
		case strings.Contains(h, "NewPolicyCheck"):
			iPol = i
		}
	}
	if iClass < 0 {
		t.Fatalf("o classificador de risco SAIU da cadeia (%v).\n"+
			"Sem ele o oráculo de autonomia vê a classe vazia, resolve para danger, e a\n"+
			"taxonomia L0–L5 fica com dois estados — sem sintoma nenhum.", ordem)
	}
	if iPol < 0 {
		t.Fatalf("não encontrei o hook de política em %v", ordem)
	}
	if iClass > iPol {
		t.Fatalf("o classificador (posição %d) está DEPOIS da política (posição %d).\n"+
			"A política lê a classe que o classificador escreve; por esta ordem lê sempre vazio.\n"+
			"Cadeia: %v", iClass, iPol, ordem)
	}
}
