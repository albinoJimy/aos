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
// QUEM PEDE NÃO ESCOLHE QUANTOS HUMANOS PRECISA.
//
// Achado 1.10 da varredura adversarial de 2026-08-21: `dual_control_required` era um campo do
// CORPO, passado directo ao gate, que o honra à letra — `false` ⇒ EXACTAMENTE uma perna. Estava
// provada a cerimónia de duas pernas; não a obrigatoriedade dela.
// ---------------------------------------------------------------------------------------------

// noComDoisAprovadores devolve um nó com alice e bob pinados, e as duas chaves privadas.
func noComDoisAprovadores(t *testing.T) (*Node, http.Handler, string, string, []byte, []byte) {
	t.Helper()
	pubA, privA := tnPubHex(t)
	pubB, privB := tnPubHex(t)
	const alice, bob = "human:alice-1.10", "human:bob-1.10"
	path := writeApproversFile(t, `{"approvers":[
		{"principal":"`+alice+`","pubkey":"`+pubA+`","authority":["approve:danger"]},
		{"principal":"`+bob+`","pubkey":"`+pubB+`","authority":["approve:danger"]}
	]}`)
	approvers, err := parseApproversFile(path)
	if err != nil {
		t.Fatalf("parseApproversFile: %v", err)
	}
	cfg := tnBaseConfig()
	cfg.SteerClock = tnClock()
	cfg.Approvers = approvers
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	_, h := newAPI(t, node)
	return node, h, alice, bob, privA, privB
}

// corpoDual monta o corpo de /approve com as pernas dadas e o `dual_control_required` PEDIDO.
func corpoDual(t *testing.T, preview []byte, pedido bool, aprovs []string, privs [][]byte) map[string]any {
	t.Helper()
	req := integration.FourEyesRequest{
		RequestID:           "req-1.10",
		Preview:             preview,
		RiskClass:           risk.ClassDanger,
		DualControlRequired: pedido,
	}
	pernas := make([]map[string]any, 0, len(aprovs))
	for i, a := range aprovs {
		chal := make([]byte, 32)
		if _, err := rand.Read(chal); err != nil {
			t.Fatal(err)
		}
		leg := integration.SignFourEyesLeg(privs[i], req, a, "sess-"+a, "cred-"+a, chal, nil)
		pernas = append(pernas, map[string]any{
			"approver": leg.Approver, "session": leg.Session, "credential": leg.Credential,
			"challenge": base64.StdEncoding.EncodeToString(leg.Challenge),
			"signature": base64.StdEncoding.EncodeToString(leg.Signature),
		})
	}
	return map[string]any{
		"request": map[string]any{
			"request_id":            req.RequestID,
			"preview":               base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class":            uint8(req.RiskClass),
			"dual_control_required": pedido,
		},
		"legs": pernas,
	}
}

func TestOClienteNaoConsegueBaixarAExigencia(t *testing.T) {
	node, h, alice, bob, privA, privB := noComDoisAprovadores(t)
	preview := []byte("apagar o bucket de producao (1.10)")
	// O NÓ escalou uma acção IRREVERSÍVEL.
	semearPendente(t, node, "run-1-10", capIrreversivelDeTeste, preview)

	// E quem pede declara que uma perna basta.
	corpo := corpoDual(t, preview, false, []string{alice}, [][]byte{privA})
	rec := postJSON(h, "POST", "/runs/run-1-10/approve", corpo)
	if rec.Code == http.StatusOK {
		t.Errorf("UMA perna autorizou uma accao que o NO classifica como IRREVERSIVEL — quem pede "+
			"escolheu quantos humanos precisava: %s", rec.Body.String())
	}

	// CONTROLO: com DUAS pernas a mesma acção passa. Sem este ramo, um handler que recusasse
	// SEMPRE passaria no teste acima, e a cerimónia deixaria de servir para alguma coisa.
	// Repetida COM a exigencia declarada: as pernas assinam a afirmacao verdadeira.
	corpo2 := corpoDual(t, preview, true, []string{alice, bob}, [][]byte{privA, privB})
	rec2 := postJSON(h, "POST", "/runs/run-1-10/approve", corpo2)
	if rec2.Code != http.StatusOK {
		t.Errorf("com DUAS pernas a accao irreversivel devia autorizar; veio %d (%s)",
			rec2.Code, rec2.Body.String())
	}
}

// TestUmaAccaoREVERSIVELContinuaAPassarComUmaPerna é a âncora anti-fricção.
//
// Sem ela, «exigir sempre dois» passaria no teste acima — e transformaria toda a aprovação numa
// cerimónia de duas pessoas, que é o custo que o card explicitamente recusa pagar em acções
// reversíveis ("seria fricção injustificada").
func TestUmaAccaoREVERSIVELContinuaAPassarComUmaPerna(t *testing.T) {
	node, h, alice, _, privA, _ := noComDoisAprovadores(t)
	preview := []byte("ler um ficheiro (1.10)")
	semearPendente(t, node, "run-1-10-rev", capReversivelDeTeste, preview)

	rec := postJSON(h, "POST", "/runs/run-1-10-rev/approve",
		corpoDual(t, preview, false, []string{alice}, [][]byte{privA}))
	if rec.Code != http.StatusOK {
		t.Errorf("uma accao REVERSIVEL devia bastar-se com uma perna; veio %d (%s)",
			rec.Code, rec.Body.String())
	}
}

// TestOClienteCONSEGUESubirAExigencia — a monotonia é numa só direcção.
//
// Pedir MAIS rigor do que o nó exige é fricção que quem pede escolhe para si; pedir menos é a
// vulnerabilidade. Sem este teste, um `=` no lugar do `||` passaria despercebido.
func TestOClienteCONSEGUESubirAExigencia(t *testing.T) {
	node, h, alice, bob, privA, privB := noComDoisAprovadores(t)
	preview := []byte("ler um ficheiro, mas com dois (1.10)")
	semearPendente(t, node, "run-1-10-sobe", capReversivelDeTeste, preview)

	// Uma perna, com o corpo a pedir duas: RECUSA.
	rec := postJSON(h, "POST", "/runs/run-1-10-sobe/approve",
		corpoDual(t, preview, true, []string{alice}, [][]byte{privA}))
	if rec.Code == http.StatusOK {
		t.Errorf("o corpo pediu DOIS e uma perna passou — a exigencia so pode SUBIR: %s", rec.Body.String())
	}
	// E com duas, passa.
	rec2 := postJSON(h, "POST", "/runs/run-1-10-sobe/approve",
		corpoDual(t, preview, true, []string{alice, bob}, [][]byte{privA, privB}))
	if rec2.Code != http.StatusOK {
		t.Errorf("com duas pernas devia autorizar; veio %d (%s)", rec2.Code, rec2.Body.String())
	}
}

// TestUmaPreviewQueONoNuncaEscalouERecusada fecha a via que tornaria tudo o resto decorativo.
//
// A preview é um DIGEST opaco (sha256 da call): o nó não consegue classificar o que ela
// representa. Se aceitasse uma preview que não reconhece, quem pede escolheria a preview E o
// número de humanos — e a amarra WYSIWYS compararia o grant com a preview inventada, que casa
// consigo própria.
func TestUmaPreviewQueONoNuncaEscalouERecusada(t *testing.T) {
	_, h, alice, _, privA, _ := noComDoisAprovadores(t)

	rec := postJSON(h, "POST", "/runs/run-1-10-fantasma/approve",
		corpoDual(t, []byte("efeito que ninguem escalou"), false, []string{alice}, [][]byte{privA}))
	if rec.Code != http.StatusForbidden {
		t.Errorf("uma preview que o no NUNCA escalou devia dar 403; veio %d (%s)",
			rec.Code, rec.Body.String())
	}
}
