package main

// O CONTRATO DE CONCLUSÃO, DO LADO DO `aos-orq` (AOS-495).
//
// Para o `aos-orq`, um nó do plano estava concluído quando o run filho respondia `completed`,
// `terminated` e sem erro. Nada perguntava o que o run fez. Medido em produção a 2026-10-04: em
// dois de dez planos o nó de leitura respondeu num turno, com a chamada à tool escrita como
// texto; o texto foi publicado como saída do nó e os planos saíram com 0.
//
// O kernel do nó passou a julgar o desfecho de um run contra um contrato de conclusão (AOS-493,
// ADR-037), e o nó passou a aceitar o contrato e a devolver a razão (AOS-494). Este ficheiro é
// o que o `aos-orq` faz com isso:
//
//   - DECLARA o contrato dos nós elegíveis, e só a um nó que anuncie aceitá-lo — um anúncio
//     que não se leu não é um «não»: o `serve` pára e o pedido volta à fila;
//   - regista a CAUSA de cada nó que fecha `failed`, num vocabulário fechado, e leva-a ao
//     `detail` do desfecho do plano;
//   - nunca publica uma saída VAZIA.
//
// O QUE NÃO MUDA, DE PROPÓSITO. O nó do plano conclui pela regra de sempre — `completed`,
// `terminated`, sem erro ([estadoDoRun.concluiu]). A razão do veredicto é informação: em modo de
// observação o nó devolve-a ao lado de um run concluído, e o nó do plano conclui. E o `aos-orq`
// não olha para a forma do texto: quem julga se o run cumpriu é o kernel do nó, pelos seus
// contadores.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Causas de um nó do plano `failed`, como o `aos-orq` as nomeia. Com as razões do veredicto do
// kernel ([agentruntime.OutcomeReasons]) formam o VOCABULÁRIO FECHADO do `causa=` do desfecho:
// o `detail` fica em claro no nó, e nunca leva texto de terceiros.
const (
	// causaSaidaVazia — o run concluiu e o nó declara uma saída de forma aberta, mas o texto
	// final veio vazio. Não se publica; quem falha é o produtor.
	causaSaidaVazia = "saida_vazia"
	// causaSaidaIndisponivel — o run concluiu e o nó `aos` já não consegue servir a saída
	// (`output_unavailable`): o desfecho saiu da memória dele e a captura não se leu do log.
	causaSaidaIndisponivel = "saida_indisponivel"
	// causaRunNaoConcluido — o run filho acabou sem concluir e sem razão de veredicto: erro de
	// loop, orçamento ou turnos esgotados, run morto.
	causaRunNaoConcluido = "run_nao_concluido"
	// causaRunPerdido — o nó `aos` deixou de conhecer o run filho (404 persistente).
	causaRunPerdido = "run_perdido"
	// causaEntradaPorCumprir — o nó não correu: um contrato do seu `consumes` ficou por cumprir.
	causaEntradaPorCumprir = "entrada_por_cumprir"
	// causaRazaoDesconhecida — o nó `aos` devolveu uma razão fora do vocabulário do kernel que
	// este binário conhece. O texto dela não é repetido.
	causaRazaoDesconhecida = "razao_desconhecida"
	// causaNaoRegistada — o nó do plano já vinha `failed` do log: fechou-o um `serve` anterior,
	// e a causa vivia na memória dele.
	causaNaoRegistada = "nao_registada"
)

// causaConclusaoNaoCumprida é o valor do sufixo `causa=` da linha `aviso:` (AOS-495): pelo
// menos um nó do plano falhou por a CONCLUSÃO não se ter cumprido — veredicto negativo do
// kernel, ou saída vazia ou indisponível. É um valor fixo, e o único: as contagens por causa
// não saem do servidor.
const causaConclusaoNaoCumprida = "conclusao_nao_cumprida"

// causaDaConclusao diz se a causa é da classe «a conclusão não se cumpriu».
func causaDaConclusao(causa string) bool {
	switch causa {
	case causaSaidaVazia, causaSaidaIndisponivel:
		return true
	}
	// AOS-501: uma saída por referência que não se entrega é a conclusão por cumprir.
	for _, c := range causasDaOrigem {
		if causa == c {
			return true
		}
	}
	for _, r := range agentruntime.OutcomeReasons() {
		if causa == string(r) {
			return true
		}
	}
	return false
}

// causaDoRunFilho classifica um run filho que NÃO concluiu. A razão do veredicto do nó só
// passa se for do vocabulário do kernel; tudo o resto é um nome deste ficheiro.
func causaDoRunFilho(st estadoDoRun, existe bool) string {
	if !existe {
		return causaRunPerdido
	}
	if st.OutcomeReason == "" {
		return causaRunNaoConcluido
	}
	razao := agentruntime.OutcomeReason(st.OutcomeReason)
	if razao == agentruntime.OutcomeFulfilled || !razao.NoVocabulario() {
		return causaRazaoDesconhecida
	}
	return st.OutcomeReason
}

// Classes de um nó do plano face ao contrato de conclusão (AOS-495). É o que cada nó diz no log
// da drenagem quando é submetido, e o rótulo `classe` de [metricaNosPorContrato]. Vocabulário
// FECHADO: cinco valores, todos deste ficheiro.
const (
	// classeComContratoSaidaAberta — leva contrato e declara uma saída de forma aberta: além do
	// contrato, uma saída vazia fecha-o `failed`.
	classeComContratoSaidaAberta = "com_contrato_saida_aberta"
	// classeComContratoSemSaida — leva contrato e não declara saída de forma aberta: o último nó
	// do plano, o plano de um só nó, o nó de escrita. Pode concluir sem texto.
	classeComContratoSemSaida = "com_contrato_sem_saida"
	// classeSemContratoVerificador — um verificador julga muitas vezes só com os `inputs`.
	classeSemContratoVerificador = "sem_contrato_verificador"
	// classeSemContratoSemTools — sem tools atribuídas não há de que a conclusão dependa.
	classeSemContratoSemTools = "sem_contrato_sem_tools"
	// classeSemContratoNaoAnunciado — o nó do plano era elegível, e o nó `aos` não anuncia o
	// suporte (anterior ao AOS-494): o campo não vai, porque dava 400.
	classeSemContratoNaoAnunciado = "sem_contrato_no_nao_anuncia"
)

// classesDoContrato é a lista fechada das classes, pela ordem em que se documentam.
var classesDoContrato = []string{
	classeComContratoSaidaAberta, classeComContratoSemSaida,
	classeSemContratoVerificador, classeSemContratoSemTools, classeSemContratoNaoAnunciado,
}

// contratoDoNo devolve o contrato de conclusão de um nó ELEGÍVEL (AOS-495), ou nil. É elegível
// TODO o nó que:
//
//   - não é verificador: um verificador julga muitas vezes só com os `inputs`;
//   - tem tools atribuídas no plano materializado (`tools`, os nomes da lista-branca do run).
//
// COM OU SEM `outputs`. A primeira versão exigia também uma saída de forma aberta declarada, e a
// revisão adversarial mostrou o que isso deixava de fora: o planeador só declara `outputs` quando
// outro nó os consome (regras 7 e 12 do prompt), pelo que o plano de um só nó, o último nó e os
// nós de escrita ficavam sem contrato — a classe mais cara, a de um nó que diz «feito» sem ter
// escrito. Reproduzido: nó com `doc_read` e sem `outputs`, resposta de produção, plano 0.
//
// O contrato é a lista inteira das tools atribuídas: o plano não diz qual delas é o trabalho do
// nó. Uma tool atribuída que o objectivo afinal não precisava dá um vermelho falso — resíduo
// declarado no ADR-037 §5, e é por isso que o nó `aos` corre primeiro em observação. O
// alargamento aumenta esse resíduo, e o dono valida a classe alargada antes de ligar `enforce`.
func contratoDoNo(n plan.Node, tools []string) []string {
	if n.IsVerifier() || len(tools) == 0 {
		return nil
	}
	return tools
}

// classeDoContrato diz em que classe o nó do plano fica. `anunciado` é o que o nó `aos`
// respondeu no anúncio ([nodeClient.ContratoDeConclusao]).
func classeDoContrato(n plan.Node, tools []string, anunciado bool) string {
	switch {
	case n.IsVerifier():
		return classeSemContratoVerificador
	case len(tools) == 0:
		return classeSemContratoSemTools
	case !anunciado:
		return classeSemContratoNaoAnunciado
	case produzSaidaAberta(n):
		return classeComContratoSaidaAberta
	default:
		return classeComContratoSemSaida
	}
}

// produzSaidaAberta diz se o nó declara pelo menos uma saída de forma aberta — a que se
// publica a partir do texto final do run.
func produzSaidaAberta(n plan.Node) bool {
	for _, o := range n.Outputs {
		if !o.Type.ClosedForm() {
			return true
		}
	}
	return false
}

// anuncioDoNo é o que o nó `aos` anuncia sobre o contrato de conclusão.
type anuncioDoNo struct {
	// aceita — o nó devolveu `completion_contract` no `GET /tools`: aceita `completion_requires`
	// no `POST /runs`. Falso num nó anterior ao AOS-494, que recusaria o campo com 400.
	aceita bool
	// modo é o que o nó diz fazer com o veredicto (`observe`, `enforce`, `off`). Só se imprime.
	modo string
	// origem — o nó devolveu `output_source` no `GET /tools` e diz aceitar o vínculo «só medição»
	// (AOS-498): aceita `output_from_tool` no `POST /runs`. Falso num nó anterior, e num nó com o
	// veredicto desligado. Lê-se na MESMA resposta do contrato: um anúncio que não se leu pára o
	// `serve` para os dois.
	origem bool
	// vinculativa — o mesmo anúncio diz aceitar o vínculo VINCULATIVO (`binding`): é a condição
	// de a entrega por referência estar activa (AOS-501). Um nó que só anunciasse `measure` não
	// julga a origem, e por ele não se entrega.
	vinculativa bool
	// tentativas (AOS-503) é o tecto de tentativas a mais por nó do plano que o nó `aos` anuncia
	// (`run_retry.max` do `GET /tools`, AOS-502). Zero ⇒ o nó não anuncia o suporte — um nó anterior,
	// ou com o tecto a zero —, e nenhuma tentativa é pedida. Lê-se na MESMA resposta.
	tentativas int
	// tentativaVazia (AOS-511): o mesmo anúncio diz que o nó admite a nova tentativa de um run que
	// fechou `empty_output` (`run_retry.empty_output`, AOS-510). Falso ⇒ um nó anterior, ou com
	// esse interruptor desligado: essa tentativa não é pedida.
	tentativaVazia bool
}

// ContratoDeConclusao lê do `GET /tools` se o nó aceita o contrato de conclusão.
//
// É a rota que o `serve` já lê antes de submeter (a conferência do snapshot, AOS-441), com a
// mesma credencial. Lê-se por `serve`, logo por plano: um nó trocado entre dois planos é
// perguntado outra vez.
//
// DUAS RESPOSTAS, QUE NÃO SE CONFUNDEM:
//
//   - `(anuncio, nil)` — o nó RESPONDEU. Com `aceita` falso, respondeu sem `completion_contract`:
//     é um nó anterior ao AOS-494, e os nós do plano vão sem contrato;
//   - `(_, erro)` — o anúncio NÃO SE LEU (rede, 429, 5xx, corpo ilegível). Não é o nó a dizer
//     que não. Quem chama NÃO pode tratá-lo como «não anunciado»: era desligar o contrato do
//     plano inteiro por uma leitura falhada, e quem conseguisse provocar um 429 nesta rota
//     desligava-o sem credencial nenhuma. O `serve` pára com [errAnuncioIlegivel] e o pedido
//     volta à fila.
func (c *nodeClient) ContratoDeConclusao(ctx context.Context) (anuncioDoNo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/tools", nil)
	if err != nil {
		return anuncioDoNo{}, err
	}
	if err := c.autenticar(ctx, req); err != nil {
		return anuncioDoNo{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return anuncioDoNo{}, fmt.Errorf("anúncio do contrato de conclusão: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return anuncioDoNo{}, fmt.Errorf("anúncio do contrato de conclusão: GET /tools deu HTTP %d", resp.StatusCode)
	}
	var corpo struct {
		CompletionContract *struct {
			Mode string `json:"mode"`
		} `json:"completion_contract"`
		OutputSource *struct {
			Bindings []string `json:"bindings"`
		} `json:"output_source"`
		RunRetry *struct {
			Max         int  `json:"max"`
			EmptyOutput bool `json:"empty_output"`
		} `json:"run_retry"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&corpo); err != nil {
		return anuncioDoNo{}, fmt.Errorf("anúncio do contrato de conclusão: resposta ilegível: %w", err)
	}
	var anuncio anuncioDoNo
	if corpo.RunRetry != nil && corpo.RunRetry.Max > 0 {
		// O valor vem de outro processo: fica dentro do que este binário sabe usar.
		anuncio.tentativas = corpo.RunRetry.Max
		if anuncio.tentativas > maxTentativasAMaisPorNo {
			anuncio.tentativas = maxTentativasAMaisPorNo
		}
		// AOS-511: a segunda classe só existe dentro do anúncio do tecto.
		anuncio.tentativaVazia = corpo.RunRetry.EmptyOutput
	}
	if corpo.OutputSource != nil {
		// A presença é o anúncio; o vínculo que este binário envia tem de constar dos aceites.
		for _, v := range corpo.OutputSource.Bindings {
			if v == string(agentruntime.OutputSourceMeasure) {
				anuncio.origem = true
			}
			if v == string(agentruntime.OutputSourceBinds) {
				anuncio.vinculativa = true
			}
		}
	}
	if corpo.CompletionContract == nil {
		return anuncio, nil
	}
	anuncio.aceita, anuncio.modo = true, modoImprimivel(corpo.CompletionContract.Mode)
	return anuncio, nil
}

// modoImprimivel reduz o modo anunciado a um dos três que este binário conhece. O valor vem de
// outro processo e vai para o log: um modo desconhecido imprime-se como tal, sem o repetir.
func modoImprimivel(modo string) string {
	if _, err := agentruntime.ParseCompletionMode(modo); err != nil {
		return "desconhecido"
	}
	return modo
}

// errAnuncioIlegivel — o `serve` não conseguiu ler do nó `aos` se ele aceita o contrato de
// conclusão. É TRANSITÓRIO (saída 1): o `serve` pára antes da posse e antes de planear, e o
// `consume` devolve o pedido à fila. Correr sem contrato era falhar aberto num controlo cujo
// objectivo é não haver verdes falsos.
var errAnuncioIlegivel = errors.New("aos-orq: o anuncio do contrato de conclusao nao se leu do no")

// Motivos por que o contrato não foi aplicado a uma execução de plano — o rótulo `motivo` de
// [metricaContratoNaoAplicado]. Vocabulário fechado.
const (
	// motivoNoNaoAnuncia — o nó respondeu sem `completion_contract`; o plano correu sem contrato.
	motivoNoNaoAnuncia = "no_nao_anuncia"
	// motivoAnuncioIlegivel — o anúncio não se leu; o plano NÃO correu e o pedido voltou à fila.
	motivoAnuncioIlegivel = "anuncio_ilegivel"
)

// motivosDoContratoNaoAplicado é a lista fechada dos motivos.
var motivosDoContratoNaoAplicado = []string{motivoNoNaoAnuncia, motivoAnuncioIlegivel}

// bannerDoContrato declara, no arranque do `serve`, se os nós elegíveis vão levar contrato. Só
// se chama com um anúncio que se LEU: o que não se leu pára o `serve` ([errAnuncioIlegivel]).
func bannerDoContrato(a anuncioDoNo) string {
	if !a.aceita {
		return "contrato de conclusao (AOS-495): NAO APLICADO — o no nao anuncia o suporte (GET /tools sem completion_contract: no anterior ao AOS-494). Os nos do plano sao submetidos sem contrato, como antes: enviar o campo dava 400 em todas as submissoes"
	}
	return fmt.Sprintf("contrato de conclusao (AOS-495): DECLARADO — cada no nao-verificador com tools atribuidas leva as suas tools como contrato de conclusao, com ou sem saida declarada. O no anuncia o modo %q: em enforce um run que nao o cumpra fecha failed e o plano sai 13; em observe o no do plano conclui como antes e o veredicto observado fica neste log", a.modo)
}

// vectorDoRunFilho escreve o vector do veredicto numa linha de log: por tool do contrato, as
// chamadas pedidas, efectivas, negadas e falhadas. Só números, e os nomes de tool que este
// processo enviou no contrato — os do nó não se repetem.
func vectorDoRunFilho(v *agentruntime.Verdict, contrato map[string]bool) string {
	if v == nil || len(v.Tools) == 0 {
		return "[sem contrato]"
	}
	partes := make([]string, 0, len(v.Tools))
	for _, l := range v.Tools {
		nome := "outra"
		if contrato[l.Tool] {
			nome = l.Tool
		}
		partes = append(partes, fmt.Sprintf("%s pedidas=%d efectivas=%d negadas=%d falhadas=%d", nome, l.Requested, l.Effective, l.Denied, l.Failed))
	}
	return "[" + strings.Join(partes, "; ") + "]"
}

// erroDeNosFalhados é o [errNosFalhados] com as causas dos nós que falharam. O `consume` lê-as
// para o resumo do desfecho; quem só pergunta `errors.Is(err, errNosFalhados)` não muda.
type erroDeNosFalhados struct {
	msg string
	// causas conta os nós `failed` por causa, no vocabulário fechado deste ficheiro.
	causas map[string]int
}

func (e *erroDeNosFalhados) Error() string { return e.msg }
func (e *erroDeNosFalhados) Unwrap() error { return errNosFalhados }

// causasDosFalhados agrega as causas dos nós `failed` do plano. Um nó cuja causa este processo
// não registou (fechou-o um `serve` anterior) conta como [causaNaoRegistada].
func causasDosFalhados(falhados []string, porNo map[string]string) map[string]int {
	causas := map[string]int{}
	for _, id := range falhados {
		c := porNo[id]
		if c == "" {
			c = causaNaoRegistada
		}
		causas[c]++
	}
	return causas
}

// linhaDasCausas é a forma do `causa=` do resumo: `nome:n` separados por vírgula, por ordem
// alfabética. Vazio sem causas.
func linhaDasCausas(causas map[string]int) string {
	nomes := make([]string, 0, len(causas))
	for c := range causas {
		nomes = append(nomes, c)
	}
	sort.Strings(nomes)
	partes := make([]string, 0, len(nomes))
	for _, c := range nomes {
		partes = append(partes, c+":"+strconv.Itoa(causas[c]))
	}
	return strings.Join(partes, ",")
}

// causaDoAviso devolve o sufixo de causa da linha `aviso:` para um desfecho: o valor fixo
// [causaConclusaoNaoCumprida] quando o plano saiu com nós falhados e pelo menos um falhou por a
// conclusão não se ter cumprido; vazio em todos os outros casos.
func causaDoAviso(codigo int, causas map[string]int) string {
	if codigo != exitNosFalhados {
		return ""
	}
	for c, n := range causas {
		if n > 0 && causaDaConclusao(c) {
			return causaConclusaoNaoCumprida
		}
	}
	return ""
}
