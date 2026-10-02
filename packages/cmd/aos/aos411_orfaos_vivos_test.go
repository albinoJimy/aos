package main

// AOS-411 — A RE-VARREDURA DE ÓRFÃOS EXCLUI OS RUNS VIVOS ANTES DE OS RECONSTITUIR.
//
// O defeito que estes testes trancam foi OBSERVADO EM PRODUÇÃO (v0.1.22, 2026-09-18): um run que
// estava a correr, e que terminou bem, fez o nó escrever «capturas ILEGIVEIS — NAO retomado
// (fail-closed)» e «1 run(s) orfaos em `running`», num contentor que nunca tinha reiniciado. Quem
// escreveu foi a RE-VARREDURA PERIÓDICA, que se anunciou como «varredura de arranque».
//
// A causa não era a retoma — era a ORDEM. Tudo o que está em `running` era classificado como
// órfão e reconstituído (cursor, registo de retoma e CAPTURAS DECIFRADAS SOB A CHAVE DO TITULAR)
// ANTES de alguém perguntar se o run tinha dono. A pergunta existia, mas só no fim, dentro do
// `submit` — que continua lá, como defesa em profundidade.
//
// Os testes exercitam o varredor REAL ([NodeService.resumeInterruptedRuns]) sobre o nó REAL
// (obsPermitNodeWith → Bootstrap com PDP, catálogo assinado e registo de retoma), e leem o que o
// operador leria: o LOG do serviço. Sem doubles do varredor, como a invariante do EPIC-20 exige.
//
// DETERMINISMO: nenhum `time.Now()` decide nada. O relógio da autoridade de lease é o
// [relogioMovel] manual (o mesmo de AOS-283), a vivacidade de um run é sincronizada por CANAL (o
// modelo pára no 1.º turno e só continua quando o teste o solta), e os `time.After` que existem
// são tectos de segurança para o teste não pendurar — nunca condições de sucesso.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// ttl411 é o TTL de lease destes testes. Vale um minuto de relógio MANUAL: nada expira sozinho,
// e o que expira, expira porque o teste avançou o relógio.
const ttl411 = time.Minute

// O destino de log é o [registoSeguro] que já existe no pacote de teste (aos101) — seguro para
// leitura concorrente, que é o que o `-race` exige enquanto a goroutine de um run VIVO escreve.
// Só lhe faltavam dois gestos, acrescentados aqui em vez de duplicar o tipo.

// tem diz se o log acumulado contém `sub`.
func (r *registoSeguro) tem(sub string) bool { return strings.Contains(r.String(), sub) }

// limpar descarta o que foi escrito até aqui (banners de arranque do serviço), para que a
// asserção fale só da passagem sob teste.
func (r *registoSeguro) limpar() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b.Reset()
}

// modeloPreso pára no PRIMEIRO turno e só devolve quando o teste o solta. É o retrato fiel do run
// de produção no instante em que a re-varredura passou: durávelmente em `running`, hospedado por
// esta réplica e SEM nenhum `turn.recorded` capturado ainda — o caso em que a varredura antiga
// saía com «capturas ILEGIVEIS».
type modeloPreso struct {
	chegou  chan struct{}
	liberta chan struct{}
	umaVez  sync.Once
	solta   sync.Once
}

func novoModeloPreso() *modeloPreso {
	return &modeloPreso{chegou: make(chan struct{}), liberta: make(chan struct{})}
}

func (m *modeloPreso) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.umaVez.Do(func() { close(m.chegou) })
	select {
	case <-m.liberta:
	case <-ctx.Done():
		return agentruntime.ModelResponse{}, ctx.Err()
	}
	return agentruntime.ModelResponse{Text: "done", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func (m *modeloPreso) soltar() { m.solta.Do(func() { close(m.liberta) }) }

// fixtura411 levanta o substrato mínimo de que o varredor precisa (Event Store, máquina de
// estados durável e registo de retoma) e um serviço cujo relógio de LEASE é manual.
func fixtura411(t *testing.T, modelo agentruntime.ModelClient) (*eventstore.Store, *Node, *NodeService, *relogioMovel, *registoSeguro) {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	vault := audit.NewInMemoryKeyVault(nil)
	node, _ := obsPermitNodeWith(t, "", modelo, func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = vault
		cfg.DurableExecution = true
		cfg.Approvers = crashResumeApprovers(t) // sem four-eyes não há ResumeRecords composto
	})
	t.Cleanup(func() { _ = node.Close() })
	if node.ResumeRecords == nil || node.stateGates == nil {
		t.Fatal("precondicao: sem registo de retoma ou maquina de estados o varredor declara-se DESLIGADO e o teste nao mede nada")
	}

	rel := novoRelogioMovel()
	reg := &registoSeguro{}
	svc, err := NewNodeService(node,
		WithLeaseClock(durable.ClockFunc(rel.Now)), WithLeaseTTL(ttl411),
		WithDeadlineSweepInterval(0), WithServiceLog(reg))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.Shutdown(sc)
	})
	return store, node, svc, rel, reg
}

// marcarDuravelmenteRunning põe o stream do run em `running` sem o hospedar — o rasto que um
// crash deixa, e o MESMO gesto que o teste de A4 usa.
func marcarDuravelmenteRunning(t *testing.T, store *eventstore.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	m, err := state.NewMachine(store, runID)
	if err != nil {
		t.Fatalf("state.NewMachine(%s): %v", runID, err)
	}
	if _, err := m.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild(%s): %v", runID, err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim ready->running(%s): %v", runID, err)
	}
}

// TestAOS411_RunVivoNestaReplicaNaoEOrfao é a prova central (AC1) e o retrato do incidente: um run
// HOSPEDADO por esta réplica, parado no 1.º turno, é saltado sem se lhe ler cursor, registo de
// retoma nem capturas — e a passagem fica MUDA.
//
// FALHA-ANTES: sem a guarda, este mesmo run conta como 1 órfão, as suas capturas são procuradas
// (e decifradas quando existem), a passagem escreve «capturas ILEGIVEIS — NAO retomado
// (fail-closed)» e o banner anuncia um crash que não houve.
func TestAOS411_RunVivoNestaReplicaNaoEOrfao(t *testing.T) {
	ctx := context.Background()
	modelo := novoModeloPreso()
	_, node, svc, _, reg := fixtura411(t, modelo)
	defer modelo.soltar()

	const runID = "run-411-vivo-aqui"
	if err := svc.Submit(ctx, agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: durAgent},
		Objective: "run vivo durante a re-varredura",
		MaxTurns:  4,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Sincronização por CANAL: quando o modelo entra, o run já reclamou `running` e já semeou o
	// registo de retoma (AOS-253), e ainda NÃO capturou turno nenhum.
	select {
	case <-modelo.chegou:
	case <-time.After(60 * time.Second):
		t.Fatal("o run vivo nao chegou ao primeiro turno — o teste nao chegou a medir nada")
	}

	// NÃO-VÁCUO: sem estas duas precondições o teste passaria sobre um run que não está vivo nem
	// em `running`, e a guarda nunca seria exercitada.
	if st, serr := node.stateGates.currentState(ctx, runID); serr != nil || st != state.Running {
		t.Fatalf("precondicao: o run vivo tem de estar duravelmente em `running`; st=%v err=%v", st, serr)
	}
	svc.mu.Lock()
	_, hospedado := svc.runs[runID]
	svc.mu.Unlock()
	if !hospedado {
		t.Fatal("precondicao: o run vivo tem de estar no registo de em-curso desta replica")
	}

	reg.limpar()
	orfaos, retomados, err := svc.resumeInterruptedRuns(ctx, false) // re-varredura PERIÓDICA
	if err != nil {
		t.Fatalf("resumeInterruptedRuns: %v", err)
	}
	if orfaos != 0 || retomados != 0 {
		t.Fatalf("um run HOSPEDADO por esta replica NAO e orfao de crash nenhum: orfaos=%d retomados=%d\nlog:\n%s", orfaos, retomados, reg.String())
	}
	// A passagem periódica que só encontrou runs vivos não tem nada a declarar — nem a linha de
	// fail-closed, nem o banner. Foi a existência destas linhas que mandou um operador procurar
	// um crash que não tinha havido.
	for _, proibido := range []string{"ILEGIVEIS", "orfao", "crash-resume"} {
		if reg.tem(proibido) {
			t.Fatalf("a re-varredura falou de um run VIVO (%q) — sinal falso de crash; log:\n%s", proibido, reg.String())
		}
	}

	// E o run continua o seu caminho, intocado pela varredura.
	modelo.soltar()
	wc, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(wc, runID)
	if werr != nil || !ok {
		t.Fatalf("Wait do run vivo: ok=%v err=%v", ok, werr)
	}
	if oc.Err != nil || !oc.Result.Terminated {
		t.Fatalf("o run vivo devia COMPLETAR normalmente; result=%+v err=%v", oc.Result, oc.Err)
	}
}

// TestAOS411_LeaseVivoNoutraReplicaNaoEOrfao tranca o AC2: um run com LEASE VIVO noutra réplica é
// saltado pelo MESMO critério, ANTES do passo 2, e contado à parte.
//
// FALHA-ANTES: hoje o run é reconstituído até às capturas (o registo de retoma está lá de
// propósito, para que a varredura antiga NÃO pare antes disso) e sai como «capturas ILEGIVEIS».
func TestAOS411_LeaseVivoNoutraReplicaNaoEOrfao(t *testing.T) {
	ctx := context.Background()
	store, node, svc, rel, reg := fixtura411(t, &crashResumeModel{finalFrom: 1})

	const runID = "run-411-lease-noutra"
	marcarDuravelmenteRunning(t, store, runID)
	if err := node.ResumeRecords.Put(ctx, resumeRecordFromGoal(agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: durAgent},
		Objective: "run possuido por outra replica",
		MaxTurns:  4,
	})); err != nil {
		t.Fatalf("registo de retoma: %v", err)
	}

	// OUTRA réplica reclama o lease, sobre o MESMO log e o MESMO relógio manual.
	outra, err := durable.NewLeaseManager(store, ttl411,
		durable.WithLeaseClock(durable.ClockFunc(rel.Now)), durable.WithWorkerID("replica-B"))
	if err != nil {
		t.Fatalf("NewLeaseManager(replica-B): %v", err)
	}
	if _, err := outra.Claim(ctx, runID); err != nil {
		t.Fatalf("Claim por replica-B: %v", err)
	}

	reg.limpar()
	orfaos, retomados, err := svc.ResumeInterruptedRuns(ctx) // ARRANQUE: o banner sai sempre
	if err != nil {
		t.Fatalf("ResumeInterruptedRuns: %v", err)
	}
	if orfaos != 0 || retomados != 0 {
		t.Fatalf("um run com LEASE VIVO noutra replica NAO e orfao: orfaos=%d retomados=%d\nlog:\n%s", orfaos, retomados, reg.String())
	}
	if reg.tem("ILEGIVEIS") {
		t.Fatalf("as capturas de um run VIVO noutra replica nao deviam sequer ter sido procuradas; log:\n%s", reg.String())
	}
	if !reg.tem("0 run(s) orfaos") {
		t.Fatalf("o banner devia declarar ZERO orfaos; log:\n%s", reg.String())
	}
	if !reg.tem("1 com LEASE VIVO noutra replica") {
		t.Fatalf("o resumo devia contar o run vivo noutra replica a parte; log:\n%s", reg.String())
	}
}

// TestAOS411_OrfaoVerdadeiroContinuaAVerSeOLeaseExpirou é o guarda de NÃO-REGRESSÃO do AC3: com o
// lease EXPIRADO (relógio avançado para lá do TTL) o run volta a ser órfão e a varredura segue
// exactamente como seguia. A guarda de AOS-411 não reclama leases mais cedo nem mais tarde — lê o
// MESMO predicado que o `Claim` do `submit` usa.
//
// Passa dos DOIS lados da correcção, de propósito: é a prova de que a guarda não come órfãos.
func TestAOS411_OrfaoVerdadeiroContinuaAVerSeOLeaseExpirou(t *testing.T) {
	ctx := context.Background()
	store, _, svc, rel, reg := fixtura411(t, &crashResumeModel{finalFrom: 1})

	const runID = "run-411-orfao-verdadeiro"
	marcarDuravelmenteRunning(t, store, runID)
	outra, err := durable.NewLeaseManager(store, ttl411,
		durable.WithLeaseClock(durable.ClockFunc(rel.Now)), durable.WithWorkerID("replica-morta"))
	if err != nil {
		t.Fatalf("NewLeaseManager(replica-morta): %v", err)
	}
	if _, err := outra.Claim(ctx, runID); err != nil {
		t.Fatalf("Claim por replica-morta: %v", err)
	}

	// A réplica morre: ninguém renova, e o TTL passa.
	rel.avancar(ttl411 + time.Second)

	reg.limpar()
	orfaos, _, err := svc.ResumeInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("ResumeInterruptedRuns: %v", err)
	}
	if orfaos != 1 {
		t.Fatalf("um `running` cujo lease EXPIROU continua a ser orfao; orfaos=%d\nlog:\n%s", orfaos, reg.String())
	}
	// Sem registo de retoma não é reconstituível — o desfecho de sempre, declarado como sempre.
	if !reg.tem("SEM registo de retoma") {
		t.Fatalf("o orfao sem Goal devia ser declarado irreconstituivel; log:\n%s", reg.String())
	}
}

// TestAOS411_BannerDistingueAOrigemDaPassagem tranca o AC4: a re-varredura periódica deixa de se
// anunciar como varredura de arranque, e um ciclo periódico sem órfãos continua MUDO.
//
// FALHA-ANTES: as duas passagens escreviam a mesma linha, «varredura de arranque», e o operador
// foi procurar um restart que não existia (`restarts=0`, arranque às 22:19, linhas das 22:55).
func TestAOS411_BannerDistingueAOrigemDaPassagem(t *testing.T) {
	ctx := context.Background()
	store, _, svc, _, reg := fixtura411(t, &crashResumeModel{finalFrom: 1})

	const runID = "run-411-origem"
	marcarDuravelmenteRunning(t, store, runID) // um órfão verdadeiro faz a periódica falar

	reg.limpar()
	if _, _, err := svc.resumeInterruptedRuns(ctx, false); err != nil {
		t.Fatalf("re-varredura periodica: %v", err)
	}
	if !reg.tem("RE-VARREDURA periodica") {
		t.Fatalf("a passagem periodica tem de se NOMEAR; log:\n%s", reg.String())
	}
	if reg.tem("varredura de arranque") {
		t.Fatalf("a re-varredura periodica anunciou-se como varredura de ARRANQUE num no que nao reiniciou; log:\n%s", reg.String())
	}

	reg.limpar()
	if _, _, err := svc.ResumeInterruptedRuns(ctx); err != nil {
		t.Fatalf("varredura de arranque: %v", err)
	}
	if !reg.tem("varredura de arranque") {
		t.Fatalf("a passagem de ARRANQUE mantem a sua forma (e a pegada do roteiro E2E); log:\n%s", reg.String())
	}
	if reg.tem("RE-VARREDURA periodica") {
		t.Fatalf("a varredura de arranque nao e uma re-varredura; log:\n%s", reg.String())
	}
}

// TestAOS411_CicloPeriodicoSemOrfaosContinuaMudo — a outra metade do AC4, e a razão de ser do
// `anuncia`: uma linha de crash-resume no log significa sempre alguma coisa.
func TestAOS411_CicloPeriodicoSemOrfaosContinuaMudo(t *testing.T) {
	ctx := context.Background()
	_, _, svc, _, reg := fixtura411(t, &crashResumeModel{finalFrom: 1})

	reg.limpar()
	orfaos, retomados, err := svc.resumeInterruptedRuns(ctx, false)
	if err != nil {
		t.Fatalf("re-varredura periodica: %v", err)
	}
	if orfaos != 0 || retomados != 0 {
		t.Fatalf("substrato sem runs: orfaos=%d retomados=%d", orfaos, retomados)
	}
	if reg.String() != "" {
		t.Fatalf("um ciclo periodico sem orfaos tem de ser MUDO; log:\n%s", reg.String())
	}
}
