// AOS-408 — A CERIMÓNIA DE DECISÃO DE UM PLANO PENDENTE.
//
// O `serve --goal` deixa um plano de risco PENDENTE e sai; a decisão chega por aqui, noutro
// processo e noutro momento. O `decide` é **só** o acto de governação: verifica quem decide, que
// decidiu sobre QUE organigrama, e apensa o facto. NÃO materializa nem despacha — isso continua a
// ser do `serve`, que passa a exigir a decisão no log antes de admitir nós.
//
// Separar as duas coisas não é arrumação: é o que impede que a superfície que autoriza seja também
// a que executa. Um operador com a chave de aprovação assina; quem materializa é o processo que
// detém a posse do run e lê o log.
//
// A disciplina de credencial é a MESMA do nó `aos` (ADR-016 §1: quem verifica não assina):
//   - a assinatura é ed25519 e é produzida FORA deste processo (o `aos-issuer plan-approve-sign`);
//   - a chave pública do aprovador é PINADA por ficheiro, com autoridade por classe
//     (`approve:danger`), no mesmo formato que o nó usa em AOS_APPROVERS_FILE;
//   - a assinatura está AMARRADA ao plano concreto — o `request_id` é `plan:<plan_id>:<plan_hash>`,
//     pelo que uma aprovação de um organigrama nunca aprova outro;
//   - o nonce é consumido com CAS no Event Store (uso-único durável, sobrevive a restart);
//   - o documento reapresentado é confrontado por HASH com o `plan.validated` selado;
//   - o prazo é imposto no momento da decisão, sem depender de varredor nenhum.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/control-plane/governance/hitl"
	planapproval "github.com/aos-ref/control-plane/governance/plan-approval"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// ttlPendentePorOmissao é quanto tempo um plano fica à espera de decisão. Um dia: uma cerimónia
// humana atravessa fusos e fins-de-tarde, e um prazo curto transformaria o gate numa corrida. É
// ajustável por `--ttl` — e um prazo expirado RECUSA, nunca aprova por decurso.
const ttlPendentePorOmissao = 24 * time.Hour

// validarPrazo aceita um `--ttl` que ENCURTE o prazo da política e recusa um que o alargue.
//
// O prazo é política do sistema, não um argumento de quem decide: com um `--ttl` livre, quem tem
// interesse em aprovar um plano velho passava um prazo enorme e ressuscitava-o (e alargava a janela
// de frescura da assinatura). Encurtar só pode recusar a decisão de quem o encurta — e, como a
// expiração não escreve nada, não afecta mais ninguém.
func validarPrazo(ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("--ttl tem de ser positivo (veio %s): um prazo nao-positivo desligaria a expiracao do pendente", ttl)
	}
	if ttl > ttlPendentePorOmissao {
		return fmt.Errorf("--ttl %s alarga o prazo da politica (%s): o prazo so pode ser encurtado por quem decide, nunca alargado", ttl, ttlPendentePorOmissao)
	}
	return nil
}

// nivelDaCerimoniaDeDecisao é o nível de autonomia com que o gate corre NA CERIMÓNIA — L1, que
// confirma qualquer classe. Deliberadamente distinto do nível do `serve`: ali a auto-aprovação de
// planos sem risco é o que evita atrito inútil; aqui saltaria o canal, e o canal é a única peça que
// verifica a assinatura.
const nivelDaCerimoniaDeDecisao = autonomy.L1

// desvioMaximoDoFuturo é a tolerância de relógio para uma decisão datada à frente. Uma decisão do
// futuro não é uma decisão tomada.
const desvioMaximoDoFuturo = 2 * time.Minute

// exigirFrescura recusa uma decisão fora da janela do pendente: mais velha do que o prazo, ou
// datada para o futuro além da tolerância de relógio.
func exigirFrescura(emitida, agora time.Time, ttl time.Duration) error {
	if emitida.After(agora.Add(desvioMaximoDoFuturo)) {
		return fmt.Errorf("%w: a decisao esta datada de %s, no futuro", errDecisaoRecusada, emitida.Format(time.RFC3339))
	}
	if ttl > 0 && agora.Sub(emitida) > ttl {
		return fmt.Errorf("%w: a decisao foi tomada em %s, fora da janela de %s", errDecisaoRecusada, emitida.Format(time.RFC3339), ttl)
	}
	return nil
}

// aprovacaoFicheiro é a face JSON de uma [hitl.SignedApproval] — o artefacto que o humano produz
// fora deste processo. `nonce` e `signature` em hex; `issued_at` em RFC3339.
type aprovacaoFicheiro struct {
	RequestID string `json:"request_id"`
	Approver  string `json:"approver"`
	Approved  bool   `json:"approved"`
	Nonce     string `json:"nonce"`
	IssuedAt  string `json:"issued_at"` // RFC3339Nano: ver lerAprovacaoAssinada
	Signature string `json:"signature"`
}

// aprovadoresFicheiro é o registo de aprovadores PINADOS. Formato deliberadamente IDÊNTICO ao
// AOS_APPROVERS_FILE do nó `aos`: um operador não deve ter de aprender dois formatos para a mesma
// coisa, e uma divergência de formato acabaria por ser uma divergência de política.
type aprovadoresFicheiro struct {
	Approvers []struct {
		Principal string   `json:"principal"`
		Pubkey    string   `json:"pubkey"`
		Authority []string `json:"authority"`
	} `json:"approvers"`
}

// cmdDecide corre a cerimónia de decisão sobre um plano pendente.
func cmdDecide(args []string) error {
	fs := flag.NewFlagSet("decide", flag.ExitOnError)
	var sub substrato
	sub.registarFlags(fs)
	runID := fs.String("run", "", "run_id do plano a decidir")
	planDoc := fs.String("plan-doc", "", "ficheiro JSON do PlanDocument que ficou pendente (o log só tem o hash)")
	snapshot := fs.String("snapshot", "", "ficheiro JSON do snapshot PINADO — é dele que sai o risco resolvido do cartão")
	decisao := fs.String("decision", "", "approve | reject")
	aprovacao := fs.String("approval", "", "ficheiro JSON da decisão ASSINADA (aos-issuer plan-approve-sign)")
	aprovadores := fs.String("approvers", "", "ficheiro JSON dos aprovadores PINADOS (default: $AOS_APPROVERS_FILE)")
	ttl := fs.Duration("ttl", ttlPendentePorOmissao, "prazo do pendente, contado do plan.validated (tem de ser POSITIVO)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Um prazo não-positivo DESLIGAVA a expiração, e quem o passava era quem decide: o prazo
	// deixava de ser política do sistema para ser um argumento de quem tem interesse em que ele
	// não se aplique. Recusa-se em vez de tratar como «sem prazo».
	if err := validarPrazo(*ttl); err != nil {
		return err
	}
	if *runID == "" || *planDoc == "" || *snapshot == "" || *decisao == "" || *aprovacao == "" {
		return errors.New("decide exige --run, --plan-doc, --snapshot, --decision e --approval")
	}
	if *decisao != "approve" && *decisao != "reject" {
		return fmt.Errorf("--decision tem de ser approve ou reject, veio %q", *decisao)
	}
	caminhoAprovadores := *aprovadores
	if caminhoAprovadores == "" {
		caminhoAprovadores = os.Getenv("AOS_APPROVERS_FILE")
	}
	if caminhoAprovadores == "" {
		return errors.New("decide exige --approvers (ou AOS_APPROVERS_FILE): sem chaves PINADAS não há aprovador autenticável, e aceitar uma assinatura sem chave conhecida seria aceitar qualquer assinatura")
	}
	// O plano DERIVA do run e não é um argumento. O lease é tomado sobre o RUN e a escrita vai
	// para o stream do PLANO: com os dois independentes, um `--plan` de outro run era escrito sob
	// a posse deste, e dois `decide` com runs diferentes e o mesmo plano não eram arbitrados por
	// lease nenhum — a precedência terminal passava a depender da ordem de chegada ao disco. A
	// ligação run→plano não é um facto do log (os eventos do plano levam o plan_id como RunID),
	// pelo que a única amarra honesta é a convenção, imposta aqui.
	planoID := *runID + "-plan"

	ctx := context.Background()
	store, fechar, err := sub.abrirParaEscrita()
	if err != nil {
		return err
	}
	defer func() { _ = fechar() }()
	fmt.Println(sub.descrever())

	// POSSE: a decisão é uma ESCRITA no stream do plano, logo passa pelo MESMO lease e pelo MESMO
	// appender fenced que tudo o resto. Sem isto, dois processos podiam decidir em paralelo, e a
	// precedência terminal («a primeira decisão vence») dependeria de quem chegasse primeiro ao
	// disco em vez de quem detém o run.
	leases, err := durable.NewLeaseManager(store, leaseTTL, durable.WithWorkerID("decide"))
	if err != nil {
		return err
	}
	ten, err := runlifecycle.Claim(ctx, store, leases, *runID)
	if err != nil {
		return fmt.Errorf("posse do run %q: %w", *runID, err)
	}
	rec, err := runlifecycle.NewPlanRecorder(ten, planoID, eventstore.Producer{NHIID: "nhi:decide"})
	if err != nil {
		return err
	}

	err = decidirPlano(ctx, pedidoDeDecisao{
		store:              store,
		rec:                rec,
		runID:              *runID,
		planoID:            planoID,
		caminhoDoDoc:       *planDoc,
		caminhoDoSnapshot:  *snapshot,
		caminhoAprovacao:   *aprovacao,
		caminhoAprovadores: caminhoAprovadores,
		aprovar:            *decisao == "approve",
		ttl:                *ttl,
	})

	// HANDOFF POR ANÚNCIO, decidido ou recusado. A cerimónia acabou: quem materializa é outro
	// processo, e ele precisa da MESMA posse. Manter o lease até expirar faria a aprovação
	// bloquear a execução que ela própria autoriza — o gate a travar-se a si mesmo.
	if rerr := ten.Release(ctx); rerr != nil && err == nil {
		return fmt.Errorf("anúncio de largar a posse: %w", rerr)
	}
	return err
}

type pedidoDeDecisao struct {
	store              runlifecycle.EventStore
	rec                *runlifecycle.PlanRecorder
	runID              string
	planoID            string
	caminhoDoDoc       string
	caminhoDoSnapshot  string
	caminhoAprovacao   string
	caminhoAprovadores string
	aprovar            bool
	ttl                time.Duration
}

// decidirPlano é a cerimónia, na ordem em que cada recusa custa menos e prova mais.
func decidirPlano(ctx context.Context, p pedidoDeDecisao) error {
	leitor, err := runlifecycle.NewPlanDecisionReader(p.store, p.planoID)
	if err != nil {
		return err
	}
	estado, err := leitor.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !estado.Validated() {
		return fmt.Errorf("plano %q nao tem `plan.validated` no log: nao ha nada pendente para decidir", p.planoID)
	}
	if d := estado.Decision(); d != "" {
		// PRECEDÊNCIA TERMINAL: a primeira decisão venceu. Uma segunda não se sobrepõe — nem
		// para aprovar o que foi recusado, nem o contrário.
		return fmt.Errorf("%w: o plano %q ja tem decisao terminal %q (%s)", errDecisaoRecusada, p.planoID, d, estado.DecidedAt().Format(time.RFC3339))
	}
	agora := time.Now().UTC()
	if estado.Expirado(agora, p.ttl) {
		// O prazo é imposto AQUI, no momento da decisão, sem varredor. E NÃO SE ESCREVE NADA: a
		// expiração é DERIVADA — do instante do `plan.validated` e do prazo da política —, tal como
		// o próprio pendente. Escrevê-la como `plan.rejected` era dar a quem invoca o comando o
		// poder de FECHAR qualquer plano pendente (a precedência terminal impede uma aprovação
		// posterior) sem apresentar decisão nenhuma: bastava `--ttl 1ns` e ficheiros que nem
		// existiam. Um plano fora do prazo continua fora do prazo para toda a gente, sem facto.
		fmt.Printf("decisao RECUSADA: prazo do pendente expirou (validado %s, prazo %s)\n", estado.ValidatedAt().Format(time.RFC3339), p.ttl)
		return fmt.Errorf("%w: prazo do pendente expirou", errDecisaoRecusada)
	}

	// O DOCUMENTO: vem por ficheiro (o log só tem o hash — ADR-005) e é confrontado com o hash
	// selado. Sem esta comparação, aprovar-se-ia um organigrama e materializar-se-ia outro.
	doc, hash, err := lerDocumentoPorHash(p.caminhoDoDoc, estado.PlanHash())
	if err != nil {
		return err
	}
	snap, err := carregarSnapshot(p.caminhoDoSnapshot)
	if err != nil {
		return err
	}
	// O SNAPSHOT tem de ser o que o plano declara. Sem isto, o risco de cada nó era recalculado
	// sobre um ficheiro escolhido por quem decide: um snapshot benigno fazia o plano perigoso
	// resolver-se como `safe`, a auto-aprovação por nível dispensava o canal, e a assinatura nunca
	// era verificada.
	if err := exigirSnapshotDoPlano(doc, snap); err != nil {
		return err
	}
	// E o CONTEÚDO tem de ser o selado quando o plano ficou pendente: o rótulo `hash` copia-se, o
	// digest dos eixos não. Sem isto, um aprovador só com `approve:gray` aprovava um plano `danger`
	// apresentando um catálogo onde a tool perigosa era `gray` — e a decisão passava depois a valer
	// sob o catálogo real.
	if err := exigirSnapshotSelado(estado, snap); err != nil {
		return err
	}
	riscos := planvalidate.ResolveRisks(doc, snap, nil)
	pl := planoParaGate(doc, riscos, p.runID, agenteDoRun(p.runID), dominioDeAutonomia)
	forcados := nosQueExigemHumano(pl)

	// A AMARRA: o id do pedido é o plano e o seu hash. É sobre estes bytes que a assinatura do
	// humano vale — e é por isso que ela não pode ser reapresentada noutro plano.
	requestID := "plan:" + p.planoID + ":" + hash

	aprovacao, err := lerAprovacaoAssinada(p.caminhoAprovacao)
	if err != nil {
		return err
	}
	if aprovacao.RequestID != requestID {
		return fmt.Errorf("%w: a aprovacao esta assinada para %q e este pedido e %q — uma decisao sobre outro organigrama nao vale aqui",
			errDecisaoRecusada, aprovacao.RequestID, requestID)
	}
	if aprovacao.Approved != p.aprovar {
		return fmt.Errorf("%w: --decision diz %q mas a aprovacao ASSINADA diz o contrario; vale o que esta assinado",
			errDecisaoRecusada, veredictoTexto(p.aprovar))
	}
	// FRESCURA: a decisão tem de ter sido tomada dentro do prazo do pendente e não no futuro. Sem
	// janela, uma decisão pré-assinada (ou datada para a frente) valia indefinidamente desde que o
	// nonce fosse fresco — a assinatura cobre o instante, mas ninguém o confrontava com nada.
	if err := exigirFrescura(aprovacao.IssuedAt, agora, p.ttl); err != nil {
		return err
	}

	registo, err := carregarAprovadores(p.caminhoAprovadores)
	if err != nil {
		return err
	}

	canal, err := hitl.NewChannel(registo, fonteDeDecisaoSubmetida{aprovacao: aprovacao}, audit.NewMemStore(),
		hitl.WithIDSource(func() string { return requestID }))
	if err != nil {
		return fmt.Errorf("canal de decisao humana: %w", err)
	}
	// NÍVEL DA CERIMÓNIA: L1 (confirmação por acção, qualquer classe). NÃO é o nível do `serve`.
	// Aqui há uma decisão humana submetida para verificar, e a auto-aprovação por nível de
	// autonomia SALTA o canal — que é a única peça que verifica a assinatura. Com L1 o canal é
	// sempre chamado, pelo que uma assinatura forjada é recusada mesmo que o risco resolvido do
	// plano dissesse `safe`. É defesa em profundidade sobre a amarra do snapshot, não substituição.
	gate, err := planapproval.NewPlanGate(
		autonomy.NewLevelRegistry(autonomy.WithDefaultLevel(nivelDaCerimoniaDeDecisao)),
		canal,
		planapproval.WithReviewer(revisorDaDecisaoAssinada{}),
		planapproval.WithForcedReview(),
	)
	if err != nil {
		return fmt.Errorf("gate de aprovacao de plano: %w", err)
	}

	dec, err := gate.Approve(ctx, pl)
	if err != nil {
		// NADA TERMINAL AQUI. Um erro do gate (plano inválido, revisão forçada em falta, canal
		// indisponível) não é uma decisão do humano — e escrever `plan.rejected` neste ramo dava a
		// quem não tem chave nenhuma o poder de FECHAR um plano pendente para sempre (a precedência
		// terminal impede uma aprovação posterior), com o log a atribuir a recusa a um aprovador
		// que nunca foi autenticado. O plano continua PENDENTE; o erro diz porquê.
		return fmt.Errorf("gate de aprovacao de plano: %w", err)
	}
	// A partir daqui o canal RESPONDEU, e o canal só responde depois de verificar a assinatura
	// contra a chave pinada e a autoridade para a classe. O aprovador que vai para o log é o que o
	// canal resolveu (`dec.Approver`), não o nome que o ficheiro clamava.
	// O APROVADOR que o canal resolveu. Só vem preenchido quando a decisão foi assinada e
	// VERIFICADA contra a chave pinada — e é essa a linha que separa uma decisão de uma tentativa.
	aprovadorVerificado := dec.Approver
	if aprovadorVerificado == "" {
		// NÃO HOUVE DECISÃO DE NINGUÉM: assinatura forjada, aprovador desconhecido, autoridade em
		// falta. O plano fica PENDENTE — fechá-lo aqui dava a quem não tem chave o poder de matar
		// qualquer plano (a precedência terminal impede uma aprovação posterior) e punha no log uma
		// recusa atribuída a um aprovador que nunca foi autenticado.
		return fmt.Errorf("%w: a decisao apresentada nao foi verificada (assinatura, aprovador ou autoridade) — o plano CONTINUA pendente", errDecisaoRecusada)
	}
	// USO-ÚNICO DURÁVEL, DEPOIS da verificação. A ordem importa nas duas direcções: consumir o
	// nonce ANTES de verificar deixava uma assinatura FORJADA queimar o nonce de uma decisão
	// legítima (uma forma barata de negar serviço a quem tem a chave); consumi-lo DEPOIS de
	// registar o facto deixaria a mesma aprovação ser reapresentada. Aqui, entre as duas, o CAS no
	// Event Store é o árbitro: de duas cerimónias concorrentes com a mesma decisão, uma vence.
	nonces := hitl.NewEventStoreNonceStore(p.store)
	fresco, nerr := nonces.ConsumeNonce(ctx, requestID, aprovacao.Nonce)
	if nerr != nil {
		return fmt.Errorf("consumo do nonce da aprovacao: %w", nerr)
	}
	if !fresco {
		return fmt.Errorf("%w: o nonce desta aprovacao ja foi usado (replay)", errDecisaoRecusada)
	}

	if dec.Verdict != planapproval.VerdictApprove && aprovacao.Approved {
		// A decisão assinada diz SIM e o canal negou por outro motivo — dual-control (aprovador
		// igual ao solicitante), prazo do canal esgotado depois da verificação, selo que falhou. O
		// canal preenche o aprovador nesses casos porque a assinatura verificou, mas NÃO houve
		// recusa humana: gravar `plan.rejected` atribuiria a esse humano um «não» que ele nunca
		// disse, e fecharia o plano. Fica pendente.
		return fmt.Errorf("%w: a aprovacao de %s foi verificada mas o canal negou-a (%s) — nao e uma recusa humana; o plano CONTINUA pendente",
			errDecisaoRecusada, aprovadorVerificado, dec.Reason)
	}
	if dec.Verdict != planapproval.VerdictApprove {
		if _, rerr := p.rec.RecordDecision(ctx, plannerevents.DecisionPayload{
			PlanHash: hash, Decision: plannerevents.DecisionRejected,
			DecisionRef: runlifecycle.PrefixoDecisaoHumana + aprovadorVerificado,
		}); rerr != nil {
			return fmt.Errorf("%w e o facto da recusa também falhou: %v", errDecisaoRecusada, rerr)
		}
		fmt.Printf("decisao RECUSADA por %s: plano=%s plan_hash=%s\n", aprovadorVerificado, p.planoID, hash)
		return fmt.Errorf("%w: %s", errDecisaoRecusada, dec.Reason)
	}
	if _, err := p.rec.RecordDecision(ctx, plannerevents.DecisionPayload{
		PlanHash: hash, Decision: plannerevents.DecisionApproved,
		DecisionRef: runlifecycle.PrefixoDecisaoHumana + aprovadorVerificado,
	}); err != nil {
		return fmt.Errorf("facto da decisao do plano: %w", err)
	}
	fmt.Printf("decisao APROVADA por %s: plano=%s plan_hash=%s nos_de_risco=%d\n", aprovadorVerificado, p.planoID, hash, len(forcados))
	fmt.Printf("  materialize com: aos-orq serve --run %s --plan-doc %s --snapshot %s\n", p.runID, p.caminhoDoDoc, p.caminhoDoSnapshot)
	return nil
}

// veredictoTexto nomeia a decisão pedida, para a mensagem de divergência.
func veredictoTexto(aprovar bool) string {
	if aprovar {
		return "approve"
	}
	return "reject"
}

// lerDocumentoPorHash lê o documento reapresentado e EXIGE que o seu hash canónico seja o que o
// log selou em `plan.validated`. Um hash selado vazio (payload sem `plan_hash`) é recusa: não se
// aprova contra uma âncora que não se sabe ler.
func lerDocumentoPorHash(caminho, hashSelado string) (plan.PlanDocument, string, error) {
	if hashSelado == "" {
		return plan.PlanDocument{}, "", fmt.Errorf("%w: o `plan.validated` do log nao traz plan_hash — nao ha ancora para confrontar", errDecisaoRecusada)
	}
	raw, err := os.ReadFile(caminho)
	if err != nil {
		return plan.PlanDocument{}, "", fmt.Errorf("documento pendente %q: %w", caminho, err)
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		return plan.PlanDocument{}, "", fmt.Errorf("documento pendente %q: %w", caminho, err)
	}
	hash := hashDoPlano(doc)
	if hash != hashSelado {
		return plan.PlanDocument{}, "", fmt.Errorf("%w: o documento reapresentado tem hash %s e o plano validado tem %s — o humano decidiria sobre outro organigrama",
			errDecisaoRecusada, hash, hashSelado)
	}
	return doc, hash, nil
}

// lerAprovacaoAssinada lê e descodifica a decisão assinada. Não verifica a assinatura — isso é do
// [hitl.Channel], contra a chave pinada. Aqui só se recusa o que nem forma tem.
func lerAprovacaoAssinada(caminho string) (hitl.SignedApproval, error) {
	raw, err := os.ReadFile(caminho)
	if err != nil {
		return hitl.SignedApproval{}, fmt.Errorf("aprovacao assinada %q: %w", caminho, err)
	}
	var f aprovacaoFicheiro
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return hitl.SignedApproval{}, fmt.Errorf("aprovacao assinada %q: %w", caminho, err)
	}
	nonce, err := hex.DecodeString(f.Nonce)
	if err != nil {
		return hitl.SignedApproval{}, fmt.Errorf("aprovacao assinada %q: nonce nao e hex: %w", caminho, err)
	}
	sig, err := hex.DecodeString(f.Signature)
	if err != nil {
		return hitl.SignedApproval{}, fmt.Errorf("aprovacao assinada %q: assinatura nao e hex: %w", caminho, err)
	}
	// RFC3339Nano, e não RFC3339: a serialização canónica que a assinatura cobre usa o instante
	// em NANOSEGUNDOS (UnixNano). Um formato de wire com precisão de segundos truncaria o
	// instante e a assinatura deixaria de verificar — uma decisão legítima recusada por um
	// detalhe de formato. O parse em Nano aceita as duas precisões.
	emitida, err := time.Parse(time.RFC3339Nano, f.IssuedAt)
	if err != nil {
		return hitl.SignedApproval{}, fmt.Errorf("aprovacao assinada %q: issued_at nao e RFC3339: %w", caminho, err)
	}
	return hitl.SignedApproval{
		RequestID: f.RequestID,
		Approver:  f.Approver,
		Approved:  f.Approved,
		Nonce:     nonce,
		IssuedAt:  emitida,
		Signature: sig,
	}, nil
}

// carregarAprovadores lê as chaves PINADAS. Fail-closed em tudo: pubkey que não é hex de 32 bytes,
// principal repetido, autoridade fora do vocabulário `approve:<classe>` ou registo vazio.
func carregarAprovadores(caminho string) (hitl.ApproverRegistry, error) {
	raw, err := os.ReadFile(caminho)
	if err != nil {
		return nil, fmt.Errorf("aprovadores pinados %q: %w", caminho, err)
	}
	var f aprovadoresFicheiro
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("aprovadores pinados %q: %w", caminho, err)
	}
	if len(f.Approvers) == 0 {
		return nil, fmt.Errorf("aprovadores pinados %q: registo vazio — nenhum plano de risco poderia ser aprovado", caminho)
	}
	registo := hitl.NewMemApproverRegistry()
	vistos := make(map[string]struct{}, len(f.Approvers))
	porChave := make(map[string]string, len(f.Approvers))
	for _, a := range f.Approvers {
		if a.Principal == "" {
			return nil, fmt.Errorf("aprovadores pinados %q: entrada sem principal", caminho)
		}
		if _, dup := vistos[a.Principal]; dup {
			return nil, fmt.Errorf("aprovadores pinados %q: principal repetido %q", caminho, a.Principal)
		}
		vistos[a.Principal] = struct{}{}
		pub, err := hex.DecodeString(a.Pubkey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("aprovadores pinados %q: pubkey de %q nao e hex de %d bytes", caminho, a.Principal, ed25519.PublicKeySize)
		}
		// Duas identidades com a MESMA chave são uma identidade a fingir-se de duas: hoje sem
		// efeito (basta um aprovador), mas seria a forma óbvia de derrotar o dual-control quando
		// ele for ligado. O nó recusa-o no seu ficheiro de aprovadores; aqui também.
		if outro, dup := porChave[a.Pubkey]; dup {
			return nil, fmt.Errorf("aprovadores pinados %q: %q e %q partilham a mesma pubkey", caminho, outro, a.Principal)
		}
		porChave[a.Pubkey] = a.Principal
		for _, cap := range a.Authority {
			if !autoridadeDeAprovacaoValida(cap) {
				return nil, fmt.Errorf("aprovadores pinados %q: autoridade %q de %q fora do vocabulario approve:safe|gray|danger", caminho, cap, a.Principal)
			}
		}
		registo.Register(a.Principal, ed25519.PublicKey(pub), a.Authority...)
	}
	return registo, nil
}

// autoridadeDeAprovacaoValida fecha o vocabulário das capabilities de aprovação. Um literal
// inventado num ficheiro de config não pode virar autoridade por descuido de escrita.
func autoridadeDeAprovacaoValida(cap string) bool {
	switch cap {
	case hitl.RequiredAuthority(risk.ClassSafe),
		hitl.RequiredAuthority(risk.ClassGray),
		hitl.RequiredAuthority(risk.ClassDanger):
		return true
	default:
		return false
	}
}

// fonteDeDecisaoSubmetida é a [hitl.ApprovalSource] NÃO-BLOQUEANTE desta cerimónia: a decisão já
// foi submetida no comando, logo devolve-se de imediato.
//
// É esta peça que torna o gate assíncrono sem mentir. A porta do `hitl` chama-se `Await` e é
// bloqueante por assinatura — usá-la para ESPERAR por um humano prenderia o processo, e um `Await`
// que falhasse por timeout produziria uma RECUSA, fechando por engano um plano que ninguém decidiu.
// Aqui não se espera por nada: o pendente vive no log, e este processo só existe porque já há
// decisão para apresentar.
type fonteDeDecisaoSubmetida struct{ aprovacao hitl.SignedApproval }

func (f fonteDeDecisaoSubmetida) Await(ctx context.Context, _ hitl.Presentation) (hitl.SignedApproval, error) {
	if err := ctx.Err(); err != nil {
		return hitl.SignedApproval{}, err
	}
	return f.aprovacao, nil
}

// revisorDaDecisaoAssinada é a [planapproval.PlanReviewer] desta cerimónia: declara que os nós de
// risco foram revistos e devolve o veredicto que a assinatura carrega.
//
// A declaração de revisão é legítima porque a assinatura está amarrada ao HASH do plano: o humano
// assinou aquele organigrama e não outro, e o cartão apresenta todos os seus nós de risco. O que
// NÃO fica provado — e está declarado no ticket como resíduo — é a revisão nó-a-nó: o log guarda «o
// plano foi aprovado», não «este nó foi lido». Uma superfície de revisão item-a-item é UX, não
// governação, e entra noutro ticket.
type revisorDaDecisaoAssinada struct{}

func (r revisorDaDecisaoAssinada) Review(_ context.Context, card planapproval.PlanCard) (planapproval.PlanDecision, error) {
	// Os nós revistos são os que o CARTÃO marca como forçados — `Class >= gray` ou lacuna de
	// capacidade —, não o subconjunto mais estreito que decide se o plano precisa de humano
	// (`danger|gap`). A diferença tornava IMPOSSÍVEL aprovar um plano com um nó `gray` ao lado do
	// `danger`: a revisão forçada exigia o `gray` na lista, o reviewer não o punha, e o gate
	// recusava — e a primeira tentativa legítima FECHAVA o plano. E `gray` é o caso comum
	// (qualquer egress interno, qualquer dado sensível reversível).
	//
	// Declarar revistos todos os nós do cartão é legítimo porque a assinatura está amarrada ao
	// HASH do plano: o humano assinou ESTE organigrama, e o cartão apresenta todos estes nós. O que
	// NÃO fica provado é a leitura nó-a-nó — resíduo declarado no ticket.
	// O veredicto deste revisor é SEMPRE `approve` — e isso não é um bug: quem decide é o canal,
	// que verifica a assinatura contra a chave pinada. Devolver `reject` aqui fazia o gate retornar
	// ANTES do canal, e era assim que uma recusa com assinatura de lixo fechava um plano pendente
	// com o log a atribuir a recusa a um aprovador que nunca foi autenticado. O `approved:false`
	// que vai ASSINADO no ficheiro é o que produz a recusa — já verificada — do lado do canal.
	return planapproval.PlanDecision{Verdict: planapproval.VerdictApprove, ReviewedNodes: card.ForcedTaskIDs()}, nil
}

// cmdPlans lista os planos PENDENTES de decisão de um run — a superfície de leitura da cerimónia.
// Só lê (nunca pede posse), como o `inspect`.
func cmdPlans(args []string) error {
	fs := flag.NewFlagSet("plans", flag.ExitOnError)
	var sub substrato
	sub.registarFlags(fs)
	runID := fs.String("run", "", "run_id a inspeccionar")
	ttl := fs.Duration("ttl", ttlPendentePorOmissao, "prazo do pendente, contado do plan.validated (so pode encurtar o da politica)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validarPrazo(*ttl); err != nil {
		return err
	}
	if *runID == "" {
		return errors.New("--run é obrigatório")
	}
	planoID := *runID + "-plan" // a mesma convenção que o `decide` impõe
	ctx := context.Background()
	store, fechar, err := sub.abrirParaLeitura()
	if err != nil {
		return err
	}
	defer func() { _ = fechar() }()

	leitor, err := runlifecycle.NewPlanDecisionReader(store, planoID)
	if err != nil {
		return err
	}
	estado, err := leitor.Snapshot(ctx)
	if err != nil {
		return err
	}
	agora := time.Now().UTC()
	switch {
	case !estado.Validated():
		fmt.Printf("plano=%s estado=SEM-PROPOSTA-VALIDADA\n", planoID)
	case estado.Decision() != "":
		fmt.Printf("plano=%s estado=DECIDIDO decisao=%s em=%s plan_hash=%s\n",
			planoID, estado.Decision(), estado.DecidedAt().Format(time.RFC3339), estado.PlanHash())
	case estado.Expirado(agora, *ttl):
		fmt.Printf("plano=%s estado=PRAZO-EXPIRADO validado=%s prazo=%s plan_hash=%s\n",
			planoID, estado.ValidatedAt().Format(time.RFC3339), *ttl, estado.PlanHash())
	default:
		restante := *ttl - agora.Sub(estado.ValidatedAt())
		fmt.Printf("plano=%s estado=PENDENTE validado=%s restante=%s plan_hash=%s request_id=plan:%s:%s\n",
			planoID, estado.ValidatedAt().Format(time.RFC3339), restante.Truncate(time.Second), estado.PlanHash(), planoID, estado.PlanHash())
	}
	return nil
}
