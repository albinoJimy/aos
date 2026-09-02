package main

// AOS-292 — A RETOMA PASSA A FECHAR O CICLO DO CANAL DE STEER.
//
// O defeito: `POST /runs/{id}/resume` transitava `paused→running` DIRECTAMENTE na máquina de
// estados (o ramo `Paused` do `resumeIfWaiting`), sem tocar no canal. O único sítio que limpa
// `pauseRequested` é o ramo `SignalResume` do canal, alcançável só por `SteerChannel.Resume`.
//
// Duas consequências, e a segunda é a que se vê em produção:
//
//  1. a pausa continuava «em efeito» na projecção, pelo que o `GracefulPause` a via outra vez
//     na fronteira do primeiro turno e o run RE-PAUSAVA;
//  2. a correcção do operador nunca era entregue — aceite, selada, e ignorada.
//
// E havia um terceiro, que o ticket não nomeava e que a implementação expôs: a pausa de um
// OPERADOR era levantada por quem detivesse a credencial NHI do run, sem assinar nada. O
// `POST /pause` sempre exigiu operador pinado; o `/resume` não exigia ninguém.
//
// # ALCANCE DESTES TESTES, E O QUE FICA POR COBRIR
//
// Cobrem a decisão e o efeito no canal, entrando por `NodeService.retomarPausaPeloCanal` — o
// ponto onde a retoma da pausa é decidida, e onde os três defeitos viviam.
//
// NÃO cobrem o ciclo por HTTP ponta-a-ponta (`POST /pause` → `/steer` → `/resume`), que é o
// que a AC2 pede. A razão é de fixture e está reconhecida no próprio repositório
// (`aos263_decisao_simetrica_test.go`): um run destas fixtures «nunca correu um turno de
// modelo a sério, pelo que não tem capturas para reproduzir e a retoma acaba por falhar mais à
// frente, no plano de replay». Nenhum teste do repo consegue hoje uma retoma HTTP bem-sucedida.
// Fechar a AC2 exige uma fixture que corra um turno real antes de pausar — trabalho próprio,
// declarado por fazer.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	integration "github.com/aos-ref/integration"
	control "github.com/aos-ref/kernel/agent-runtime/control"
)

// aos292Pausado devolve um nó com um run pausado NA MÁQUINA e com a pausa registada NO CANAL —
// o estado que o caminho real produz, e que `runPausado` sozinho não dá (ele pausa pelo gate,
// deixando a projecção do canal vazia).
func aos292Pausado(t *testing.T, runID string) (*Node, *NodeService, *runGate, ed25519.PrivateKey) {
	t.Helper()
	node, svc, _ := runPausado(t, runID)
	priv := aos292OperatorKey(t, node)

	emit := aos292Assinado(t, priv, runID, control.SignalPause, nil)
	if err := node.Steer.Pause(context.Background(), runID, emit); err != nil {
		t.Fatalf("Pause no canal: %v", err)
	}
	if !node.Steer.PendingPause(runID) {
		t.Fatal("premissa: a pausa devia estar pendente na projeccao do canal")
	}
	gate := node.stateGates.resolveGate(runID)
	if gate == nil {
		t.Fatal("premissa: o gate do run devia existir")
	}
	return node, svc, gate, priv
}

// TestAOS292_SemEmissorAPausaNaoSeLevanta fixa a autoridade: a pausa foi imposta por um
// operador e é um operador que a levanta. Antes, a credencial do run bastava — e o `/pause`
// sempre exigiu operador pinado, pelo que a assimetria era só do lado da saída.
func TestAOS292_SemEmissorAPausaNaoSeLevanta(t *testing.T) {
	const run = "run-aos292-sem-emissor"
	node, svc, gate, _ := aos292Pausado(t, run)

	err := svc.retomarPausaPeloCanal(context.Background(), run, gate)
	if !errors.Is(err, ErrResumeNeedsEmitter) {
		t.Fatalf("retoma sem emissor = %v, quero ErrResumeNeedsEmitter", err)
	}
	// A recusa não pode deixar o sistema a meio.
	if !node.Steer.PendingPause(run) {
		t.Fatal("a recusa levantou a pausa na mesma")
	}
}

// TestAOS292_ComEmissorLimpaAPausaEPreservaACorreccao é o coração do ticket.
//
// Prova as duas metades que estavam partidas: a retoma passa PELO CANAL (a pausa deixa de
// estar pendente — antes ficava, e o run re-pausava no primeiro turno) e a correcção SOBREVIVE
// à retoma, para o loop a injectar no turno seguinte. Que o loop a injecta quando está
// pendente é o que `TestNodeSteerCorrectionReachesLoop` (AOS-218) já prova; as duas juntas
// fecham o caminho, e nenhuma sozinha o faz.
func TestAOS292_ComEmissorLimpaAPausaEPreservaACorreccao(t *testing.T) {
	const run = "run-aos292-ciclo"
	node, svc, gate, priv := aos292Pausado(t, run)
	ctx := context.Background()
	correction := []byte("prioriza a superficie desktop")

	steerEmit := aos292Assinado(t, priv, run, control.SignalSteer, correction)
	if err := node.Steer.Steer(ctx, run, correction, steerEmit); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if _, ok := node.Steer.PendingCorrection(run); !ok {
		t.Fatal("premissa: a correccao devia estar pendente depois do steer")
	}

	resumeCtx := withResumeEmitter(ctx, aos292Assinado(t, priv, run, control.SignalResume, nil))
	if err := svc.retomarPausaPeloCanal(resumeCtx, run, gate); err != nil {
		t.Fatalf("retoma assinada: %v", err)
	}

	// (1) A PAUSA DEIXOU DE ESTAR PENDENTE. Era isto que faltava.
	if node.Steer.PendingPause(run) {
		t.Fatal("apos a retoma a pausa AINDA esta pendente — nao passou pelo canal, e o run re-pausa no primeiro turno")
	}
	// (2) A CORRECÇÃO SOBREVIVEU. O `Resume` deixou de a consumir: quem a consome é a ENTREGA
	// ao loop. Consumida aqui, o loop — que só a lê DEPOIS de a pausa levantar — não
	// encontraria nada, e a correcção morria na retoma.
	if _, ok := node.Steer.PendingCorrection(run); !ok {
		t.Fatal("a correccao foi consumida pela retoma — o loop nao tera nada para injectar")
	}
	// (3) A máquina transitou: sem isto, o disjuntor, o selo terminal e o deadline ficariam
	// desarmados, porque os três exigem `running`.
	if got := gate.m.Current(); string(got) != "running" {
		t.Fatalf("maquina em %q apos a retoma, quero running", got)
	}
}

// TestAOS292_ARetomaSelaControlResume fecha a AC3: quem pausou ficava no registo e quem
// retomou não, porque a rota não passava pelo canal. O selo sempre existiu — faltava chamador.
func TestAOS292_ARetomaSelaControlResume(t *testing.T) {
	const run = "run-aos292-selo"
	node, svc, gate, priv := aos292Pausado(t, run)
	ctx := withResumeEmitter(context.Background(), aos292Assinado(t, priv, run, control.SignalResume, nil))

	if err := svc.retomarPausaPeloCanal(ctx, run, gate); err != nil {
		t.Fatalf("retoma: %v", err)
	}

	// Um canal FRESCO que dobra o LOG tem de ver a pausa levantada — só verdade se o
	// `control.resume` tiver mesmo sido escrito. É a diferença entre limpar a projecção em
	// memória e registar o facto.
	fresh, err := control.NewChannel(node.EventStore, node.SteerAuth)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	if err := fresh.Rebuild(context.Background(), run); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if fresh.PendingPause(run) {
		t.Fatal("o log nao tem control.resume: um canal reconstruido do zero ainda ve a pausa pendente")
	}
}

func aos292OperatorKey(t *testing.T, node *Node) ed25519.PrivateKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if node.SteerAuth == nil {
		t.Fatal("o no nao tem autenticador de canal composto")
	}
	// A fixture não devolve a chave que registou, por isso regista-se outra sob o mesmo id.
	node.SteerAuth.Register(aos263OperatorID, pub)
	return priv
}

func aos292Assinado(t *testing.T, priv ed25519.PrivateKey, runID string, kind control.SignalKind, payload []byte) control.Emitter {
	t.Helper()
	// NONCE ÚNICO POR SINAL: o autenticador consome-o de forma DURÁVEL e de uso único, pelo
	// que reutilizá-lo entre o pause, o steer e o resume seria replay — e a recusa leria-se
	// como assinatura errada, mandando quem depura procurar no sítio errado.
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i) ^ kind[0] ^ byte(len(kind))
	}
	return integration.SignSignal(priv, aos263OperatorID, runID, kind, payload, nonce, tnClock()())
}
