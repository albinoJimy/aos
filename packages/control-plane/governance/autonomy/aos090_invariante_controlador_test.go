package autonomy

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/platform/audit"
)

// AOS-090 / DEF-908 — O INVARIANTE QUE TORNA A DEMOCAO AUTOMATICA DURAVEL.
//
// O controlador escreve no WORM com [ControllerActor] e SEM prova assinada: nao ha operador
// nem cerimonia numa democao automatica. Sem um invariante, a releitura teria de escolher
// entre duas derrotas — rejeitar (e perder toda a democao legitima no reinicio seguinte) ou
// aceitar (e dar a quem escreve o ficheiro um terceiro actor forjavel, que escolhe o nivel).
//
// A saida e a DIRECCAO: o controlador so desce. Estes testes fixam-na, e fixam sobretudo a
// forma que a torna nao-trivial — a comparacao e contra o ESTADO DO REPLAY, nunca contra os
// campos que o proprio registo declara.

// TestAOS090ControladorQueDesceEReidratado — o caso legitimo. Um par posto em L5 por
// operador e depois despromovido para L3 pelo controlador tem de sobreviver ao reinicio.
// Sem isto, o defeito que DEF-908 nomeia («um par promovido a L5 fica a L5 ate alguem
// reparar») sobrevive a propria correccao: a democao acontecia e evaporava-se no boot.
func TestAOS090ControladorQueDesceEReidratado(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	semear := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L5, "decisao assinada", "op:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L3, "anomalia unsafe_action", ControllerActor); err != nil {
		t.Fatal(err)
	}

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	if len(rep.Rejeitados) != 0 {
		t.Fatalf("uma democao que DESCE nao pode ser rejeitada: %+v", rep.Rejeitados)
	}
	if rep.Applied != 2 {
		t.Fatalf("Applied = %d; quero 2 (o selo do operador e a democao)", rep.Applied)
	}
	if got := r.LevelFor("agt-1", "fs"); got != L3 {
		t.Fatalf("LevelFor = %s; quero L3 — a democao automatica TEM de sobreviver ao reinicio", got)
	}
}

// TestAOS090ElevacaoDisfarcadaDeDemocaoERecusada — o teste que da substancia ao invariante.
//
// O adversario que escreve o ficheiro do WORM (o EntryHash e um SHA-256 SEM chave) apende um
// registo bem-formado com [ControllerActor] cujos campos declaram `L5 -> L3` — que LIDO
// ISOLADAMENTE parece uma democao. Mas o par vale L1 no replay, pelo que aplica-lo ELEVARIA
// de L1 para L3. Comparar contra o estado acumulado — e nao contra o `old_level` que o
// proprio registo reclama — e o que separa o invariante de uma verificacao decorativa.
func TestAOS090ElevacaoDisfarcadaDeDemocaoERecusada(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	semear := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	// O estado real do par: L1.
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L1, "decisao assinada", "op:alice"); err != nil {
		t.Fatal(err)
	}
	// O registo FORJADO. `Old` diz L5 — e mentira, e o invariante nunca lhe toca.
	forjado := BuildLevelChangedRecord(LevelChange{
		Agent: "agt-1", Domain: "fs", Old: L5, New: L3,
		Reason: "anomalia unsafe_action", Actor: ControllerActor,
	}, "")
	if _, err := store.Append(ctx, forjado); err != nil {
		t.Fatal(err)
	}

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatalf("um registo recusado SALTA, nao aborta: %v", err)
	}
	if got := r.LevelFor("agt-1", "fs"); got != L1 {
		t.Fatalf("LevelFor = %s; quero L1 — um registo de controlador NUNCA pode elevar", got)
	}
	if len(rep.Rejeitados) != 1 {
		t.Fatalf("a recusa tem de ser DECLARADA, nao silenciosa: %+v", rep.Rejeitados)
	}
	rej := rep.Rejeitados[0]
	if rej.AuditSeq != 2 || rej.Actor != ControllerActor || rej.Level != L3 {
		t.Errorf("a rejeicao tem de nomear o que o registo reclamava: %+v", rej)
	}
	if !strings.Contains(rej.Motivo, "L1") {
		t.Errorf("o motivo tem de nomear o estado do REPLAY (L1), senao nao se distingue de uma recusa por forma: %q", rej.Motivo)
	}
	// O motivo viaja como STRING na [RehydrateRejection] (o tipo carrega `Motivo string`,
	// nao um error), pelo que a identificacao do sentinela e por conteudo — `errors.Is` sobre
	// uma string re-embrulhada nunca casaria, e escreve-lo daria uma verificacao decorativa.
	if !strings.Contains(rej.Motivo, ErrControladorNaoDesce.Error()) {
		t.Errorf("o motivo tem de identificar o invariante violado: %q", rej.Motivo)
	}
}

// TestAOS090ControladorLateralERecusado — `>=` e nao `>`. Um registo que repete o nivel
// actual nao desce: aceita-lo daria ao forjador uma escrita gratuita no historico e, pior,
// tornaria o invariante dependente de o nivel ser ESTRITAMENTE menor nalguns caminhos e nao
// noutros. A regra e uma so.
func TestAOS090ControladorLateralERecusado(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	semear := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L3, "decisao assinada", "op:alice"); err != nil {
		t.Fatal(err)
	}
	lateral := BuildLevelChangedRecord(LevelChange{
		Agent: "agt-1", Domain: "fs", Old: L3, New: L3,
		Reason: "anomalia", Actor: ControllerActor,
	}, "")
	if _, err := store.Append(ctx, lateral); err != nil {
		t.Fatal(err)
	}

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejeitados) != 1 {
		t.Fatalf("um registo lateral (L3->L3) tem de ser recusado: %+v", rep.Rejeitados)
	}
	if got := r.LevelFor("agt-1", "fs"); got != L3 {
		t.Errorf("LevelFor = %s; quero L3", got)
	}
}

// TestAOS090PisoEOEstadoInicialDoReplay — um par SEM registo anterior vale o piso, tal como
// [LevelRegistry.LevelFor] o trata. O controlador nao pode «descer» a partir do nada: sem
// esta regra, um forjador escolheria o primeiro registo de um par novo e punha-o onde
// quisesse abaixo do tecto.
func TestAOS090PisoEOEstadoInicialDoReplay(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	// Par SEM historico: o unico registo e do controlador, a pedir L3.
	rec := BuildLevelChangedRecord(LevelChange{
		Agent: "agt-novo", Domain: "fs", Old: L5, New: L3,
		Reason: "anomalia", Actor: ControllerActor,
	}, "")
	if _, err := store.Append(ctx, rec); err != nil {
		t.Fatal(err)
	}

	// Piso L2: L3 NAO desce face a L2 — recusado.
	r := NewLevelRegistry(WithDefaultLevel(L2))
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejeitados) != 1 || r.LevelFor("agt-novo", "fs") != L2 {
		t.Fatalf("com piso L2 o registo tem de ser recusado: rejeitados=%+v nivel=%s",
			rep.Rejeitados, r.LevelFor("agt-novo", "fs"))
	}

	// CONTROLO — com piso L4 o MESMO registo desce, e e aceite. Sem este controlo, o teste
	// acima passaria tambem se o invariante recusasse SEMPRE registos de controlador.
	r2 := NewLevelRegistry(WithDefaultLevel(L4))
	rep2, err := r2.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2.Rejeitados) != 0 || rep2.Applied != 1 {
		t.Fatalf("com piso L4 o registo DESCE e tem de ser aceite: rejeitados=%+v applied=%d",
			rep2.Rejeitados, rep2.Applied)
	}
	if got := r2.LevelFor("agt-novo", "fs"); got != L3 {
		t.Fatalf("LevelFor = %s; quero L3", got)
	}
}

// TestAOS090InvarianteSoAlcancaOControlador — o invariante nao pode contaminar os outros
// actores. Um operador SOBE de L1 para L5 e isso tem de continuar a ser aceite: e a
// promocao assinada, que e a razao de ser da rota. Sem este controlo, um invariante
// demasiado largo transformaria a rehidratacao numa cremalheira so-descendente.
func TestAOS090InvarianteSoAlcancaOControlador(t *testing.T) {
	ctx := context.Background()
	store := audit.NewMemStore()
	semear := NewLevelRegistry(WithSink(NewAuditSink(store, "")))
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L1, "primeira", "op:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := semear.SetLevel(ctx, "agt-1", "fs", L5, "promocao assinada", "op:alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := semear.SetLevel(ctx, "agt-2", "http", L4, "provisionamento", "config:node"); err != nil {
		t.Fatal(err)
	}

	r := NewLevelRegistry()
	rep, err := r.Rehydrate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejeitados) != 0 {
		t.Fatalf("nenhum registo de operador ou de config pode ser tocado pelo invariante: %+v", rep.Rejeitados)
	}
	if got := r.LevelFor("agt-1", "fs"); got != L5 {
		t.Errorf("LevelFor(agt-1/fs) = %s; quero L5 — a promocao assinada tem de subir", got)
	}
	if got := r.LevelFor("agt-2", "http"); got != L4 {
		t.Errorf("LevelFor(agt-2/http) = %s; quero L4", got)
	}
}
