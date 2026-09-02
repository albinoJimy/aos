package main

// AOS-293 — A PROJECÇÃO DO CANAL DE CONTROLO PASSA A SER RECONSTRUÍDA.
//
// `SteerChannel.Rebuild` não tinha um único chamador de produção. O log de controlo É durável
// — `control.pause`, `control.steer`, `control.resume` e `control.correction_consumed` são
// todos escritos —, mas depois de um reinício `c.runs` está vazio, e o `control/doc.go`
// prometia o contrário, por escrito: «um crash em paused → um worker novo relê o log e recupera
// a correcção INTACTA».
//
// # O REINÍCIO AQUI É REAL, E É POR ISSO QUE ESTE TESTE VALE
//
// Não se simula o restart com um canal fresco construído à mão — isso é o que
// `control/steer_channel_test.go` já faz, e prova o MECANISMO, não a cablagem. Foi precisamente
// por o mecanismo estar provado que o defeito passou: ninguém o chamava.
//
// Aqui faz-se um `Bootstrap` NOVO sobre o MESMO WAL em disco. É um nó novo, com um
// `SteerChannel` novo e uma projecção genuinamente vazia, a ler o log que o nó anterior
// escreveu. O que o teste não faz é matar um processo do sistema operativo — e isso não é
// material para esta propriedade, porque o que se perde num restart é exactamente a projecção
// em memória, e essa perde-se aqui.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"path/filepath"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// aos293Node arranca um nó com WAL e WORM em disco, num directório dado — para o segundo
// arranque poder reler o que o primeiro escreveu.
func aos293Node(t *testing.T, dir string, opPub ed25519.PublicKey) *Node {
	t.Helper()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Operators = map[string]ed25519.PublicKey{aos293OperatorID: opPub}
	cfg.SteerClock = tnClock()
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	return node
}

const aos293OperatorID = "human:operator-aos293"

// TestAOS293_ACorreccaoSobreviveAoReinicioDoNo é a AC2, e o reinício é genuíno.
//
// Prova a cadeia inteira: um nó escreve pause+steer no log durável, morre, um nó NOVO nasce
// sobre o mesmo WAL com a projecção vazia, e depois de a reconstruir devolve a correcção que o
// operador tinha escrito. Sem o `Rebuild`, o nó novo devolve (nil, false) e a correcção
// desaparece em silêncio — que é o defeito.
func TestAOS293_ACorreccaoSobreviveAoReinicioDoNo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const run = "run-aos293-correccao"
	correction := []byte("prioriza a superficie desktop")

	// (1) O nó que escreve.
	primeiro := aos293Node(t, dir, opPub)
	if err := primeiro.Steer.Pause(ctx, run, aos293Assinado(t, opPriv, run, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := primeiro.Steer.Steer(ctx, run, correction, aos293Assinado(t, opPriv, run, control.SignalSteer, correction)); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if _, ok := primeiro.Steer.PendingCorrection(run); !ok {
		t.Fatal("premissa: o primeiro no devia ter a correccao pendente")
	}
	_ = primeiro.Close()

	// (2) O REINÍCIO: nó novo, canal novo, MESMO WAL.
	segundo := aos293Node(t, dir, opPub)
	if _, ok := segundo.Steer.PendingCorrection(run); ok {
		t.Fatal("premissa invalida: o no novo ja sabia sem Rebuild — a projeccao deixou de ser em memoria?")
	}

	// (3) A reconstrução — o que `hostRun` passa a fazer em cada hospedagem.
	if err := segundo.Steer.Rebuild(ctx, run); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got, ok := segundo.Steer.PendingCorrection(run)
	if !ok {
		t.Fatal("apos o reinicio a correccao do operador desapareceu — o log e duravel e a projeccao nao o dobra")
	}
	if string(got) != string(correction) {
		t.Fatalf("correccao reconstruida = %q, quero %q", got, correction)
	}
	// A pausa também: sem ela, desde AOS-292 a retoma pelo canal recusa e o run pausado deixa
	// de se conseguir retomar de todo.
	if !segundo.Steer.PendingPause(run) {
		t.Fatal("apos o reinicio a pausa desapareceu — SteerChannel.Resume vai recusar e o run fica preso")
	}
}

// TestAOS293_UmaCorreccaoJAENTREGUENaoRessuscita é o outro lado, e é o que impede o rebuild de
// se tornar um defeito próprio.
//
// Se o log já tiver `control.correction_consumed` (AOS-292), reconstruir NÃO pode repor a
// correcção: o loop injectá-la-ia segunda vez, num turno cujo prompt já foi capturado, e o
// replay divergiria por `prompt_hash`. É a de-duplicação durável a fazer o seu trabalho através
// de um reinício — o `applied` in-process do `LoopSteer` não sobrevive e não podia ajudar.
func TestAOS293_UmaCorreccaoJAENTREGUENaoRessuscita(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const run = "run-aos293-consumida"

	primeiro := aos293Node(t, dir, opPub)
	if err := primeiro.Steer.Pause(ctx, run, aos293Assinado(t, opPriv, run, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	corr := []byte("ja entregue ao loop")
	if err := primeiro.Steer.Steer(ctx, run, corr, aos293Assinado(t, opPriv, run, control.SignalSteer, corr)); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	// A ENTREGA ao loop, que é o que a consome duravelmente.
	if consumida, err := primeiro.Steer.ConsumeCorrection(ctx, run); err != nil || !consumida {
		t.Fatalf("ConsumeCorrection = (%v, %v), quero (true, nil)", consumida, err)
	}
	_ = primeiro.Close()

	segundo := aos293Node(t, dir, opPub)
	if err := segundo.Steer.Rebuild(ctx, run); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, ok := segundo.Steer.PendingCorrection(run); ok {
		t.Fatal("uma correccao JA ENTREGUE ressuscitou no reinicio — o loop injecta-la-ia segunda vez e o replay divergiria")
	}
	// A pausa continua pendente: consumir a correcção não a levanta.
	if !segundo.Steer.PendingPause(run) {
		t.Fatal("consumir a correccao nao pode levantar a pausa")
	}
}

func aos293Assinado(t *testing.T, priv ed25519.PrivateKey, runID string, kind control.SignalKind, payload []byte) control.Emitter {
	t.Helper()
	// Nonce único por sinal — o autenticador consome-os de forma durável e de uso único.
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i) ^ kind[0] ^ byte(len(kind))
	}
	return integration.SignSignal(priv, aos293OperatorID, runID, kind, payload, nonce, tnClock()())
}

// TestAOS293_AHospedagemRECONSTROIAProjeccao é a AC1, e é a que impede o defeito de voltar.
//
// Os dois testes acima provam o MECANISMO — que era o que já estava provado quando o defeito
// existia, e foi por isso que ele passou: `SteerChannel.Rebuild` tinha testes e zero chamadores.
// Este prova a CABLAGEM: que uma hospedagem real reconstrói a projecção sem ninguém lhe pedir.
//
// O run é NOVO e nunca foi materializado como pausado — só o SINAL de pausa está no log. Assim
// a máquina fica em `ready`, o `resumeIfWaiting` não toca em nada, e o run corre até ao fim sem
// precisar de capturas nem de emissor de retoma. O que se observa depois é se a projecção do
// segundo nó ficou preenchida — e só o `hostRun` lha podia ter dado.
func TestAOS293_AHospedagemRECONSTROIAProjeccao(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	const run = "run-aos293-cablagem"
	corr := []byte("correccao escrita antes do reinicio")

	// (1) O nó que escreve os sinais no log — sem os materializar na máquina.
	primeiro := aos293Node(t, dir, opPub)
	if err := primeiro.Steer.Pause(ctx, run, aos293Assinado(t, opPriv, run, control.SignalPause, nil)); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := primeiro.Steer.Steer(ctx, run, corr, aos293Assinado(t, opPriv, run, control.SignalSteer, corr)); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	_ = primeiro.Close()

	// (2) Reinício: nó novo, projecção vazia.
	segundo := aos293Node(t, dir, opPub)
	if _, ok := segundo.Steer.PendingCorrection(run); ok {
		t.Fatal("premissa invalida: o no novo nao devia saber nada antes de hospedar")
	}

	// (3) HOSPEDA o run pelo caminho real do serviço. Ninguém chama Rebuild explicitamente.
	svc, err := NewNodeService(segundo, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	if err := svc.Submit(ctx, svcGoal(run, "trabalho qualquer")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, _, err := svc.Wait(ctx, run); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// (4) A projecção tem de estar preenchida — e o único caminho por onde isso podia
	// acontecer é o `hostRun` ter chamado o `Rebuild`.
	if !segundo.Steer.PendingPause(run) {
		t.Fatal("a hospedagem NAO reconstruiu a projeccao do canal: a pausa do log nao chegou a memoria (AOS-293)")
	}
}
