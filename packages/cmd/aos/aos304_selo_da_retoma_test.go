package main

// AOS-304 — A RETOMA ENTRA NA HASH-CHAIN, COMO A PAUSA.
//
// O defeito: `sealControlAction` tinha cinco chamadores — `steer`, `pause`, `approve`, `autonomy`
// e `nhi_revoke` — e a retoma não era um deles. Nenhum outro `WORM.Append` do pacote a
// compensava. Quem lesse a cadeia tamper-evidente via `control:pause` sobre um run e NÃO via quem
// levantou a pausa: a imposição sem a libertação.
//
// NÃO É A AC3 DE AOS-292, e a distinção é o que torna este ticket separado. Essa pede o evento
// `control.resume` no LOG DE CONTROLO, e esse É escrito — `SteerChannel.Resume` chama
// `appendControl` audit-first, e `TestAOS292_ARetomaSelaControlResume` prova-o reconstruindo um
// canal fresco do log. São dois registos com propriedades diferentes: o Event Store dá replay e
// reconstrução, o WORM dá evidência tamper-evidente. AOS-292 fechou o primeiro. Este fecha o
// segundo.
//
// # PORQUE O SELO NÃO ESTÁ NO HANDLER, COMO OS OUTROS CINCO
//
// `handleResume` devolve 202 assim que `NodeService.Resume` re-submete o run. A retoma pelo canal
// acontece DEPOIS, noutra goroutine, dentro do `hostRun`, e pode ainda falhar. Selar no handler
// gravaria na cadeia uma retoma que podia não ter acontecido — e uma entrada falsa numa cadeia
// cuja única propriedade é a fidedignidade estraga o registo inteiro, não só aquela linha.
//
// O selo vive em `retomarPausaPeloCanal`, depois de o `SteerChannel.Resume` devolver sem erro: aí
// o `control.resume` é durável e a máquina transitou.

import (
	"context"
	"testing"

	control "github.com/aos-ref/kernel/agent-runtime/control"
	durable "github.com/aos-ref/kernel/agent-runtime/durable"
)

// TestAOS304_ARetomaFicaNaHashChainComoAPausa é a AC3, e o par é o ponto.
//
// Não basta afirmar que a retoma sela: o que faltava era a SIMETRIA. O teste exige as duas
// entradas para o MESMO run, e falha se só a pausa lá estiver — que era exactamente o estado
// anterior, e um estado em que um teste sobre a retoma sozinha passaria a verde sem nada mudar.
func TestAOS304_ARetomaFicaNaHashChainComoAPausa(t *testing.T) {
	node, svc, h, priv, opID := newA3Node(t)
	const runID = "run-aos304-par"
	ctx := context.Background()

	// ÂNCORA DE NÃO-VACUIDADE: a partição começa vazia, pelo que os registos encontrados no fim
	// são destes dois sinais e não de outra coisa qualquer.
	if antes := selosDeControlo(t, node); len(antes) != 0 {
		t.Fatalf("a particao de controlo devia comecar vazia, tinha %d", len(antes))
	}

	// (1) Um run RETOMÁVEL e uma máquina em `running` — a receita de `runPausado`. NÃO se
	// submete: um run desta fixture corre até ao fim, e uma máquina em estado terminal não aceita
	// a pausa graciosa que este teste precisa de encenar.
	gate := aos304RunPausavel(t, node, runID)

	// (2) PAUSA pela rota REAL — é ela que sela, e é o lado que já funcionava.
	rec := postReq(h, "/runs/"+runID+"/pause",
		pauseRequest{Emitter: a3Emitter(t, priv, opID, runID, control.SignalPause, nil)}, nil)
	if rec.Code != 202 {
		t.Fatalf("pause autenticado devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if err := gate.Pause(ctx, "pausa graciosa de teste"); err != nil {
		t.Fatalf("Pause do gate: %v", err)
	}

	// (3) RETOMA pelo canal — o caminho que o `hostRun` percorre, e onde o selo novo vive.
	em, err := a3Emitter(t, priv, opID, runID, control.SignalResume, nil).decode()
	if err != nil {
		t.Fatalf("decode do emissor de retoma: %v", err)
	}
	if err := svc.retomarPausaPeloCanal(withResumeEmitter(ctx, em), runID, gate); err != nil {
		t.Fatalf("retomarPausaPeloCanal: %v", err)
	}

	// (4) O PAR. Uma cadeia com a pausa e sem a retoma é o defeito; uma cadeia com a retoma e sem
	// a pausa seria outro.
	selos := selosDeControlo(t, node)
	porCap := map[string]int{}
	for _, s := range selos {
		porCap[s.Capability]++
		if s.RunID != runID {
			t.Errorf("selo %q amarrado ao run errado: %q", s.Capability, s.RunID)
		}
		if s.Principal.NHIID != opID {
			t.Errorf("selo %q sem o emissor (%q): veio %q — um registo que diz que houve intervencao sem dizer de quem nao serve o nao-repudio",
				s.Capability, opID, s.Principal.NHIID)
		}
	}
	if porCap["control:pause"] != 1 {
		t.Errorf("a cadeia devia ter exactamente um control:pause, tem %d", porCap["control:pause"])
	}
	if porCap["control:resume"] != 1 {
		t.Errorf("a cadeia NAO tem control:resume (AOS-304): quem pausou fica no registo e quem retomou nao; selos=%v", porCap)
	}
	if len(selos) != 2 {
		t.Errorf("a particao devia ter exactamente os dois selos do ciclo, tem %d: %v", len(selos), porCap)
	}
}

// TestAOS304_UmaRetomaRECUSADANaoSela é a face que impede o selo de virar vector, e é a mesma
// disciplina de `TestA3_SinalRecusado_NaoSela`: só se sela o que SURTIU EFEITO.
//
// Aqui a retoma é recusada por falta de emissor — `retomarPausaPeloCanal` devolve
// [ErrResumeNeedsEmitter] antes de tocar no canal. Se selasse à mesma, quem inundasse a rota com
// retomas sem autoridade inchava a cadeia tamper-evidente sem nunca retomar nada.
func TestAOS304_UmaRetomaRECUSADANaoSela(t *testing.T) {
	node, svc, h, priv, opID := newA3Node(t)
	const runID = "run-aos304-recusada"
	ctx := context.Background()
	gate := aos304RunPausavel(t, node, runID)
	rec := postReq(h, "/runs/"+runID+"/pause",
		pauseRequest{Emitter: a3Emitter(t, priv, opID, runID, control.SignalPause, nil)}, nil)
	if rec.Code != 202 {
		t.Fatalf("pause: %d (%s)", rec.Code, rec.Body.String())
	}

	// SEM emissor no contexto.
	if err := svc.retomarPausaPeloCanal(ctx, runID, gate); err == nil {
		t.Fatal("uma retoma sem emissor devia ser recusada")
	}

	selos := selosDeControlo(t, node)
	for _, s := range selos {
		if s.Capability == "control:resume" {
			t.Fatal("uma retoma RECUSADA entrou na hash-chain — o selo passa a poder ser inchado por quem nao tem autoridade nenhuma")
		}
	}
	if len(selos) != 1 {
		t.Fatalf("devia ficar so o selo da pausa, ficaram %d", len(selos))
	}
}

// aos304RunPausavel põe a máquina do run em `running` e devolve o seu gate — o estado mínimo em
// que a pausa graciosa e a retoma pelo canal correm.
//
// NÃO submete o run: um run desta fixture corre até ao fim, e sobre uma máquina em estado terminal
// a pausa graciosa é uma transição inválida. E não precisa de registo de retoma: o
// `retomarPausaPeloCanal` não consulta o `ResumeRecords` — só o canal, o emissor do contexto e o
// gate. Encenar mais do que isso seria uma fixture a medir outro caminho.
func aos304RunPausavel(t *testing.T, node *Node, runID string) *runGate {
	t.Helper()
	if err := node.stateGates.Open(context.Background(), runID, durable.FencingToken(1)); err != nil {
		t.Fatalf("Open(%s): %v", runID, err)
	}
	gate := node.stateGates.resolveGate(runID)
	if gate == nil {
		t.Fatal("o gate nao apareceu depois do Open")
	}
	return gate
}
