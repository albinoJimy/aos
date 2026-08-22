package main

import (
	"context"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
)

// ---------------------------------------------------------------------------------------------
// A CERIMÓNIA NÃO ACONTECE NO VAZIO.
//
// Desde o achado 1.10 (2026-08-22), o `POST /runs/{id}/approve` só aprova efeitos que o NÓ
// escalou: procura a preview no registo de pendentes e é dela que tira quantos aprovadores a
// acção exige. Uma preview que o nó não reconhece é recusada — sem isso, quem pede escolheria a
// preview E o número de humanos, e a amarra WYSIWYS compararia o grant com a preview inventada.
//
// Os testes de cerimónia que existiam antes construíam a preview à mão e chamavam a rota
// directamente, sem a escalada que em produção a precede SEMPRE (o RM escala, o run suspende, o
// operador aprova). Passam a montar essa pré-condição — e ficam mais fortes por isso: a
// capability semeada é agora o que decide se uma perna basta ou se são precisas duas.
// ---------------------------------------------------------------------------------------------

const (
	// capReversivelDeTeste é lida por [rm.CapabilityIrreversible] como NÃO-irreversível: uma perna.
	capReversivelDeTeste = "cap:fs.read"
	// capIrreversivelDeTeste contém um token de acção destrutiva: duas pernas, decidido pelo nó.
	capIrreversivelDeTeste = "cap:storage.delete"
)

// semearPendente regista a escalada que a cerimónia vai aprovar.
func semearPendente(t *testing.T, node *Node, runID, capability string, preview []byte) {
	t.Helper()
	if node.PendingApprovals == nil {
		t.Fatal("registo de pendentes nao composto — a cerimonia seria recusada e o teste mediria outra coisa")
	}
	if err := node.PendingApprovals.Put(context.Background(), integration.PendingRecord{
		RunID:      runID,
		StepID:     "step-semeado",
		Turn:       1,
		ToolID:     "tool-semeado",
		Capability: capability,
		Preview:    preview,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("semear pendente: %v", err)
	}
}
