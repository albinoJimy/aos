package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	integration "github.com/aos-ref/integration"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// ---------------------------------------------------------------------------------------------
// A CLASSE DE RISCO NÃO É ESCOLHA DE QUEM PEDE.
//
// Achado da verificação de completude de 2026-08-23. O `risk_class` é o campo VIZINHO do
// `dual_control_required` — na MESMA struct do wire — e decide `hitl.RequiredAuthority(class)`,
// ou seja QUE CAPABILITY cada aprovador tem de possuir. A comparação é de igualdade exacta:
// `approve:danger` NÃO implica `approve:gray`.
//
// Não é forja — a classe entra na mensagem assinada. É quem pede a escolher o QUADRO em que os
// humanos assinam: dois aprovadores só-`gray` assinam «gray» e, no quadro deles, fazem
// exactamente aquilo para que têm autoridade.
// ---------------------------------------------------------------------------------------------

// noComAprovadoresDe monta um nó com dois aprovadores cuja autoridade é a dada.
func noComAprovadoresDe(t *testing.T, autoridade string) (*Node, http.Handler, []string, [][]byte) {
	t.Helper()
	pubA, privA := tnPubHex(t)
	pubB, privB := tnPubHex(t)
	a := "human:a-" + autoridade
	b := "human:b-" + autoridade
	path := writeApproversFile(t, `{"approvers":[
		{"principal":"`+a+`","pubkey":"`+pubA+`","authority":["`+autoridade+`"]},
		{"principal":"`+b+`","pubkey":"`+pubB+`","authority":["`+autoridade+`"]}
	]}`)
	aprovs, err := parseApproversFile(path)
	if err != nil {
		t.Fatalf("parseApproversFile: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.SteerClock = tnClock()
	cfg.Approvers = aprovs
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	_, h := newAPI(t, node)
	return node, h, []string{a, b}, [][]byte{privA, privB}
}

// corpoComClasse é o corpoDual com a classe de risco escolhida pelo chamador.
func corpoComClasse(t *testing.T, preview []byte, classe risk.Class, quem []string, chaves [][]byte) map[string]any {
	t.Helper()
	req := integration.FourEyesRequest{
		RequestID: "req-classe", Preview: preview,
		RiskClass: classe, DualControlRequired: true,
	}
	pernas := make([]map[string]any, 0, len(quem))
	for i, a := range quem {
		chal := make([]byte, 32)
		if _, err := rand.Read(chal); err != nil {
			t.Fatal(err)
		}
		leg := integration.SignFourEyesLeg(chaves[i], req, a, "s-"+a, "c-"+a, chal, nil)
		pernas = append(pernas, map[string]any{
			"approver": leg.Approver, "session": leg.Session, "credential": leg.Credential,
			"challenge": base64.StdEncoding.EncodeToString(leg.Challenge),
			"signature": base64.StdEncoding.EncodeToString(leg.Signature),
		})
	}
	return map[string]any{
		"request": map[string]any{
			"request_id": req.RequestID,
			"preview":    base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class": uint8(classe), "dual_control_required": true,
		},
		"legs": pernas,
	}
}

func TestAprovadoresGrayNaoDestravamUmEfeitoIRREVERSIVEL(t *testing.T) {
	node, h, quem, chaves := noComAprovadoresDe(t, "approve:gray")
	preview := []byte("apagar o bucket de producao (classe)")
	// O NÓ escalou uma capability IRREVERSÍVEL.
	semearPendente(t, node, "run-classe", capIrreversivelDeTeste, preview)

	// E a cerimónia declara-se «gray», que é a autoridade que estes dois têm.
	rec := postJSON(h, "POST", "/runs/run-classe/approve",
		corpoComClasse(t, preview, risk.ClassGray, quem, chaves))
	if rec.Code == http.StatusOK {
		t.Errorf("dois aprovadores com autoridade `approve:gray` destravaram um efeito "+
			"IRREVERSIVEL — a classe declarada escolheu o escalao de autoridade exigido: %s",
			rec.Body.String())
	}
}

// TestOMesmoPedidoComClasseHONESTAEAceite é a âncora anti-vacuidade.
//
// Sem ela, «recusar sempre» passaria no teste acima e a cerimónia de duas pernas deixaria de
// servir para alguma coisa. Aqui os aprovadores TÊM `approve:danger` e a classe declarada é a
// verdadeira — que é como a cerimónia deve correr.
func TestOMesmoPedidoComClasseHONESTAEAceite(t *testing.T) {
	node, h, quem, chaves := noComAprovadoresDe(t, "approve:danger")
	preview := []byte("apagar o bucket de producao (honesto)")
	semearPendente(t, node, "run-classe-ok", capIrreversivelDeTeste, preview)

	rec := postJSON(h, "POST", "/runs/run-classe-ok/approve",
		corpoComClasse(t, preview, risk.ClassDanger, quem, chaves))
	if rec.Code != http.StatusOK {
		t.Fatalf("a cerimonia HONESTA sobre uma accao irreversivel devia autorizar; veio %d (%s)",
			rec.Code, rec.Body.String())
	}
}

// TestUmaAccaoREVERSIVELContinuaAAceitarClasseDeclarada — o limite inferior é só isso.
//
// Do pendente o nó deriva UMA implicação: irreversível ⇒ danger. Para as outras acções não sabe
// mais do que quem pede, e não finge que sabe. Sem este teste, uma regra que exigisse `danger`
// sempre passaria nos anteriores e travaria toda a aprovação de acções reversíveis.
func TestUmaAccaoREVERSIVELContinuaAAceitarClasseDeclarada(t *testing.T) {
	node, h, quem, chaves := noComAprovadoresDe(t, "approve:gray")
	preview := []byte("ler um ficheiro (classe)")
	semearPendente(t, node, "run-classe-rev", capReversivelDeTeste, preview)

	rec := postJSON(h, "POST", "/runs/run-classe-rev/approve",
		corpoComClasse(t, preview, risk.ClassGray, quem, chaves))
	if rec.Code != http.StatusOK {
		t.Errorf("uma accao REVERSIVEL declarada `gray` devia passar com aprovadores gray; "+
			"veio %d (%s) — o no nao sabe classificar alem do limite inferior e nao pode fingir "+
			"que sabe", rec.Code, rec.Body.String())
	}
}
