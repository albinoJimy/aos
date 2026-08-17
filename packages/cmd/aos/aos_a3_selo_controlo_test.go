package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
)

// Achado A3 da auditoria de 2026-08-17. Uma LEITURA de run ficava na hash-chain tamper-evidente
// (`gov.read`); uma INTERVENÇÃO NA EXECUÇÃO — `pause`, `steer` — ficava só no Event Store. A
// acção mais consequente tinha o registo mais fraco.
//
// O evento do Event Store carrega o emitter e a assinatura, pelo que ADULTERÁ-LO já era
// detectável. O que faltava era a REMOÇÃO: o Event Store não é encadeado, e apagar o evento não
// parte cadeia nenhuma.
//
// Os dois testes exercitam as duas faces: a intervenção que SURTE efeito passa a deixar rasto
// nomeando quem interveio; a que é RECUSADA continua a não deixar — senão o selo virava um vector
// para inchar o trilho sem autoridade nenhuma.

// newA3Node compõe um nó com WORM em disco (o substrato tamper-evidente que o selo exige) e um
// operador registado no canal de controlo.
func newA3Node(t *testing.T) (*Node, *NodeService, http.Handler, ed25519.PrivateKey, string) {
	t.Helper()
	const operatorID = "human:operator-a3"
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.Operators = map[string]ed25519.PublicKey{operatorID: pub}
	cfg.SteerClock = tnClock()
	cfg.Model = &a3Model{}
	cfg.WORMPath = filepath.Join(t.TempDir(), "worm.wal")
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return node, svc, h, priv, operatorID
}

type a3Model struct{}

func (*a3Model) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{Text: "ok", Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

// selosDeControlo devolve os registos da partição do plano de controlo.
func selosDeControlo(t *testing.T, node *Node) []audit.AuditRecord {
	t.Helper()
	if node.WORM == nil {
		t.Fatal("no sem WORM composto: o teste nao provaria nada")
	}
	ctx := context.Background()
	head, err := node.WORM.Head(ctx, controlSealPartition)
	if err != nil {
		t.Fatalf("Head(%s): %v", controlSealPartition, err)
	}
	if head == 0 {
		return nil
	}
	recs, err := node.WORM.Read(ctx, controlSealPartition, 1, head)
	if err != nil {
		t.Fatalf("Read(%s): %v", controlSealPartition, err)
	}
	return recs
}

// a3Emitter assina um sinal para o run/kind dados, no MESMO tuplo que o autenticador verifica.
func a3Emitter(t *testing.T, priv ed25519.PrivateKey, id, runID string, kind control.SignalKind, payload []byte) emitterWire {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("rand nonce: %v", err)
	}
	em := integration.SignSignal(priv, id, runID, kind, payload, nonce, tnClock()())
	return emitterWire{
		ID:        em.ID,
		Signature: base64.StdEncoding.EncodeToString(em.Signature),
		Nonce:     base64.StdEncoding.EncodeToString(em.Nonce),
		IssuedAt:  em.IssuedAt,
	}
}

// TestA3_PauseAplicado_FicaNaHashChain: o coração da correcção.
func TestA3_PauseAplicado_FicaNaHashChain(t *testing.T) {
	node, svc, h, priv, opID := newA3Node(t)
	const runID = "run-a3-pause"
	if err := svc.Submit(context.Background(), steerGoal(runID)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// ÂNCORA DE NÃO-VACUIDADE: a partição começa vazia. Sem isto, um registo encontrado depois
	// podia ser de outra coisa qualquer.
	if antes := selosDeControlo(t, node); len(antes) != 0 {
		t.Fatalf("a particao de controlo devia comecar vazia, tinha %d", len(antes))
	}

	rec := postReq(h, "/runs/"+runID+"/pause",
		pauseRequest{Emitter: a3Emitter(t, priv, opID, runID, control.SignalPause, nil)}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("pause autenticado devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}

	selos := selosDeControlo(t, node)
	if len(selos) != 1 {
		t.Fatalf("um pause APLICADO tem de deixar exactamente um selo, ficaram %d", len(selos))
	}
	s := selos[0]
	if s.Capability != "control:pause" {
		t.Fatalf("a capability selada tem de nomear o tipo de sinal, veio %q", s.Capability)
	}
	if s.RunID != runID {
		t.Fatalf("o selo tem de amarrar o run, veio %q", s.RunID)
	}
	// A peça que faltava ao trilho: QUEM interveio.
	if s.Principal.NHIID != opID {
		t.Fatalf("o selo TEM de nomear o emissor (%q) — um registo que diz que houve intervencao sem dizer de quem nao serve o nao-repudio; veio %q",
			opID, s.Principal.NHIID)
	}
}

// TestA3_SinalRecusado_NaoSela: a face que impede o selo de virar vector. Um sinal que NÃO surte
// efeito não muda estado nenhum e não entra na cadeia — senão quem inundasse o canal inchava o
// trilho tamper-evidente sem autoridade nenhuma.
func TestA3_SinalRecusado_NaoSela(t *testing.T) {
	node, svc, h, priv, opID := newA3Node(t)
	const runID = "run-a3-recusado"
	if err := svc.Submit(context.Background(), steerGoal(runID)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Assinatura VÁLIDA mas para OUTRO run: o alvo está preso ao tuplo assinado, logo é recusada.
	rec := postReq(h, "/runs/"+runID+"/pause",
		pauseRequest{Emitter: a3Emitter(t, priv, opID, "outro-run-qualquer", control.SignalPause, nil)}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sinal com alvo errado devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if selos := selosDeControlo(t, node); len(selos) != 0 {
		t.Fatalf("um sinal RECUSADO nao pode selar (seria vector de inchaco do trilho), ficaram %d", len(selos))
	}
}

// TestA3_SteerAplicado_SelaSemACorreccao: o steer também sela — e o selo NÃO transporta a
// correcção. A correcção é conteúdo submetido por um humano; o trilho é sem PII. O que entra é
// quem interveio, sobre que run e com que tipo de sinal.
func TestA3_SteerAplicado_SelaSemACorreccao(t *testing.T) {
	node, svc, h, priv, opID := newA3Node(t)
	const runID = "run-a3-steer"
	const segredo = "CORRECCAO-QUE-NAO-PODE-APARECER-NO-TRILHO"
	if err := svc.Submit(context.Background(), steerGoal(runID)); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	em := a3Emitter(t, priv, opID, runID, control.SignalSteer, []byte(segredo))
	rec := postReq(h, "/runs/"+runID+"/steer", steerRequest{
		Emitter: em,
		Payload: base64.StdEncoding.EncodeToString([]byte(segredo)),
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("steer autenticado devia dar 202, veio %d (%s)", rec.Code, rec.Body.String())
	}

	selos := selosDeControlo(t, node)
	if len(selos) != 1 {
		t.Fatalf("um steer APLICADO tem de deixar exactamente um selo, ficaram %d", len(selos))
	}
	if selos[0].Capability != "control:steer" {
		t.Fatalf("capability selada errada: %q", selos[0].Capability)
	}
	if selos[0].Principal.NHIID != opID {
		t.Fatalf("o selo tem de nomear o emissor, veio %q", selos[0].Principal.NHIID)
	}
	// O trilho é SEM PII: a correcção submetida não pode lá estar.
	for _, o := range selos[0].Obligations {
		for _, f := range o.Fields {
			if f == segredo {
				t.Fatal("a correccao do steer NAO pode entrar no selo — o trilho e sem conteudo")
			}
		}
	}
	if selos[0].Reason == segredo {
		t.Fatal("a correccao do steer NAO pode entrar no campo Reason")
	}
}
