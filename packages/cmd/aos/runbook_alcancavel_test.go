package main

import (
	"strings"
	"testing"

	runbooks "github.com/aos-ref/platform/runbooks"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// UM RUNBOOK CUJO ALERTA NUNCA DISPARA TEM DE O DECLARAR.
//
// O registo de runbooks modelava DOIS estados — canonico sem alerta (exige NoAlertReason) e
// canonico com alerta que encaminha — e a validacao bidireccional ficava verde nos dois. O
// TERCEIRO estado passava calado: o runbook TEM alerta, o alerta encaminha para la, e mesmo
// assim nenhum alerta que ali chega pode disparar, porque o SLI que o alimenta nao tem
// produtor neste binario.
//
// A diferenca entre os dois e a diferenca entre "nao ha alerta" e "ha alerta e esta morto".
// O segundo e pior: o operador le o runbook, ve o alerta na tabela, e conclui que esta
// coberto.
//
// A VERIFICACAO VIVE AQUI e nao no registo porque "tem produtor" e uma propriedade DESTE
// binario: um destacamento que componha o scheduler torna o RB-01 alcancavel sem uma linha
// mudar no registo. E por isso que o predicado [produtorNoNo] esta em cmd/aos.
func TestRunbook_AlcancavelOuEntaoDeclarado(t *testing.T) {
	// OS DOIS CATALOGOS, que e o que o no compoe ([sloEvaluator]: `med` + `ops`). Ler so o
	// operacional daria a RESPOSTA ERRADA — o `cost_per_trajectory_high` (RB-03) e o
	// `override_rate_high` (RB-05) vivem no catalogo da MEDIACAO, e sao precisamente eles
	// que mantem esses dois runbooks alcancaveis.
	tipoRota := carregaRotas()

	// A TORNEIRA LIGADA e o caso MAIS FAVORAVEL: se nem assim um runbook tem alerta com
	// produtor, e porque nao tem mesmo. Usar o caso desfavoravel (torneira desligada)
	// declararia mortos os tres criticais derivados de spans, que e o defeito do achado D
	// por outra porta.
	const torneira = true

	// runbook -> alertas que la encaminham, e quantos deles tem produtor
	vivos := map[string]int{}
	total := map[string]int{}
	for rb, slis := range tipoRota {
		for _, sli := range slis {
			total[rb]++
			if produtorNoNo(sli, torneira) {
				vivos[rb]++
			}
		}
	}

	porID := map[string]runbooks.Entry{}
	for _, e := range runbooks.Registry() {
		porID[e.ID] = e
	}

	var semDeclaracao, declaracaoObsoleta []string
	for rb, n := range total {
		e, ok := porID[rb]
		if !ok {
			t.Fatalf("alerta encaminha para o runbook %q que nao esta no registo — o gate de nao-orfaos devia ter apanhado isto", rb)
		}
		alcancavel := vivos[rb] > 0
		declarado := strings.TrimSpace(e.SemProdutorReason) != ""

		if !alcancavel && !declarado {
			semDeclaracao = append(semDeclaracao, rb)
		}
		// A DIRECCAO INVERSA, e e ela que impede a declaracao de apodrecer: um runbook que
		// PASSOU a ser alcancavel — porque alguem cablou o produtor — nao pode continuar a
		// dizer que nao e. Sem esta asercao a declaracao viraria comentario morto, que e a
		// classe de mentira que este eixo inteiro existe para fechar.
		if alcancavel && declarado {
			declaracaoObsoleta = append(declaracaoObsoleta, rb)
		}
		_ = n
	}

	if len(semDeclaracao) > 0 {
		t.Errorf("runbook(s) %v nao tem NENHUM alerta com produtor neste binario e nao o declaram.\n"+
			"O operador le o runbook, ve o alerta na tabela, e conclui que esta coberto.\n"+
			"Preencha Entry.SemProdutorReason com QUEM produz o SLI e ONDE, ou cable o produtor.", semDeclaracao)
	}
	if len(declaracaoObsoleta) > 0 {
		t.Errorf("runbook(s) %v declaram-se SEM produtor mas ja tem alerta alcancavel — "+
			"a declaracao ficou obsoleta e passou a mentir ao contrario. Remova o SemProdutorReason.", declaracaoObsoleta)
	}
}

// CONTROLO ANTI-VACUIDADE: O GATE TEM DE SABER DIZER QUE NAO.
//
// Sem este caso, um `produtorNoNo` que devolvesse SEMPRE true tornaria todos os runbooks
// "alcancaveis" e o teste de cima passaria sem verificar coisa nenhuma — exactamente a forma
// de vacuidade que a casa proibe. Aqui exige-se que a particao seja REAL: alguns runbooks
// alcancaveis, alguns nao, e os que nao o sao com declaracao escrita.
func TestRunbook_AParticaoEReal(t *testing.T) {
	rotas := carregaRotas()
	vivos, mortos := map[string]bool{}, map[string]bool{}
	for rb, slis := range rotas {
		for _, sli := range slis {
			if produtorNoNo(sli, true) {
				vivos[rb] = true
			}
		}
	}
	for rb := range rotas {
		if !vivos[rb] {
			mortos[rb] = true
		}
	}
	if len(vivos) == 0 {
		t.Fatal("NENHUM runbook e alcancavel — o predicado de produtor esta a devolver sempre false e o gate nao verifica nada")
	}
	if len(mortos) == 0 {
		t.Fatal("TODOS os runbooks sao alcancaveis — o predicado esta a devolver sempre true e o gate de cima passaria vazio")
	}
	// E os mortos sao os que sabemos: dois, e nao os quatro que a varredura contou com o
	// sinal antigo. Fixar o numero faz este teste cair quando alguem cablar (ou perder) um
	// produtor, que e precisamente quando se quer olhar.
	if len(mortos) != 2 {
		t.Errorf("esperava 2 runbooks sem produtor (RB-01, PROC-ESCALA), vieram %d: %v", len(mortos), chaves(mortos))
	}
}

func chaves(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// carregaRotas devolve runbook -> SLIs que la encaminham, dos DOIS catalogos que o no
// compoe. Ler so um deles muda a resposta: foi assim que a varredura contou QUATRO runbooks
// sem produtor quando sao DOIS.
func carregaRotas() map[string][]string {
	out := map[string][]string{}
	for _, r := range otelgenai.DefaultAlertConfig().Rules {
		out[r.Route.Runbook] = append(out[r.Route.Runbook], r.SLI)
	}
	for _, r := range otelgenai.DefaultOperationalAlertConfig().Rules {
		out[r.Route.Runbook] = append(out[r.Route.Runbook], r.SLI)
	}
	return out
}
