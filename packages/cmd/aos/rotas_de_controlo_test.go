package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// rotasDeControlo são os caminhos que mudam ou expõem o ESTADO DE GOVERNAÇÃO do nó. Todos têm de
// passar pelo mTLS do plano de controlo.
//
// A lista é explícita, e não derivada por heurística, porque a pergunta "isto é plano de
// controlo?" é de JUÍZO e não de sintaxe. Uma rota nova obriga a decidir — e a decisão fica
// escrita aqui, com o resto do raciocínio.
var rotasDeControlo = map[string]string{
	"handleSteer":           "injecta correcção num run em curso",
	"handlePause":           "suspende um run",
	"handleResume":          "retoma um run suspenso",
	"handleApprove":         "cerimónia four-eyes: destranca uma acção escalada",
	"handleChallenge":       "emite o challenge que a cerimónia consome",
	"handleAutonomySet":     "muda NÍVEIS DE AUTONOMIA — quanta supervisão humana se aplica",
	"handleAutonomyGet":     "expõe a postura de autonomia em vigor",
	"handleAutonomySimular": "expõe o que uma configuração faria ao histórico selado",
}

// TestTodaRotaDeControloPassaPeloMTLS fecha um defeito de PROCESSO, não de código.
//
// `admitControlMTLS` NÃO é middleware: cada handler de controlo chama-a explicitamente. Uma
// barreira imposta por convenção de escrita só resiste enquanto ninguém escrever distraído — e eu
// escrevi. As três rotas de autonomia nasceram sem ela, enquanto o plano e o comentário da própria
// rota afirmavam que passavam "pela mesma admissão do /approve".
//
// O que torna isto insidioso: sem `AOS_CONTROL_MTLS_CA_PATH` configurado, `admitControlMTLS`
// devolve `true` sempre. Um handler que a esqueça comporta-se de forma IDÊNTICA a um que a chame,
// em todos os testes e em toda a operação — até ao dia em que alguém liga o mTLS e descobre que
// uma das portas nunca esteve fechada.
func TestTodaRotaDeControloPassaPeloMTLS(t *testing.T) {
	fontes := []string{"api.go", "autonomy_route.go", "autonomy_simular.go"}
	corpo := map[string]string{}
	for _, f := range fontes {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("ler %s: %v", f, err)
		}
		for nome, texto := range extrairHandlers(string(b)) {
			corpo[nome] = texto
		}
	}
	if len(corpo) < 8 {
		t.Fatalf("so extrai %d handlers — o varredor partiu-se e o teste ficaria vacuoso", len(corpo))
	}

	// AS DUAS BARREIRAS, e a razao de o teste as verificar juntas: quando escrevi este ficheiro
	// verifiquei so o mTLS, porque foi o que me ocorreu primeiro. As mesmas tres rotas estavam
	// TAMBEM fora do balde de admissao, e o teste — verde — nao me disse nada.
	//
	// Um teste que verifica metade de uma afirmacao e mais perigoso do que nenhum: cobre o
	// suficiente para se confiar nele, e deixa passar o resto sem sinal.
	barreiras := map[string]string{
		"admitControl":     "balde de admissao do plano de controlo (rate limit)",
		"admitControlMTLS": "mTLS do plano de controlo (certificado de cliente verificado)",
	}
	var faltam []string
	for nome, porque := range rotasDeControlo {
		texto, ok := corpo[nome]
		if !ok {
			t.Errorf("%s consta da lista de rotas de controlo mas NAO existe — remova-o ou corrija o nome", nome)
			continue
		}
		for barreira, oQueE := range barreiras {
			if !strings.Contains(texto, barreira+"(") {
				faltam = append(faltam, nome+" nao passa por "+barreira+" — "+oQueE+" ("+porque+")")
			}
		}
	}
	sort.Strings(faltam)
	if len(faltam) > 0 {
		t.Errorf("estas rotas de CONTROLO nao passam por admitControlMTLS:\n  %s\n\n"+
			"Sem ela, com AOS_CONTROL_MTLS_CA_PATH ligado a porta fica ABERTA — e como a funcao\n"+
			"devolve true quando o mTLS nao esta configurado, nada distingue este caso do correcto\n"+
			"ate ao dia em que alguem o liga.", strings.Join(faltam, "\n  "))
	}
}

var reHandler = regexp.MustCompile(`(?m)^func \(h \*apiHandler\) (handle\w+)\(`)

// extrairHandlers devolve o corpo de cada handler, do cabeçalho até à chaveta de fecho na coluna
// zero. Textual de propósito: o que se quer verificar é o que está ESCRITO em cada handler, e um
// parser de AST daria a mesma resposta com muito mais superfície para partir.
func extrairHandlers(src string) map[string]string {
	out := map[string]string{}
	idx := reHandler.FindAllStringSubmatchIndex(src, -1)
	for i, m := range idx {
		nome := src[m[2]:m[3]]
		fim := len(src)
		if i+1 < len(idx) {
			fim = idx[i+1][0]
		}
		out[nome] = src[m[0]:fim]
	}
	return out
}
