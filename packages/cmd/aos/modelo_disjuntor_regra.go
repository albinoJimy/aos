package main

// modelo_disjuntor_regra.go — A REGRA DO DISJUNTOR DO CANDIDATO (AOS-521, ADR-041 §2.9).
//
// Duas funções PURAS, sem relógio, sem estado e sem leituras:
//
//   - [sinalDoDesfecho] diz, do que o nó selou no fim de um run, que SINAL esse run deixa — ou que
//     não deixa nenhum, e não entra na janela;
//   - [avaliarDisjuntor] diz, da SEQUÊNCIA das observações de um registo de entrada pela ordem em
//     que estão no livro, se a regra disparou, por que regra, e com que janela e contadores.
//
// A REGRA (decisão D5 do dono): dispara com [disjuntorFalhasParaAbrir] falhas nas últimas
// [disjuntorJanela] UNIDADES do candidato, ou à primeira observação de um turno com o modelo servido
// diferente do esperado. Disparada, fica disparada: o estado é o do momento do disparo, e as
// observações seguintes não o mudam.
//
// A UNIDADE DA JANELA é o nó do plano, e não a tentativa: uma nova tentativa (AOS-502, AOS-510) do
// mesmo nó SUBSTITUI o desfecho da anterior e passa para o fim da janela. Uma tentativa falhada que
// a seguinte recupera deixa de contar — mas conta enquanto a seguinte não fechar, e se entretanto a
// regra disparou, disparou. Num run sem plano, a unidade é o run.

import (
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// Os parâmetros da regra (D5).
const (
	// disjuntorFalhasParaAbrir é quantas falhas na janela disparam a regra.
	disjuntorFalhasParaAbrir = 3
	// disjuntorJanela é quantas unidades do candidato a janela tem.
	disjuntorJanela = 20
)

// Os sinais de um run do candidato. Vocabulário FECHADO: é o que fica no livro.
const (
	// sinalCumprido — o run concluiu sem veredicto negativo. Entra na janela e não é falha.
	sinalCumprido = "cumprido"
	// sinalNaoCumprido (F1) — o veredicto de conclusão do kernel é negativo, por uma razão que não
	// é a resposta vazia.
	sinalNaoCumprido = "nao_cumprido"
	// sinalRespostaVazia (F2) — o veredicto do kernel é negativo com a razão `empty_output`.
	sinalRespostaVazia = "resposta_vazia"
	// sinalRespostaRejeitada (F3) — o run falhou porque o gateway recusou a resposta do provider.
	sinalRespostaRejeitada = "resposta_rejeitada"
	// sinalErroDoProvider (F4) — o run falhou porque o provider respondeu com um status que não é
	// 200 nem 429.
	sinalErroDoProvider = "erro_do_provider"
	// sinalModeloDiferente (F5) — a governação da rota viu o modelo servido diferente do esperado.
	sinalModeloDiferente = "modelo_servido_diferente"
)

// sinaisDeFalha devolve os cinco sinais de falha, pela ordem F1..F5.
func sinaisDeFalha() []string {
	return []string{sinalNaoCumprido, sinalRespostaVazia, sinalRespostaRejeitada, sinalErroDoProvider, sinalModeloDiferente}
}

// sinalDeRunConhecido diz se o sinal é um dos seis que um desfecho de run pode ter.
func sinalDeRunConhecido(s string) bool {
	if s == sinalCumprido {
		return true
	}
	for _, f := range sinaisDeFalha() {
		if s == f {
			return true
		}
	}
	return false
}

// As regras que disparam. Vocabulário FECHADO.
const (
	// regraFalhasNaJanela — [disjuntorFalhasParaAbrir] falhas nas últimas [disjuntorJanela] unidades.
	regraFalhasNaJanela = "falhas_na_janela"
	// regraModeloDiferente — um turno com o modelo servido diferente do esperado.
	regraModeloDiferente = "modelo_servido_diferente"
)

// sinalDoDesfecho diz que sinal o fim de um run deixa na janela. `conta` falso ⇒ o run NÃO entra na
// janela — nem como falha, nem como run bom (um run que não conta não dilui as falhas dos outros).
//
// `selado` é o estado em que o log durável ficou; `res`, `runErr` e `panicked` são o que a
// hospedagem tem na mão quando sela — o veredicto é o MESMO valor que vai na transição terminal.
//
// O QUE CONTA:
//
//   - veredicto do kernel negativo ⇒ F2 se a razão é `empty_output`, F1 por qualquer outra razão —
//     com o nó a impor o veredicto (o run fechou `failed`) ou só a observá-lo (fechou `complete`):
//     o veredicto é o mesmo, e é ele o sinal;
//   - sem veredicto negativo e selado `complete` ⇒ cumprido;
//   - o run falhou com o erro de uma resposta recusada pelo gateway ⇒ F3;
//   - falhou com um status do provider que não é 200 nem 429 ⇒ F4;
//   - falhou porque a governação da rota, em imposição, viu o modelo servido DIFERENTE ⇒ F5.
//
// O QUE NÃO CONTA, e porquê:
//
//   - o limite de ritmo (429): é da conta, não do modelo (D5);
//   - o run recusado pela fixação do modelo: não deu um turno;
//   - o pânico, o cancelamento, o tecto de turnos e o orçamento esgotado: não dizem nada do modelo
//     que o nó saiba sem ler texto;
//   - um erro de transporte SEM resposta (ligação recusada ou cortada, tempo esgotado antes de
//     qualquer status): o nó não sabe se foi o modelo ou o caminho até ao proxy, que o titular
//     também usa. O proxy que esgota o tempo do provider responde com um status — e esse conta;
//   - a rota `nao_reportado` em imposição: o proxy deixou de declarar, o que não prova outro modelo
//     (é o `alerta-rota.sh` que o avisa);
//   - um desfecho que NÃO ficou selado, ou um run que parou sem acabar (suspenso, pausado).
func sinalDoDesfecho(selado state.State, res agentruntime.Result, runErr error, panicked bool) (sinal string, conta bool) {
	if panicked || !desfechoDuravelRegistado(selado) {
		return "", false
	}
	if runErr == nil {
		if v := res.Verdict; v != nil && !v.Fulfilled {
			if v.Reason == agentruntime.OutcomeEmptyOutput {
				return sinalRespostaVazia, true
			}
			return sinalNaoCumprido, true
		}
		if selado == state.Complete {
			return sinalCumprido, true
		}
		return "", false
	}
	switch modelgateway.ClassifyTurnFailure(runErr) {
	case modelgateway.TurnFailureResponseRejected:
		return sinalRespostaRejeitada, true
	case modelgateway.TurnFailureProviderStatus:
		return sinalErroDoProvider, true
	case modelgateway.TurnFailureRouteDifferent:
		return sinalModeloDiferente, true
	}
	return "", false
}

// observacaoDoDisjuntor é uma observação do livro sobre o candidato de um registo de entrada.
type observacaoDoDisjuntor struct {
	// turno diz que a observação é a de um TURNO com o modelo servido diferente (F5), e não o
	// desfecho de um run.
	turno bool
	// run é o id do run; unidade, a unidade da janela a que o desfecho pertence; sinal, o sinal do
	// desfecho. Numa observação de turno só `run` conta.
	run, unidade, sinal string
}

// itemDaJanela é uma unidade da janela, como fica selada: o run cujo desfecho vale para a unidade, e
// o sinal. Só identificadores.
type itemDaJanela struct {
	Run    string `json:"run_id"`
	Signal string `json:"signal"`
}

// estadoDaRegra é o que [avaliarDisjuntor] devolve.
type estadoDaRegra struct {
	// disparou diz se a regra disparou em algum ponto da sequência; regra diz qual.
	disparou bool
	regra    string
	// contadores são as observações por sinal de falha, e janela as últimas unidades — os dois NO
	// MOMENTO DO DISPARO, ou no fim da sequência se a regra não disparou.
	contadores map[string]int
	janela     []itemDaJanela
}

// falhasNaJanela conta as unidades da janela cujo sinal é uma falha.
func (e estadoDaRegra) falhasNaJanela() int {
	n := 0
	for _, it := range e.janela {
		if it.Signal != sinalCumprido {
			n++
		}
	}
	return n
}

// avaliarDisjuntor aplica a regra à sequência das observações de UM registo de entrada, pela ordem
// do livro. É uma função pura: a mesma sequência dá sempre o mesmo estado.
func avaliarDisjuntor(obs []observacaoDoDisjuntor) estadoDaRegra {
	st := estadoDaRegra{contadores: map[string]int{}}
	type unidade struct {
		id   string
		item itemDaJanela
	}
	var janela []unidade
	fotografar := func() {
		st.janela = make([]itemDaJanela, 0, len(janela))
		for _, u := range janela {
			st.janela = append(st.janela, u.item)
		}
	}
	for _, o := range obs {
		if o.turno {
			// F5 DISPARA SOZINHO, ao primeiro turno, com a janela como estiver — vazia que seja.
			st.contadores[sinalModeloDiferente]++
			st.disparou, st.regra = true, regraModeloDiferente
			fotografar()
			return st
		}
		if !sinalDeRunConhecido(o.sinal) || o.unidade == "" {
			continue
		}
		if o.sinal != sinalCumprido {
			st.contadores[o.sinal]++
		}
		// A tentativa mais recente de uma unidade SUBSTITUI a anterior, e vai para o fim.
		for i := range janela {
			if janela[i].id == o.unidade {
				janela = append(janela[:i], janela[i+1:]...)
				break
			}
		}
		janela = append(janela, unidade{id: o.unidade, item: itemDaJanela{Run: o.run, Signal: o.sinal}})
		if len(janela) > disjuntorJanela {
			janela = janela[len(janela)-disjuntorJanela:]
		}
		falhas := 0
		for _, u := range janela {
			if u.item.Signal != sinalCumprido {
				falhas++
			}
		}
		if falhas >= disjuntorFalhasParaAbrir {
			st.disparou, st.regra = true, regraFalhasNaJanela
			fotografar()
			return st
		}
	}
	fotografar()
	return st
}
