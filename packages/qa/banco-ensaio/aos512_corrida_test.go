package bancoensaio

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// AOS-512 — A BATERIA CONTRA OS PROVIDERS FALSOS: TAXAS ESPERADAS EXACTAS.
//
// Os números destes testes foram DERIVADOS À MÃO do roteiro do provider falso e da estrutura da
// bateria, antes de o teste correr — não copiados do que o banco devolveu. A derivação está
// escrita em cada teste: se o banco mudar a ordem dos runs, a regra das novas tentativas ou a
// definição de uma taxa, é aqui que se nota.

func verTaxa(t *testing.T, nome string, taxa Taxa, numerador, denominador int) {
	t.Helper()
	if taxa.Numerador != numerador || taxa.Denominador != denominador {
		t.Errorf("%s = %d/%d, quer %d/%d", nome, taxa.Numerador, taxa.Denominador, numerador, denominador)
	}
	if denominador == 0 {
		if taxa.Valor != nil || taxa.IC95Inferior != nil || taxa.IC95Superior != nil {
			t.Errorf("%s: sem denominador nao ha taxa nem intervalo", nome)
		}
		return
	}
	if taxa.Valor == nil || taxa.IC95Inferior == nil || taxa.IC95Superior == nil {
		t.Errorf("%s: falta a taxa ou o intervalo de confianca", nome)
		return
	}
	if !(*taxa.IC95Inferior <= *taxa.Valor && *taxa.Valor <= *taxa.IC95Superior) {
		t.Errorf("%s: a taxa %.4f esta fora do seu intervalo [%.4f, %.4f]", nome, *taxa.Valor, *taxa.IC95Inferior, *taxa.IC95Superior)
	}
}

// Roteiro [cumpre, texto, cumpre] sobre T1, três amostras, até três tentativas:
//
//	amostra 0: conversa 0 (cumpre)  → pede a tool, responde          → cumprido, 2 pedidos
//	amostra 1: conversa 1 (texto)   → escreve a chamada como texto   → contract_unmet_no_call, 1 pedido
//	           conversa 2 (cumpre)  → 2.ª tentativa, com o aviso     → cumprido, 2 pedidos
//	amostra 2: conversa 3 (cumpre)  → o roteiro deu a volta          → cumprido, 2 pedidos
func TestAOS512_Falso_T1_TaxasExactas(t *testing.T) {
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre, ComportamentoTexto, ComportamentoCumpre}, nil)
	plano := PlanoDaBateria(a.bateria, BracoA, 3)
	plano.Casos = []string{"T1"}
	r := a.correr(t, plano, nil)

	if r.Terminou != TerminouCompleta || r.Taxas.N != 4 || r.Taxas.Unidades != 3 {
		t.Fatalf("terminou=%s runs=%d unidades=%d; quer completa, 4, 3", r.Terminou, r.Taxas.N, r.Taxas.Unidades)
	}
	verTaxa(t, "sem tool call na primeira", r.Taxas.SemToolCallNaPrimeira, 1, 3)
	verTaxa(t, "nome da tool no texto", r.Taxas.NomeDaToolNoTexto, 1, 1)
	verTaxa(t, "segundo turno aceite", r.Taxas.SegundoTurnoAceite, 3, 3)
	verTaxa(t, "factos ausentes", r.Taxas.FactosAusentes, 0, 3)
	verTaxa(t, "resposta vazia", r.Taxas.RespostaVazia, 0, 4)
	verTaxa(t, "cortada", r.Taxas.Cortada, 0, 4)
	verTaxa(t, "erro do provider", r.Taxas.ErroDoProvider, 0, 7)
	verTaxa(t, "cumprido a primeira", r.Taxas.CumpridoAPrimeira, 2, 3)
	verTaxa(t, "recuperado a segunda", r.Taxas.RecuperadoASegunda, 1, 1)
	verTaxa(t, "recuperado a terceira", r.Taxas.RecuperadoATerceira, 0, 0)
	if quer := map[string]int{"cumprido": 3, "contract_unmet_no_call": 1}; !reflect.DeepEqual(r.Taxas.Desfechos, quer) {
		t.Errorf("desfechos = %v, quer %v", r.Taxas.Desfechos, quer)
	}
	if quer := map[string]int{"200": 7}; !reflect.DeepEqual(r.Taxas.HTTP, quer) {
		t.Errorf("http = %v, quer %v", r.Taxas.HTTP, quer)
	}
	if quer := map[string]int{"tool_calls": 3, "stop": 4}; !reflect.DeepEqual(r.Taxas.MotivosDeParagem, quer) {
		t.Errorf("motivos de paragem = %v, quer %v", r.Taxas.MotivosDeParagem, quer)
	}
	if r.Pedidos.Enviados != 7 || a.falso.Pedidos() != 7 {
		t.Errorf("pedidos: relatorio %d, falso %d; quer 7", r.Pedidos.Enviados, a.falso.Pedidos())
	}
	// A 2.ª tentativa leva o aviso de nova tentativa do kernel; a 1.ª não.
	corpos := a.falso.Corpos()
	if strings.Contains(string(corpos[2]), "about=previous_attempt") || !strings.Contains(string(corpos[3]), "about=previous_attempt") {
		t.Errorf("o aviso de nova tentativa tinha de ir so na segunda tentativa")
	}
	// A tool call em texto NÃO foi executada: o falso só viu a mensagem tool depois de uma
	// tool call nativa (3 runs), nunca no run que a escreveu como texto.
	comMensagemTool := 0
	for _, c := range corpos {
		if strings.Contains(string(c), `"role":"tool"`) {
			comMensagemTool++
		}
	}
	if comMensagemTool != 3 {
		t.Errorf("pedidos com mensagem tool = %d, quer 3 (um por run que pediu a tool nativamente)", comMensagemTool)
	}
}

// A bateria inteira, duas passagens, com o roteiro por omissão (dez comportamentos). Cada run é
// uma conversa, pela ordem:
//
//	passagem 0
//	 c0 cumpre          T1 ler         cumprido                       2 pedidos
//	 c1 cumpre          T2 ler         cumprido                       2
//	 c2 texto           T2 resumir     (sem tools: responde) cumprido 1
//	 c3 cumpre          T3 resumir     cumprido                       1
//	 c4 vazia           T4 ler-dois    pede 2 tools; resposta vazia   2   → empty_output
//	 c5 cumpre          T4, 2.ª tent.  cumprido                       2
//	 c6 cortada         T5             pede 2 tools; cortada          2   → truncated
//	 c7 sem_factos      T6             pede a tool; texto sem factos  2   → cumprido, factos ausentes
//	passagem 1
//	 c8 erro_500        T1 ler         500                            1   → erro_http, 0 turnos
//	 c9 rejeita_segundo T2 ler         pede a tool; 400 ao 2.º turno  2   → erro_http (o resumir não corre)
//	 c10 cumpre         T3 resumir     cumprido                       1
//	 c11 cumpre         T4 ler-dois    cumprido                       2
//	 c12 texto          T5             chamada em texto               1   → contract_unmet_no_call
//	 c13 cumpre         T5, 2.ª tent.  cumprido                       2
//	 c14 vazia          T6             pede a tool; resposta vazia    2   → empty_output
//	 c15 cumpre         T6, 2.ª tent.  cumprido                       2
//
// 16 runs, 13 unidades, 27 pedidos.
func TestAOS512_Falso_BateriaInteira_TaxasExactas(t *testing.T) {
	a := novoAmbienteFalso(t, RoteiroPorOmissao(), nil)
	r := a.correr(t, PlanoDaBateria(a.bateria, BracoA, 2), nil)

	if r.Terminou != TerminouCompleta || r.Taxas.N != 16 || r.Taxas.Unidades != 13 {
		t.Fatalf("terminou=%s runs=%d unidades=%d; quer completa, 16, 13", r.Terminou, r.Taxas.N, r.Taxas.Unidades)
	}
	// Exigem tool e tiveram pelo menos um turno, à 1.ª tentativa: T1, T2-ler, T4, T5, T6 na
	// passagem 0; T2-ler, T4, T5, T6 na passagem 1 (o T1 levou 500 e não teve turno).
	verTaxa(t, "sem tool call na primeira", r.Taxas.SemToolCallNaPrimeira, 1, 9)
	verTaxa(t, "nome da tool no texto", r.Taxas.NomeDaToolNoTexto, 1, 1)
	// Enviaram a mensagem tool: c0, c1, c4, c5, c6, c7, c9, c11, c13, c14, c15; só c9 foi recusado.
	verTaxa(t, "segundo turno aceite", r.Taxas.SegundoTurnoAceite, 10, 11)
	// Concluíram, em nós com factos: c0, c1, c2, c3, c5, c7, c10, c11, c13, c15; só c7 sem eles.
	verTaxa(t, "factos ausentes", r.Taxas.FactosAusentes, 1, 10)
	verTaxa(t, "resposta vazia", r.Taxas.RespostaVazia, 2, 15)
	verTaxa(t, "cortada", r.Taxas.Cortada, 1, 15)
	verTaxa(t, "erro do provider", r.Taxas.ErroDoProvider, 2, 27)
	verTaxa(t, "cumprido a primeira", r.Taxas.CumpridoAPrimeira, 7, 13)
	verTaxa(t, "recuperado a segunda", r.Taxas.RecuperadoASegunda, 3, 3)
	verTaxa(t, "recuperado a terceira", r.Taxas.RecuperadoATerceira, 0, 0)
	if quer := map[string]int{"cumprido": 10, "empty_output": 2, "truncated": 1, "erro_http": 2, "contract_unmet_no_call": 1}; !reflect.DeepEqual(r.Taxas.Desfechos, quer) {
		t.Errorf("desfechos = %v, quer %v", r.Taxas.Desfechos, quer)
	}
	if quer := map[string]int{"200": 25, "400": 1, "500": 1}; !reflect.DeepEqual(r.Taxas.HTTP, quer) {
		t.Errorf("http = %v, quer %v", r.Taxas.HTTP, quer)
	}
	// Um motivo por turno. tool_calls: os 11 runs que pediram tools. length: c6. stop: os 13
	// turnos finais sem corte (c0–c5, c7, c10–c15, menos... ver a soma: 25 turnos ao todo).
	if quer := map[string]int{"tool_calls": 11, "length": 1, "stop": 13}; !reflect.DeepEqual(r.Taxas.MotivosDeParagem, quer) {
		t.Errorf("motivos de paragem = %v, quer %v", r.Taxas.MotivosDeParagem, quer)
	}
	if r.Pedidos.Enviados != 27 || a.falso.Pedidos() != 27 || r.Pedidos.PrevistosMax != 2*7*3*4 {
		t.Errorf("pedidos: enviados %d, falso %d, previstos %d; quer 27, 27, 168", r.Pedidos.Enviados, a.falso.Pedidos(), r.Pedidos.PrevistosMax)
	}

	// A FICHA DA FORMA é a do AOS-507, agregada por classe. A resposta vazia tem de aparecer
	// como «content vazio, raciocínio em reasoning_content» (a forma H1), duas vezes.
	vaziaH1 := "content=vazio reasoning=reasoning_content refusal=ausente psf_refusal=nenhum psf_reasoning=nenhum tool_calls=0"
	if r.Taxas.Fichas[vaziaH1] != 2 {
		t.Errorf("fichas: a forma H1 aparece %d vezes, quer 2; fichas = %v", r.Taxas.Fichas[vaziaH1], r.Taxas.Fichas)
	}
	if r.Taxas.Fichas[FichaSemResposta] != 2 {
		t.Errorf("fichas: os 2 pedidos sem 200 tinham de contar como %s; fichas = %v", FichaSemResposta, r.Taxas.Fichas)
	}
	total := 0
	for classe, n := range r.Taxas.Fichas {
		total += n
		if strings.ContainsAny(classe, "\n\"{}") || len(classe) > 160 {
			t.Errorf("classe de ficha fora do vocabulario: %q", classe)
		}
	}
	if total != 27 {
		t.Errorf("fichas: %d no total, quer uma por pedido (27)", total)
	}
	if len(r.PorCaso) != 6 || r.PorCaso[0].Caso != "T1" || r.PorCaso[0].Taxas.N != 2 {
		t.Errorf("taxas por caso mal formadas")
	}
	if r.Digests.Bateria != a.bateria.Digest() || !strings.HasPrefix(r.Digests.Rota, "sha256:") || !strings.HasPrefix(r.Digests.Configuracao, "sha256:") {
		t.Errorf("o relatorio tem de levar os tres digests: %+v", r.Digests)
	}
}

// T5: a tool negada. O Reference Monitor nega a leitura do documento reservado ANTES do
// despacho — o conteúdo dele nunca chega ao provider — e o run continua e conclui.
func TestAOS512_Falso_T5_ToolNegadaEContinuacao(t *testing.T) {
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	plano := PlanoDaBateria(a.bateria, BracoA, 1)
	plano.Casos = []string{"T5"}
	r := a.correr(t, plano, nil)
	o := r.Observacoes[0]
	if o.Desfecho != DesfechoCumprido || o.ToolCalls != 2 || o.Factos != FactosPresentes || o.SegundoTurno != SegundoTurnoAceite {
		t.Fatalf("T5: %+v", o)
	}
	corpos := a.falso.Corpos()
	segundo := string(corpos[1])
	if !strings.Contains(segundo, "tool_denied") {
		t.Errorf("o segundo pedido tinha de levar o resultado da tool negada, com o rotulo tool_denied")
	}
	if strings.Contains(segundo, "ZIMBRO-7741") {
		t.Errorf("o conteudo do documento negado chegou ao provider")
	}
	if !strings.Contains(segundo, "LT-4817") {
		t.Errorf("o documento permitido tinha de chegar ao provider")
	}
}

// T6: argumentos grandes. O segundo pedido leva o substituto do runtime, e não os 8 KB.
func TestAOS512_Falso_T6_ArgumentosGrandes(t *testing.T) {
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	plano := PlanoDaBateria(a.bateria, BracoA, 1)
	plano.Casos = []string{"T6"}
	r := a.correr(t, plano, nil)
	o := r.Observacoes[0]
	if o.Desfecho != DesfechoCumprido || o.ToolCalls != 1 || o.SegundoTurno != SegundoTurnoAceite || o.Factos != FactosPresentes {
		t.Fatalf("T6: %+v", o)
	}
	var segundo struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(a.falso.Corpos()[1], &segundo); err != nil {
		t.Fatal(err)
	}
	achado := false
	for _, m := range segundo.Messages {
		for _, tc := range m.ToolCalls {
			achado = true
			if !strings.Contains(tc.Function.Arguments, "aos_args_omitted_bytes") || len(tc.Function.Arguments) > 200 {
				t.Errorf("os argumentos da tool call no segundo pedido tinham de ser o substituto do runtime: %d bytes", len(tc.Function.Arguments))
			}
		}
	}
	if !achado {
		t.Fatal("o segundo pedido nao leva o assistant com a tool call")
	}
}

// T2: o resumo consome a saída da leitura como plan_input, sem tools.
func TestAOS512_Falso_T2_OResumoConsomeALeitura(t *testing.T) {
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	plano := PlanoDaBateria(a.bateria, BracoA, 1)
	plano.Casos = []string{"T2"}
	r := a.correr(t, plano, nil)
	if len(r.Observacoes) != 2 || r.Observacoes[1].No != "resumir" || r.Observacoes[1].Desfecho != DesfechoCumprido || r.Observacoes[1].Pedidos != 1 {
		t.Fatalf("T2: %+v", r.Observacoes)
	}
	terceiro := string(a.falso.Corpos()[2])
	if !strings.Contains(terceiro, "plan_input") || !strings.Contains(terceiro, "from=ler") || strings.Contains(terceiro, `"tools"`) {
		t.Errorf("o pedido do no de resumo tinha de levar o plan_input do no ler e nenhuma tool")
	}
}

// A EXPERIÊNCIA DOS SEPARADORES: 53 por braço, 212 pedidos, um turno por amostra, intercalados.
func TestAOS512_Separadores_PlanoEIntercalacao(t *testing.T) {
	a := novoAmbienteFalso(t, RoteiroPorOmissao(), nil)
	plano := PlanoDosSeparadores(20261008)
	previstos, err := plano.PedidosMaximos(a.bateria)
	if err != nil || previstos != 212 {
		t.Fatalf("pedidos previstos = %d (%v), quer 212", previstos, err)
	}
	r := a.correr(t, plano, nil)
	if r.Pedidos.Enviados != 212 || a.falso.Pedidos() != 212 || len(r.Observacoes) != 212 {
		t.Fatalf("pedidos %d, falso %d, runs %d; quer 212 em todos", r.Pedidos.Enviados, a.falso.Pedidos(), len(r.Observacoes))
	}
	if r.Plano.Semente != 20261008 {
		t.Errorf("a semente nao foi para o relatorio")
	}
	// INTERCALADOS: cada bloco de quatro runs seguidos tem os quatro braços, uma vez cada.
	ordens := map[string]bool{}
	for i := 0; i < len(r.Observacoes); i += 4 {
		vistos := map[string]bool{}
		ordem := ""
		for _, o := range r.Observacoes[i : i+4] {
			vistos[o.Braco] = true
			ordem += o.Braco
			if o.Caso != CasoDosSeparadores || o.Tentativa != 1 || o.Pedidos != 1 || o.Turnos > 1 {
				t.Fatalf("cada amostra e um run de T1, uma tentativa, um pedido: %+v", o)
			}
		}
		if len(vistos) != 4 {
			t.Fatalf("o bloco %d nao tem os quatro bracos: %s", i/4, ordem)
		}
		ordens[ordem] = true
	}
	if len(ordens) < 6 {
		t.Errorf("a ordem dentro dos blocos quase nao varia (%d ordens distintas): nao esta baralhada", len(ordens))
	}
	for _, b := range r.PorBraco {
		if b.Taxas.N != 53 {
			t.Errorf("braco %s: n = %d, quer 53", b.Braco, b.Taxas.N)
		}
		if b.Taxas.SemToolCallNaPrimeira.Valor == nil || b.Taxas.SemToolCallNaPrimeira.IC95Inferior == nil {
			t.Errorf("braco %s: falta a taxa ou o intervalo de confianca", b.Braco)
		}
	}
	// O roteiro tem 10 conversas com UMA chamada em texto e UM erro 500; 212 conversas ⇒ 21
	// de cada (as conversas 2, 12, …, 202 e 8, 18, …, 208), e 22 nas que caem antes do resto.
	verTaxa(t, "sem tool call na primeira (todos os bracos)", r.Taxas.SemToolCallNaPrimeira, 21, 191)
	if r.Taxas.HTTP["500"] != 21 || r.Taxas.HTTP["200"] != 191 {
		t.Errorf("http = %v, quer 191 de 200 e 21 de 500", r.Taxas.HTTP)
	}
	if len(r.Comparacoes) != 3 {
		t.Fatalf("comparacoes com o braco A: %d, quer 3 (B, C e D)", len(r.Comparacoes))
	}
	for _, c := range r.Comparacoes {
		if c.Diferenca == nil || c.PValor == nil || c.PValorCorrigido == nil || c.DistingueA5 == nil || c.IntervalosSobrepostos == nil ||
			*c.PValor < 0 || *c.PValor > 1 || *c.PValorCorrigido < *c.PValor || *c.PValorCorrigido > 1 || *c.DistingueA5 != (*c.PValorCorrigido < 0.05) {
			t.Errorf("comparacao %s mal formada", c.Braco)
		}
	}

	// A MESMA semente dá a MESMA ordem; outra semente dá outra.
	ordemDe := func(semente uint64) string {
		var s strings.Builder
		for _, p := range PlanoDosSeparadores(semente).sequencia(a.bateria) {
			s.WriteString(string(p.braco))
		}
		return s.String()
	}
	primeira, repetida, outra := ordemDe(1), ordemDe(1), ordemDe(2)
	if primeira != repetida || primeira == outra {
		t.Errorf("a ordem tem de ser funcao da semente")
	}
}

func TestAOS512_Wilson_ValoresConhecidos(t *testing.T) {
	perto := func(a, b float64) bool { return a-b < 0.0005 && b-a < 0.0005 }
	// Valores de referência do intervalo de Wilson a 95% (z = 1,96), calculados à parte.
	for _, c := range []struct {
		x, n     int
		inf, sup float64
	}{
		{0, 10, 0, 0.2775}, {10, 10, 0.7225, 1}, {5, 53, 0.0410, 0.2025}, {17, 53, 0.2109, 0.4548}, {1, 1, 0.2065, 1},
	} {
		tx := NovaTaxa("t", c.x, c.n)
		if !perto(*tx.IC95Inferior, c.inf) || !perto(*tx.IC95Superior, c.sup) {
			t.Errorf("Wilson(%d/%d) = [%.4f, %.4f], quer [%.4f, %.4f]", c.x, c.n, *tx.IC95Inferior, *tx.IC95Superior, c.inf, c.sup)
		}
	}
	if tx := NovaTaxa("t", 0, 0); tx.Valor != nil {
		t.Error("0/0 nao tem taxa")
	}
}

// O p-valor é o do teste EXACTO de Fisher (bilateral), com a correcção de Holm para as três
// comparações com o braço A. Os valores de referência foram calculados à parte, com
// combinações inteiras exactas.
func TestAOS512_Fisher_EHolm(t *testing.T) {
	perto := func(a, b float64) bool { return math.Abs(a-b) < 0.0005 }
	for _, c := range []struct {
		x1, n1, x2, n2 int
		quer           float64
	}{
		// O caso da revisão: a aproximação normal dava 0,041 e «distingue»; o exacto não.
		{0, 53, 4, 53, 0.1179},
		{4, 53, 0, 53, 0.1179},
		// 10% contra 32%: a diferença que a amostra foi dimensionada para ver.
		{5, 53, 17, 53, 0.0075},
		{10, 53, 20, 53, 0.0513},
		{5, 53, 6, 53, 1},
		{0, 53, 0, 53, 1},
		{53, 53, 53, 53, 1},
		{3, 10, 3, 10, 1},
	} {
		if got := pValorDeFisher(c.x1, c.n1, c.x2, c.n2); !perto(got, c.quer) {
			t.Errorf("Fisher(%d/%d contra %d/%d) = %.4f, quer %.4f", c.x1, c.n1, c.x2, c.n2, got, c.quer)
		}
	}
	if p := pValorDeFisher(1, 0, 1, 5); p != 1 {
		t.Errorf("sem denominador o p-valor e 1, veio %.4f", p)
	}
	// Holm: 0,01, 0,04 e 0,03 ⇒ 0,03 (×3), 0,06 (0,03×2) e 0,06 (0,04×1, que não desce abaixo
	// do anterior).
	got := corrigirPorHolm([]float64{0.01, 0.04, 0.03})
	for i, quer := range []float64{0.03, 0.06, 0.06} {
		if !perto(got[i], quer) {
			t.Errorf("Holm = %v, quer [0.03 0.06 0.06]", got)
			break
		}
	}
	if got := corrigirPorHolm([]float64{0.5, 0.9}); got[0] != 1 || got[1] != 1 {
		t.Errorf("o p-valor corrigido nao passa de 1: %v", got)
	}

	// No relatório: 0/53 contra 4/53 NÃO distingue; e um p de 0,03 isolado, com três
	// comparações, também não (0,03 × 3 = 0,09).
	braco := func(nome string, falhas int) TaxasDoBraco {
		return TaxasDoBraco{Braco: nome, Taxas: Taxas{SemToolCallNaPrimeira: NovaTaxa("t", falhas, 53)}}
	}
	cs := compararComA([]TaxasDoBraco{braco("A", 4), braco("B", 0), braco("C", 4), braco("D", 17)})
	if len(cs) != 3 {
		t.Fatalf("comparacoes: %d", len(cs))
	}
	if *cs[0].DistingueA5 || !perto(*cs[0].PValor, 0.1179) || !perto(*cs[0].PValorCorrigido, 0.2358) {
		t.Errorf("B (0/53) contra A (4/53): p=%.4f corrigido=%.4f distingue=%v; quer 0,1179, 0,2358 e false", *cs[0].PValor, *cs[0].PValorCorrigido, *cs[0].DistingueA5)
	}
	// D (17/53) contra A (4/53) tem o menor p-valor dos três, logo o corrigido é o triplo.
	if !*cs[2].DistingueA5 || !perto(*cs[2].PValorCorrigido, 3**cs[2].PValor) {
		t.Errorf("D contra A: p=%.4f corrigido=%.4f distingue=%v", *cs[2].PValor, *cs[2].PValorCorrigido, *cs[2].DistingueA5)
	}
}
