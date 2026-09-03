package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// Achado de revisão de segurança sobre AOS-311 — o SELO PÓS-EFEITO NÃO PODE SER CANCELÁVEL.
//
// O AOS-311 pôs o `audit.FileStore.Append` a respeitar o `ctx`, que é a correcção certa para o
// caminho audit-BEFORE-effect do Reference Monitor: um prazo esgotado tem de dar deny. Mas os
// cinco selos de acção de controlo correm DEPOIS do efeito, com o contexto do PEDIDO, e o erro
// do Append é engolido num log. Isso tornou-os canceláveis por quem os provoca: um operador
// legítimo emite `pause` assinado, corta a ligação TCP, a pausa aplica-se e o trilho
// `governance.control` fica sem quem a exerceu. Um primitivo de supressão de auditoria.
//
// A correcção é `context.WithoutCancel` com prazo próprio, o idioma já usado em
// `packages/integration/budget.go`. Este teste prova as duas metades: o selo sobrevive a um
// contexto cancelado, e o store CONTINUA a honrar o cancelamento quando o contexto é o do
// caminho que deve falhar (senão a correcção teria sido desligar o AOS-311).

// TestSeloDeControloSobreviveAContextoCancelado — a metade que importa: contexto do pedido morto,
// selo escrito na mesma.
func TestSeloDeControloSobreviveAContextoCancelado(t *testing.T) {
	worm := audit.NewMemStore()
	h := &apiHandler{
		node: &Node{WORM: worm},
		cfg:  apiConfig{maxBodyBytes: 1 << 20, now: time.Now},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // o cliente foi-se embora ANTES de selarmos

	h.sealControlAction(ctx, "pause", "run-1", "op:jimy")

	head, err := worm.Head(context.Background(), controlSealPartition)
	if err != nil {
		t.Fatal(err)
	}
	if head != 1 {
		t.Fatalf("head=%d, quero 1 — o selo de um efeito JA APLICADO nao pode ser cancelavel por quem o provocou", head)
	}
	recs, err := worm.Read(context.Background(), controlSealPartition, 1, 1)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ler selo: %v (%d)", err, len(recs))
	}
	if recs[0].Principal.NHIID != "op:jimy" || recs[0].Capability != "control:pause" {
		t.Errorf("o selo nao nomeia quem exerceu a accao: %+v", recs[0])
	}
}

// TestAppendDeAuditoriaContinuaAHonrarOContexto — o CONTROLO. Se este teste falhasse, a correcção
// acima teria sido feita desligando o AOS-311, e o fail-closed por prazo do Reference Monitor
// (que é o que o ticket entregou) deixaria de valer.
func TestAppendDeAuditoriaContinuaAHonrarOContexto(t *testing.T) {
	worm := audit.NewMemStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := worm.Append(ctx, audit.AuditRecord{Partition: "x", Decision: audit.DecisionAllow}); !errors.Is(err, context.Canceled) {
		t.Fatalf("o store tem de continuar a recusar sob contexto morto (AOS-311), veio: %v", err)
	}
	if head, _ := worm.Head(context.Background(), "x"); head != 0 {
		t.Fatalf("escreveu apesar do contexto cancelado (head=%d)", head)
	}
}

// TestSeloDeAutonomiaSobreviveAContextoCancelado — o mesmo pela rota real: `POST /autonomy` com o
// contexto do pedido já cancelado. A mudança de nível é selada ANTES de aplicar (AOS-306), pelo
// que aqui o que se mede é o SEGUNDO selo, o da acção de controlo.
func TestSeloDeAutonomiaSobreviveAContextoCancelado(t *testing.T) {
	h, priv, worm := noParaTeste(t)

	req := pedidoDeAutonomia(t, priv, "human:op", "agt-1", "fs", "L2", "com o cliente a desligar")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	w := httptest.NewRecorder()
	h.handleAutonomySet(w, req.WithContext(ctx))

	// A selagem da MUDANÇA usa o contexto do pedido e é audit-before-effect: com o contexto morto
	// ela falha, e o nível NÃO é aplicado — que é o comportamento correcto de AOS-306.
	if w.Code == http.StatusOK {
		t.Fatalf("com o contexto morto a mudanca nao devia aplicar-se: %d %s", w.Code, w.Body.String())
	}
	if _, ok := h.node.Autonomy.registry.Get("agt-1", "fs"); ok {
		t.Fatal("o nivel foi aplicado com a selagem impossivel")
	}
	// E nada foi selado na partição de autonomia — o efeito não aconteceu, logo não há o que registar.
	if head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition); head != 0 {
		t.Errorf("selou uma mudanca que nao aconteceu (head=%d)", head)
	}
}
