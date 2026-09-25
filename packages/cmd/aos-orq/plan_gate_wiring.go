// AOS-408 — COSTURA DO GATE DE APROVAÇÃO DE PLANO no `aos-orq`.
//
// Este ficheiro é o mapeador que o DEF-274 declarava em falta: «o mapeamento `PlanDocument` →
// `planapproval.Plan` vive a jusante e NÃO existe em produção». O contrato do gate (AOS-236) e as
// extensões de ADR-022 (DEF-274) estavam entregues dos dois lados; o único consumidor era o
// `aos-demo`, que construía o `Plan` à mão.
//
// PORQUE VIVE NO COMPOSITION ROOT. O mapeamento cruza dois módulos que NÃO se devem conhecer: o
// `orchestrator/plan` (o documento, dados untrusted de um LLM) e a `governance/plan-approval` (o
// gate). Nenhum dos dois importa o outro, e é isso que se preserva — quem cruza é o binário, que é
// por desenho o único sítio onde as fronteiras se juntam (ADR-019; o `layer-lint` isenta `cmd/`
// como importador). O `plandispatch` NÃO podia hospedá-lo: o seu guard-test só admite
// `orchestrator/plan` e `orchestrator/plannerevents` nos imports de produção.
//
// A REGRA DE OURO do cartão é aqui que se cumpre: o `Preview` de cada nó é o EFEITO RESOLVIDO
// (tool@versão → recurso), nunca o `objective` — texto livre que o modelo escreveu. Um cartão que
// mostrasse prosa do LLM ao humano seria uma superfície de injecção com forma de governação.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	planapproval "github.com/aos-ref/control-plane/governance/plan-approval"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// nivelDeAutonomiaDoOrq é o nível que o gate consulta para decidir se um plano auto-aprova.
//
// L4 («autonomia por excepção») é o que produz EXACTAMENTE a decisão do dono: `danger` escala para
// confirmação humana, tudo o resto corre. Não é uma escolha estética — é a linha da tabela de
// [autonomy.Oversight] que corresponde à política pedida, e escrevê-la aqui (em vez de espalhar o
// predicado pelo wiring) mantém uma só autoridade sobre «isto precisa de humano?».
//
// LIMITE DECLARADO: no nó `aos` os níveis são DURÁVEIS e re-hidratados do WORM por par
// (agente, domínio); aqui é um default do processo, sem promoção nem demoção. É limitação de
// composição deste binário, não do mecanismo — e o banner declara-a.
const nivelDeAutonomiaDoOrq = autonomy.L4

// dominioDeAutonomia é o domínio do par (agente, domínio) da consulta de autonomia. O plano do
// `aos-orq` é decomposição/meta-orquestração, não um domínio de efeito (fs/http/mail).
const dominioDeAutonomia = "plan"

// errPlanoPendente — o plano exige decisão humana e NADA foi materializado nem despachado. Não é
// uma avaria: é o estado normal de um plano de risco num gate assíncrono, e por isso tem código de
// saída próprio (um operador, e um teste, têm de distinguir «à espera do humano» de «rebentou»).
var errPlanoPendente = errors.New("plano PENDENTE de decisao humana: nada foi materializado")

// errDecisaoRecusada — houve decisão e foi NÃO. Distinto do pendente: o caso está fechado.
var errDecisaoRecusada = errors.New("plano RECUSADO pelo gate de aprovacao")

// errDocumentoDoPlanoRecusado — o documento apresentado para retomar ou materializar um plano não
// é aceitável, e voltar a apresentá-lo dá sempre o mesmo resultado (AOS-442): não descodifica, não
// passa a validação estrutural, não é o organigrama que o `plan.validated` do run ancora, ou não se
// consegue ler. Tem código de saída próprio ([exitDocumentoRecusado]) e é TERMINAL: tratado como
// genérico, era transitório, e um pedido com um documento assim voltava à cabeça da fila para
// sempre.
var errDocumentoDoPlanoRecusado = errors.New("documento do plano RECUSADO")

// ErrSnapshotNaoCorresponde — o snapshot dado não é aquele contra o qual o plano foi validado.
//
// O plano DECLARA, no seu `planner_meta.capabilities_hash`, o snapshot sobre o qual foi construído;
// esse campo está dentro do documento e portanto coberto pelo `plan_hash`. Sem esta comparação, o
// risco de cada nó era recalculado a partir de um ficheiro escolhido por quem invoca o comando: um
// snapshot benigno fazia o plano perigoso resolver-se como `safe`, o gate auto-aprovava sem chamar
// o canal, e uma assinatura nunca era verificada. O validador (AOS-231) já impunha esta regra; os
// caminhos do gate é que não passavam por ele.
var ErrSnapshotNaoCorresponde = errors.New("aos-orq: o snapshot dado nao e o que o plano declara (planner_meta.capabilities_hash): o risco de cada no seria recalculado sobre entradas escolhidas por quem invoca o comando")

// exigirSnapshotDoPlano recusa um snapshot que não seja o declarado pelo documento.
func exigirSnapshotDoPlano(doc plan.PlanDocument, snap planvalidate.Snapshot) error {
	if doc.PlannerMeta.CapabilitiesHash == "" || snap.Hash == "" {
		return fmt.Errorf("%w: documento ou snapshot sem hash (fail-closed)", ErrSnapshotNaoCorresponde)
	}
	if doc.PlannerMeta.CapabilitiesHash != snap.Hash {
		return fmt.Errorf("%w: plano declara %q, snapshot e %q", ErrSnapshotNaoCorresponde, doc.PlannerMeta.CapabilitiesHash, snap.Hash)
	}
	return nil
}

// digestDoSnapshot é o digest do CONTEÚDO do snapshot: cada capability com o seu nome, versão,
// digest, admissibilidade e os TRÊS eixos de risco, por ordem canónica (nome, versão).
//
// Existe porque o `hash` do snapshot é um rótulo que o ficheiro declara sobre si mesmo — copiá-lo
// para um snapshot com eixos benignos passava qualquer comparação por rótulo, e com isso o risco
// de um plano passava a ser escolhido por quem invoca o comando. O digest do conteúdo não se copia:
// mudar um eixo muda o digest.
func digestDoSnapshot(snap planvalidate.Snapshot) string {
	tools := append([]planvalidate.Capability(nil), snap.Tools...)
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Name != tools[j].Name {
			return tools[i].Name < tools[j].Name
		}
		return tools[i].Version < tools[j].Version
	})
	h := sha256.New()
	for _, t := range tools {
		fmt.Fprintf(h, "%q|%q|%q|%t|%t|%d|%d|%d\n",
			t.Name, t.Version, t.Digest, t.Deprecated, t.Admissible,
			int(t.Sensitivity), int(t.Egress), int(t.Reversibility))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ErrSnapshotDiferenteDoSelado — o snapshot apresentado não tem o CONTEÚDO daquele sob o qual o
// plano foi validado. Uma decisão humana é tomada sobre um RISCO; decidir, ou materializar o que foi
// decidido, sob outro catálogo é decidir sobre outra coisa.
var ErrSnapshotDiferenteDoSelado = errors.New("aos-orq: o conteudo do snapshot nao e o selado no plan.validated")

// exigirSnapshotSelado recusa um snapshot cujo conteúdo não seja o selado no log. Um selo vazio
// (facto anterior ao AOS-408) também recusa: não há âncora para confrontar.
func exigirSnapshotSelado(estado *runlifecycle.PlanDecisionSnapshot, snap planvalidate.Snapshot) error {
	selado := estado.SnapshotDigest()
	if selado == "" {
		return fmt.Errorf("%w: o plan.validated nao traz snapshot_digest (facto sem ancora)", ErrSnapshotDiferenteDoSelado)
	}
	if actual := digestDoSnapshot(snap); actual != selado {
		return fmt.Errorf("%w: selado %s, apresentado %s", ErrSnapshotDiferenteDoSelado, selado, actual)
	}
	return nil
}

// exigirPlanoDoRun recusa decidir, ou aceitar uma decisão, num plano que não é o do run. A posse
// é do RUN e a decisão vive no stream do PLANO: sem esta amarra, a decisão humana tomada para um
// run era usada para materializar noutro — com o lease e o orçamento desse outro.
func exigirPlanoDoRun(runID, planID string) error {
	if planID != runID+"-plan" {
		return fmt.Errorf("%w: o plano %q nao e o do run %q (%s-plan) — uma decisao humana nao atravessa runs", errDecisaoRecusada, planID, runID, runID)
	}
	return nil
}

// bannerDoGateDePlano DECLARA a postura do gate no arranque (AOS-408), na mesma disciplina dos
// banners do nó: o que está composto, e o que NÃO está.
//
// As três limitações que aqui se dizem em voz alta seriam, caladas, três ilusões de governação:
//   - o `gap` entra no predicado mas NADA neste binário abre um gap hoje (nenhum emissor de
//     `plan.capability_gap_opened`), pelo que essa metade do âmbito é CONTRATO e não facto;
//   - o 4-eyes do canal exige aprovador ≠ solicitante, mas o solicitante aqui é a NHI do run (uma
//     máquina): qualquer humano o satisfaz. A garantia real é «um humano com autoridade pinada»,
//     não «dois humanos»;
//   - o nível de autonomia é um default do processo, não um nível durável por par (agente,
//     domínio) re-hidratado do WORM como no nó.
func bannerDoGateDePlano() string {
	return "gate de aprovacao de plano (AOS-408, AOS-236): COMPOSTO — nivel " + nivelDeAutonomiaDoOrq.String() +
		" (danger exige decisao humana; lacuna de capacidade tambem, mas NADA a abre neste binario hoje — contrato, nao facto). " +
		"A decisao vem por fora, assinada, com chave PINADA e autoridade por classe (`aos-orq decide`); o pendente e um FACTO no log. " +
		"4-eyes FRACO neste caminho: o solicitante e a NHI do run, logo qualquer humano o satisfaz — a garantia e `um humano com autoridade`, nao `dois humanos`. " +
		"O nivel e um default deste processo (no no `aos` os niveis sao duraveis por par agente/dominio). " +
		"FRONTEIRA: governa o PLANO e quem decide sem chave — NAO quem opera este CLI: o Event Store nao assina eventos, o snapshot e os aprovadores sao ficheiros do operador"
}

// pedidoDeGate são as entradas do gate na passagem da decomposição.
type pedidoDeGate struct {
	rec       *runlifecycle.PlanRecorder
	store     runlifecycle.EventStore
	runID     string
	doc       plan.PlanDocument
	snap      planvalidate.Snapshot
	tentativa int
	planOut   string
}

// gatearPlano interpõe o gate de aprovação entre a validação e a materialização (AOS-408) e
// devolve o hash do plano DECIDIDO.
//
// Ordem, e a razão dela: os factos `plan.proposed` e `plan.validated` são apensos ANTES de se
// decidir qualquer coisa. É isso que torna o pendente DERIVÁVEL do log — sem eles, «à espera do
// humano» seria a ausência de factos, indistinguível de «nunca foi proposto», e um restart perderia
// o caso. Só depois se pergunta ao gate.
//
// Um plano que exige humano devolve [errPlanoPendente] com o hash e o resumo dos nós de risco. O
// documento vai para `planOut` quando dado — pendente OU aprovado (AOS-442): o documento cru não vive
// no log (ADR-005), pelo que a passagem da decisão, e a retoma, têm de o receber por ficheiro e
// confrontá-lo com o hash selado.
func gatearPlano(ctx context.Context, p pedidoDeGate) (string, error) {
	hash := hashDoPlano(p.doc)
	if hash == "" {
		return "", errors.New("hash canonico do plano nao derivavel (fail-closed)")
	}
	if err := exigirSnapshotDoPlano(p.doc, p.snap); err != nil {
		return "", err
	}
	riscos := planvalidate.ResolveRisks(p.doc, p.snap, nil)
	pl := planoParaGate(p.doc, riscos, p.runID, agenteDoRun(p.runID), dominioDeAutonomia)

	// O ESTADO DA DECISÃO, lido ANTES de apensar factos (AOS-412). Um `plan_id` admite UMA
	// decisão terminal; se ela já existe e não é a aprovação DESTE organigrama, este plano não é
	// decidível neste run — e dizê-lo como «pendente» era mentir: o `decide` recusaria depois
	// («ja tem decisao terminal»), e o operador ficava num beco. É o caso normal com o modelo vivo:
	// aprovado o organigrama H1, repetir o `--goal` re-decompõe e produz H2.
	estado, err := lerDecisaoDoPlano(ctx, p.store, p.rec.PlanID())
	if err != nil {
		return "", err
	}
	if d := estado.Decision(); d != "" && !(estado.Approved() && estado.DecidedHash() == hash) {
		dica := "este plano ja foi decidido; um plano novo exige um run novo"
		if estado.Approved() {
			dica = "para executar o organigrama APROVADO use `aos-orq serve --plan-doc <documento aprovado>` — repetir o `--goal` re-decompoe e produz outro"
		}
		return "", fmt.Errorf("%w: o plano %s ja tem decisao terminal %q (%s) para o organigrama %s, e este e %s — %s",
			errDecisaoRecusada, p.rec.PlanID(), d, estado.DecisionRef(), estado.DecidedHash(), hash, dica)
	}

	// O DOCUMENTO GUARDA-SE ANTES DOS FACTOS (AOS-442), e só quando é ESTE o organigrama que o
	// `plan.validated` ancora (o primeiro, ou uma repetição do mesmo).
	//
	// Antes só se escrevia o pendente — e um plano AUTO-APROVADO não deixava documento nenhum. Uma
	// retoma depois de uma falha transitória não tinha por onde correr senão decompor de novo, e o
	// gate recusava o organigrama novo (saída 7): uma falha passageira tornava-se definitiva, e
	// pagava uma decomposição ao modelo. Com o documento guardado, a retoma corre por `--plan-doc`.
	//
	// ANTES dos factos, e não depois, porque a ordem inversa tem um buraco: com o `plan.validated`
	// no log e o documento por escrever, o plano fica ancorado a um hash cujo documento não existe
	// em lado nenhum — e nenhum `--goal` o volta a produzir. Nesta ordem, uma falha a escrever
	// aborta sem factos, e a tentativa seguinte decompõe como se nada fosse.
	//
	// Um organigrama que NÃO é o ancorado (o `--goal` repetido antes da decisão) não se escreve:
	// o ficheiro é o do plano decidível, e reescrevê-lo perdia-o (AOS-412).
	if !estado.Validated() || estado.PlanHash() == hash {
		if err := escreverDocumentoDoPlano(p.planOut, p.doc); err != nil {
			return "", err
		}
	}

	if _, err := p.rec.RecordProposed(ctx, plannerevents.ProposedPayload{
		PlanHash: hash,
		Meta: plannerevents.PlannerMeta{
			Model:            p.doc.PlannerMeta.Model,
			PromptVersion:    p.doc.PlannerMeta.PromptVersion,
			CapabilitiesHash: p.doc.PlannerMeta.CapabilitiesHash,
		},
		Attempt: p.tentativa,
	}); err != nil {
		return "", fmt.Errorf("facto da proposta do plano: %w", err)
	}
	if _, err := p.rec.RecordValidated(ctx, plannerevents.ValidatedPayload{
		PlanHash:       hash,
		NodeCount:      len(p.doc.Nodes),
		BudgetTotal:    clampU64ToInt64(p.doc.BudgetTotal.Tokens),
		MaxNodes:       planvalidate.DefaultMaxNodes,
		SnapshotDigest: digestDoSnapshot(p.snap),
	}); err != nil {
		return "", fmt.Errorf("facto da validação do plano: %w", err)
	}

	forcados := nosQueExigemHumano(pl)
	if len(forcados) > 0 {
		// A DECISÃO PODE JÁ EXISTIR: é este o caminho de retoma depois da cerimónia. Sem ele, um
		// plano aprovado ficava pendente para sempre — a decisão não levava a lado nenhum, e o
		// oráculo de cartão do despacho nunca era consultado (só se chega ao despacho por aqui).
		if estado.AprovadoPorHumano(hash) {
			// Decidido por humano para ESTE organigrama — mas sob que catálogo, e em que run?
			// Materializar sob outro catálogo é executar um risco que ninguém decidiu, e a decisão
			// de um run não atravessa para outro.
			if err := exigirSnapshotSelado(estado, p.snap); err != nil {
				return "", err
			}
			if err := exigirPlanoDoRun(p.runID, p.rec.PlanID()); err != nil {
				return "", err
			}
			fmt.Printf("gate de plano: APROVADO por humano (decisao no log) plan_hash=%s nos_de_risco=%d\n", hash, len(forcados))
			return hash, nil
		}
		if estado.Approved() {
			// Aprovado para este hash, mas pela MÁQUINA (auto-aprovação de quando o plano não
			// tinha nós de risco — por exemplo, sob outro catálogo). A auto-aprovação não autoriza
			// nós de risco, e o plano já não é decidível: recusa em vez de um pendente sem saída.
			return "", fmt.Errorf("%w: o plano %s foi aprovado pela maquina (%s) e agora tem nos de risco — uma auto-aprovacao nao os autoriza; um run novo",
				errDecisaoRecusada, p.rec.PlanID(), estado.DecisionRef())
		}
		if estado.Validated() && estado.PlanHash() != hash {
			// O plano JÁ está pendente, mas de OUTRO organigrama: o `plan.validated` é de
			// primeira-escrita (o passo é fixo), pelo que o `decide` ancora no primeiro hash e
			// este não seria decidível — outro «pendente» era outro beco, e reescrever o
			// `--plan-out` perdia o documento do plano que o É. Com o modelo vivo é o caso de
			// repetir o `--goal` antes da decisão.
			return "", fmt.Errorf("%w: o plano %s ja esta pendente para o organigrama %s, e este e %s — decida o documento pendente (`aos-orq decide --plan-doc <documento pendente>`) e execute-o com `aos-orq serve --plan-doc`; repetir o `--goal` re-decompoe e produz outro",
				errDecisaoRecusada, p.rec.PlanID(), estado.PlanHash(), hash)
		}
		if err := recusarPendenteExpirado(estado, time.Now().UTC()); err != nil {
			return "", err
		}
		if p.planOut != "" {
			fmt.Printf("plano pendente escrito: %s\n", p.planOut)
		}
		fmt.Printf("pendente de aprovacao humana: plano=%s plan_hash=%s %s\n", p.rec.PlanID(), hash, resumoDosForcados(pl, forcados))
		fmt.Printf("  decida com: aos-orq decide --run %s --plan-doc <doc.json> --decision approve|reject --approval <aprovacao.json>\n", p.runID)
		return "", fmt.Errorf("%w: plano=%s plan_hash=%s", errPlanoPendente, p.rec.PlanID(), hash)
	}

	// Sem nós de risco e JÁ aprovado para este organigrama (uma repetição do mesmo comando, ou o
	// `--plan-doc` depois do `--goal`): segue sem reescrever a decisão.
	// O catálogo tem de ser o SELADO: o risco, as capabilities do token e o cartão do despacho
	// derivam dele, e um snapshot com o mesmo rótulo e eixos benignos baixava-os todos.
	if estado.Approved() {
		if err := exigirSnapshotSelado(estado, p.snap); err != nil {
			return "", err
		}
		fmt.Printf("gate de plano: ja APROVADO (%s) plan_hash=%s\n", estado.DecisionRef(), hash)
		return hash, nil
	}

	// Sem nós de risco: o plano passa pelo GATE (não por um atalho) e auto-aprova pelo nível de
	// autonomia. O canal não é chamado neste caminho — e, se algum dia for, recusa: a decisão
	// humana desta composição vive no subcomando `decide`, com assinatura verificada.
	gate, err := planapproval.NewPlanGate(
		autonomy.NewLevelRegistry(autonomy.WithDefaultLevel(nivelDeAutonomiaDoOrq)),
		canalSemDecisaoSubmetida{},
		planapproval.WithForcedReview(),
	)
	if err != nil {
		return "", fmt.Errorf("gate de aprovação de plano: %w", err)
	}
	dec, err := gate.Approve(ctx, pl)
	if err != nil {
		return "", fmt.Errorf("gate de aprovação de plano: %w", err)
	}
	if dec.Verdict != planapproval.VerdictApprove {
		if _, rerr := p.rec.RecordDecision(ctx, plannerevents.DecisionPayload{
			PlanHash: hash, Decision: plannerevents.DecisionRejected, DecisionRef: "gate:" + dec.Reason,
		}); rerr != nil {
			return "", fmt.Errorf("%w (%s) e o facto da recusa também falhou: %v", errDecisaoRecusada, dec.Reason, rerr)
		}
		return "", fmt.Errorf("%w: %s", errDecisaoRecusada, dec.Reason)
	}
	if _, err := p.rec.RecordDecision(ctx, plannerevents.DecisionPayload{
		PlanHash: hash, Decision: plannerevents.DecisionApproved, DecisionRef: refDaAutoAprovacao(dec),
	}); err != nil {
		return "", fmt.Errorf("facto da decisão do plano: %w", err)
	}
	fmt.Printf("gate de plano: APROVADO sem humano (nivel %s, sem nos de risco) plan_hash=%s\n", nivelDeAutonomiaDoOrq.String(), hash)
	return hash, nil
}

// recusarPendenteExpirado recusa um plano cujo prazo de pendente já passou (AOS-442).
//
// O `decide` já o recusava; o `serve` dizia «pendente» para sempre. Com a fila a re-oferecer o
// pedido estacionado, isso era um laço sem fim — a cada re-oferta, outro «pendente» sobre um plano
// que ninguém pode decidir. NÃO se escreve facto, pela razão do `decide`: a expiração é derivada do
// instante do `plan.validated` e do prazo da política, igual para toda a gente.
func recusarPendenteExpirado(estado *runlifecycle.PlanDecisionSnapshot, agora time.Time) error {
	if !pendenteExpirado(estado, agora) {
		return nil
	}
	return fmt.Errorf("%w: o prazo do pendente do plano %s expirou (validado %s, prazo %s) — um plano novo exige um run novo",
		errDecisaoRecusada, estado.PlanID(), estado.ValidatedAt().Format(time.RFC3339), ttlPendentePorOmissao)
}

// pendenteExpirado diz se um plano VALIDADO já passou o prazo do pendente.
//
// UM CARIMBO ILEGÍVEL CONTA COMO EXPIRADO, e é o contrário do que o
// [runlifecycle.PlanDecisionSnapshot.Expirado] faz — de propósito. Lá, no momento de uma decisão
// humana, desligar o prazo evita recusar uma decisão legítima por causa de um carimbo. Aqui a
// pergunta é outra: se um pedido estacionado continua a ser re-oferecido. Sem prazo legível, a
// re-oferta nunca acabava; fechá-lo é o lado seguro (AOS-442).
func pendenteExpirado(estado *runlifecycle.PlanDecisionSnapshot, agora time.Time) bool {
	if !estado.Validated() {
		return false
	}
	if estado.ValidatedAt().IsZero() {
		return true
	}
	return estado.Expirado(agora, ttlPendentePorOmissao)
}

// exigeDecisaoHumana diz se o documento, sob o snapshot, tem nós que exigem decisão humana — pelas
// MESMAS funções que o gate usa ([planoParaGate], [nosQueExigemHumano]), para o `consume` poder
// saber, sem correr o `serve`, se um plano validado e sem decisão está à espera de um humano ou
// ficou a meio de uma auto-aprovação (AOS-442).
func exigeDecisaoHumana(doc plan.PlanDocument, snap planvalidate.Snapshot, runID string) bool {
	riscos := planvalidate.ResolveRisks(doc, snap, nil)
	return len(nosQueExigemHumano(planoParaGate(doc, riscos, runID, agenteDoRun(runID), dominioDeAutonomia))) > 0
}

// lerDecisaoDoPlano relê o stream do plano e devolve o retrato da decisão (pendente, aprovada,
// recusada; hash e referência da decisão; digest do snapshot selado).
func lerDecisaoDoPlano(ctx context.Context, store runlifecycle.EventStore, planID string) (*runlifecycle.PlanDecisionSnapshot, error) {
	leitor, err := runlifecycle.NewPlanDecisionReader(store, planID)
	if err != nil {
		return nil, err
	}
	return leitor.Snapshot(ctx)
}

// oraculoDeCartao é o [plandispatch.CardOracle] do despacho: um nó que exige cartão só é autorizado
// se a decisão do plano for APROVADA POR HUMANO e para ESTE hash — o MESMO predicado que o gate usa.
// Antes consultava `Approved()` sozinho, que aceitaria uma auto-aprovação da máquina ou a decisão de
// outro organigrama; o gate a montante protegia, mas duas peças com dois critérios divergem.
type oraculoDeCartao struct {
	estado *runlifecycle.PlanDecisionSnapshot
	hash   string
}

func (o oraculoDeCartao) Cleared(ctx context.Context, planID, nodeID string) (bool, error) {
	if _, err := o.estado.Cleared(ctx, planID, nodeID); err != nil {
		return false, err // plano alheio ⇒ erro, nunca false silencioso
	}
	return o.estado.AprovadoPorHumano(o.hash), nil
}

// refDaAutoAprovacao é a REFERÊNCIA da decisão que vai para o log: quem/porque decidiu, sem
// assinatura crua nem PII. Numa auto-aprovação o decisor é a política de autonomia, e dizê-lo é o
// que impede um auditor de confundir «ninguém se opôs» com «um humano aprovou».
func refDaAutoAprovacao(dec planapproval.PlanDecision) string {
	if dec.AutoApproved {
		return "auto:autonomy:" + nivelDeAutonomiaDoOrq.String()
	}
	return "gate:" + dec.Approver
}

// agenteDoRun é a NHI do run — o mesmo identificador que a cadeia de delegação usa.
func agenteDoRun(runID string) string { return "agt-" + runID }

// hashDoPlano é o hash canónico do documento: a MESMA derivação que o materializador usa
// (sha256 sobre a codificação canónica). Tem de ser a mesma, senão o facto da decisão e o da
// materialização falariam de organigramas diferentes com nomes iguais.
func hashDoPlano(doc plan.PlanDocument) string {
	raw, err := plan.Encode(doc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// escreverDocumentoDoPlano guarda o documento do plano validado: para o humano o rever e o `decide`
// o reapresentar, quando fica pendente; e para a retoma correr por `--plan-doc` em vez de decompor
// de novo, quando é aprovado (AOS-442). Sem `--plan-out` não escreve nada — e isso é admissível: o
// operador pode já ter o documento (fixture, ou o ficheiro que passou em `--plan-doc`).
//
// O ficheiro NÃO é autoridade: quem o lê de volta confronta-o por HASH com o `plan.validated`
// selado no log (o `serve --plan-doc`, o `consume` e o `decide`). Um ficheiro trocado dá outro hash,
// e é recusado.
//
// A ESCRITA É ATÓMICA (AOS-442): ficheiro temporário na mesma pasta, `fsync`, e `rename` por cima.
// Um processo que morresse a meio de um `WriteFile` deixava um documento TRUNCADO no lugar do bom —
// e a retoma seguinte lia-o. Assim, ou fica o documento anterior, ou o novo inteiro.
func escreverDocumentoDoPlano(caminho string, doc plan.PlanDocument) error {
	if caminho == "" {
		return nil
	}
	raw, err := plan.Encode(doc)
	if err != nil {
		return fmt.Errorf("codificação do documento do plano: %w", err)
	}
	pasta := filepath.Dir(caminho)
	tmp, err := os.CreateTemp(pasta, ".plano-*.tmp")
	if err != nil {
		return fmt.Errorf("escrita do documento do plano em %q: %w", caminho, err)
	}
	nomeTmp := tmp.Name()
	falhou := func(e error) error {
		_ = tmp.Close()
		_ = os.Remove(nomeTmp)
		return fmt.Errorf("escrita do documento do plano em %q: %w", caminho, e)
	}
	if _, err := tmp.Write(raw); err != nil {
		return falhou(err)
	}
	if err := tmp.Sync(); err != nil {
		return falhou(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(nomeTmp)
		return fmt.Errorf("escrita do documento do plano em %q: %w", caminho, err)
	}
	if err := os.Rename(nomeTmp, caminho); err != nil {
		_ = os.Remove(nomeTmp)
		return fmt.Errorf("escrita do documento do plano em %q: %w", caminho, err)
	}
	// O `rename` só é durável com a PASTA sincronizada. Melhor-esforço: nem todos os sistemas
	// deixam abrir uma pasta para `fsync` (o Windows não), e aí o `rename` já é o que há.
	if d, err := os.Open(pasta); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// canalSemDecisaoSubmetida é o [risk.ConfirmationChannel] da passagem da DECOMPOSIÇÃO: recusa
// sempre, porque nesta passagem não há decisão humana nenhuma para colher.
//
// Não é um stub de conveniência — é a forma de o desenho assíncrono ser fail-closed. Um canal que
// BLOQUEASSE à espera do humano prenderia o processo (o oposto do que foi decidido); um que
// devolvesse «aprovado» aprovaria sem ninguém. Recusar é o único comportamento honesto, e o
// caminho que chega aqui está antes protegido pelo desvio do pendente.
type canalSemDecisaoSubmetida struct{}

func (canalSemDecisaoSubmetida) Confirm(_ context.Context, _ risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	return risk.ConfirmationResponse{}, errors.New("sem decisao humana submetida nesta passagem: use `aos-orq decide`")
}

// planoParaGate projecta o documento decomposto no plano que o gate aprova (AOS-408).
//
// `riscos` é o risco RESOLVIDO por node_id ([planvalidate.ResolveRisks]) — o piso derivado das
// tools PINADAS, elevado pelo rótulo do LLM só quando este for superior. É essa a classe que vai
// no cartão, NUNCA o `risk_class` do documento: um plano que se declare `safe` sobre uma tool
// irreversível tem de chegar ao humano como `danger`. Um nó sem entrada em `riscos` fica
// `ClassDanger` (o valor-zero de [risk.Class] é fail-closed, e não o contrariamos).
//
// `agente` e `dominio` são o par (agente, domínio) da consulta de autonomia, herdado pelos nós.
func planoParaGate(doc plan.PlanDocument, riscos map[string]planvalidate.NodeRisk, runID, agente, dominio string) planapproval.Plan {
	nos := make([]planapproval.PlanNode, 0, len(doc.Nodes))
	arestas := make([][2]string, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		nr, temRisco := riscos[n.NodeID]
		no := planapproval.PlanNode{
			TaskID: n.NodeID,
			// Class é LIDA da classificação; o gate não reclassifica. Sem risco resolvido
			// fica o valor-zero de risk.Class, que é ClassDanger (fail-closed).
			Class:        classeDeRisco(nr, temRisco),
			Irreversible: temRisco && nr.Classification.Irreversible(),
			Preview:      previewDoNo(n),
			Capability:   capabilityDoNo(n),
			Resource:     recursoDoNo(n),
			Cost:         custoDoNo(n),
			Role:         n.Role,
			// CapabilityGap: no `aos-orq` nada abre um gap hoje (nenhum emissor de
			// `plan.capability_gap_opened`), pelo que declará-lo `true` seria inventar um
			// facto. Fica `false` e o banner diz que esta metade do âmbito é CONTRATO e
			// não facto — ver o ticket AOS-408.
			ConditionalOn: condicoesDoNo(n),
			Outputs:       saidasDoNo(n),
			Consumes:      consumosDoNo(n),
		}
		nos = append(nos, no)
		for _, dep := range n.DependsOn {
			arestas = append(arestas, [2]string{dep, n.NodeID})
		}
	}
	return planapproval.Plan{
		RunID:  runID,
		Agent:  agente,
		Domain: dominio,
		Nodes:  nos,
		Edges:  arestas,
	}
}

// classeDeRisco traduz o risco RESOLVIDO do validador para a classe do gate. A tradução é
// explícita (e não um cast) porque são dois vocabulários: o do documento (`safe`/`gray`/`danger`,
// strings JSON) e o do classificador ([risk.Class], cujo valor-zero é `danger`). Qualquer valor
// que não seja exactamente `safe` ou `gray` — incluindo vazio e um nó sem risco resolvido — conta
// como `danger`: é a direcção certa do erro.
func classeDeRisco(nr planvalidate.NodeRisk, temRisco bool) risk.Class {
	if !temRisco {
		return risk.ClassDanger
	}
	switch nr.Resolved {
	case plan.RiskSafe:
		return risk.ClassSafe
	case plan.RiskGray:
		return risk.ClassGray
	default:
		return risk.ClassDanger
	}
}

// previewDoNo constrói o efeito RESOLVIDO do nó para o cartão: as tools pinadas que ele usa, na
// forma `nome@versão`, mais o papel declarado quando existe. NUNCA o `objective`: esse é texto
// livre produzido pelo modelo e o cartão é a superfície que o humano lê para decidir.
func previewDoNo(n plan.Node) string {
	partes := make([]string, 0, len(n.Tools)+1)
	for _, t := range n.Tools {
		if t.Version == "" {
			partes = append(partes, t.Name)
			continue
		}
		partes = append(partes, t.Name+"@"+t.Version)
	}
	if len(partes) == 0 {
		partes = append(partes, "sem tools pinadas")
	}
	preview := n.NodeID + ": " + strings.Join(partes, ", ")
	if n.Role != "" {
		preview += " (papel " + n.Role + ")"
	}
	return preview
}

// capabilityDoNo devolve a capability coarse do nó pelo MESMO mapper canónico que a materialização
// usa ([planmaterialize.DefaultCapabilityMapper]) — não um literal paralelo, para a convenção não
// divergir em silêncio. Com várias tools, as capabilities saem ordenadas e juntas.
func capabilityDoNo(n plan.Node) string {
	if len(n.Tools) == 0 {
		return ""
	}
	caps := make([]string, 0, len(n.Tools))
	vistas := make(map[string]struct{}, len(n.Tools))
	for _, t := range n.Tools {
		c := planmaterialize.DefaultCapabilityMapper(plan.ToolRef{Name: t.Name})
		if c == "" {
			continue
		}
		if _, ok := vistas[c]; ok {
			continue
		}
		vistas[c] = struct{}{}
		caps = append(caps, c)
	}
	sort.Strings(caps)
	return strings.Join(caps, ",")
}

// recursoDoNo identifica o alvo do efeito por DIGEST das tools pinadas, não por texto do modelo. O
// digest é o que amarra a decisão do humano a uma tool concreta e verificável.
func recursoDoNo(n plan.Node) string {
	for _, t := range n.Tools {
		if t.Digest != "" {
			return t.Digest
		}
	}
	return n.NodeID
}

// custoDoNo projecta o custo ESTIMADO do nó (por-ramo) para o cartão. O documento declara-o em
// uint64 (untrusted); satura em MaxInt64 em vez de transbordar para negativo — um custo absurdo
// tem de aparecer absurdo, não pequeno.
func custoDoNo(n plan.Node) *planapproval.CostEstimate {
	if n.BudgetEstimate.Tokens == 0 && n.BudgetEstimate.CostMicroUSD == 0 {
		return nil
	}
	return &planapproval.CostEstimate{
		EstimatedTokens: clampU64ToInt64(n.BudgetEstimate.Tokens),
		MicroUSD:        clampU64ToInt64(n.BudgetEstimate.CostMicroUSD),
	}
}

// condicoesDoNo projecta as arestas CONDICIONAIS na forma canónica do cartão: símbolos do enum
// fechado e o operando como string canónica (símbolo, ou inteiro em decimal). É o invariante
// §2.4(5) do ADR-022 — o humano vê o organigrama COM as condições que o governam.
func condicoesDoNo(n plan.Node) []planapproval.PlanCondition {
	if len(n.ConditionalOn) == 0 {
		return nil
	}
	out := make([]planapproval.PlanCondition, 0, len(n.ConditionalOn))
	for _, ce := range n.ConditionalOn {
		quando := make([]planapproval.PlanPredicate, 0, len(ce.When))
		for _, p := range ce.When {
			quando = append(quando, planapproval.PlanPredicate{
				Subject: string(p.Subject),
				Metric:  p.Metric,
				Op:      string(p.Op),
				Operand: operandoCanonico(p),
			})
		}
		out = append(out, planapproval.PlanCondition{From: ce.From, When: quando})
	}
	return out
}

// operandoCanonico devolve o operando do predicado como string canónica: o símbolo do enum quando
// o observável é simbólico, ou o inteiro em decimal quando é métrico. Um predicado sem operando
// utilizável devolve vazio, e o gate recusa-o fail-closed ([planapproval] impõe a forma canónica)
// — melhor recusar o cartão do que apresentar uma condição que não se sabe ler.
func operandoCanonico(p plan.Predicate) string {
	if p.Enum != "" {
		return string(p.Enum)
	}
	if p.Number != nil {
		return strconv.FormatInt(*p.Number, 10)
	}
	return ""
}

// saidasDoNo projecta os contratos de saída com o taint EFECTIVO — [plan.Node.EffectiveOutputTaint],
// que é «forma fechada E produtor verificador», e não o rótulo advisory do documento. É a diferença
// entre mostrar ao humano o que VALE e mostrar o que o modelo DISSE.
func saidasDoNo(n plan.Node) []planapproval.PlanOutput {
	if len(n.Outputs) == 0 {
		return nil
	}
	out := make([]planapproval.PlanOutput, 0, len(n.Outputs))
	for _, o := range n.Outputs {
		out = append(out, planapproval.PlanOutput{
			Name:  o.Name,
			Type:  string(o.Type),
			Taint: string(n.EffectiveOutputTaint(o)),
		})
	}
	return out
}

// consumosDoNo projecta as arestas de DADOS declaradas no extremo consumidor. Nunca o payload.
func consumosDoNo(n plan.Node) []planapproval.PlanConsume {
	if len(n.Consumes) == 0 {
		return nil
	}
	out := make([]planapproval.PlanConsume, 0, len(n.Consumes))
	for _, c := range n.Consumes {
		out = append(out, planapproval.PlanConsume{From: c.From, Output: c.Output, Type: string(c.Type)})
	}
	return out
}

// nosQueExigemHumano devolve os node_id que, pelo risco RESOLVIDO, exigem decisão humana antes de
// materializar — a decisão do dono (2026-09-17): `danger` ou lacuna de capability (`gap`).
//
// NÃO se usa o [planapproval.PlanCard.ForcedTaskIDs], que inclui também `gray`: o âmbito decidido
// é mais estreito, e escrever o predicado à letra aqui é o que impede que um refactor futuro o
// alargue sem ninguém decidir. O `gap` é hoje sempre falso neste binário (nada o abre) — está no
// predicado porque é contrato, e o banner declara que não é facto.
func nosQueExigemHumano(pl planapproval.Plan) map[string]bool {
	forcados := make(map[string]bool, len(pl.Nodes))
	for _, n := range pl.Nodes {
		if n.Class == risk.ClassDanger || n.CapabilityGap {
			forcados[n.TaskID] = true
		}
	}
	return forcados
}

// resumoDosForcados descreve, sem PII e sem texto do modelo, porque é que o plano precisa de
// humano — o que o operador vê no stdout antes de ir assinar.
func resumoDosForcados(pl planapproval.Plan, forcados map[string]bool) string {
	if len(forcados) == 0 {
		return ""
	}
	ids := make([]string, 0, len(forcados))
	for id := range forcados {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return fmt.Sprintf("%d no(s) de risco: %s", len(ids), strings.Join(ids, ", "))
}
