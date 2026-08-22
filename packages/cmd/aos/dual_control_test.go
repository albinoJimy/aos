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

// noComTresAprovadores pina alice, bob e carol no MESMO nó — sem os três pinados, a segunda
// cerimónia falharia por «aprovador desconhecido» e o teste mediria outra coisa.
func noComTresAprovadores(t *testing.T) (*Node, http.Handler, []string, [][]byte) {
	t.Helper()
	pubA, privA := tnPubHex(t)
	pubB, privB := tnPubHex(t)
	pubC, privC := tnPubHex(t)
	const alice, bob, carol = "human:alice-1.11", "human:bob-1.11", "human:carol-1.11"
	path := writeApproversFile(t, `{"approvers":[
		{"principal":"`+alice+`","pubkey":"`+pubA+`","authority":["approve:danger"]},
		{"principal":"`+bob+`","pubkey":"`+pubB+`","authority":["approve:danger"]},
		{"principal":"`+carol+`","pubkey":"`+pubC+`","authority":["approve:danger"]}
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
	return node, h, []string{alice, bob, carol}, [][]byte{privA, privB, privC}
}

// TestASegundaCerimoniaComOUTROParERecusadaPELAROTA prova a PROPRIEDADE do achado 1.11 na rota —
// e o nome desta nota é o que ela prova, depois de uma correcção.
//
// ESCREVI-A PRIMEIRO COMO TESTE DE CABLAGEM da guarda do `Put`, e a mutação denunciou-a: remover
// a guarda NÃO a fazia cair. A razão é uma interacção que eu não tinha visto — a correcção do
// achado 1.10, feita horas antes, já FECHA esta via: emitido o grant da primeira cerimónia, o
// pendente sai da lista de «à espera de decisão», e a segunda cerimónia é recusada com «preview
// não corresponde a nenhuma escalada pendente» ANTES de chegar ao broker.
//
// Fica dito porque a conclusão importa: ao nível da ROTA, o 1.11 já era inalcançável. A guarda do
// `Put` é defesa em profundidade para quem chegue ao store por outra via, e é lá — no pacote
// `integration` — que está coberta por mutação.
//
// O que esta função continua a provar, e é a propriedade que interessa ao operador: a segunda
// cerimónia NÃO recebe 200, e a cadeia continua a nomear o par que realmente assinou.
func TestASegundaCerimoniaComOUTROParERecusadaPELAROTA(t *testing.T) {
	node, h, quem, chaves := noComTresAprovadores(t)
	alice, bob, carol := quem[0], quem[1], quem[2]
	_ = bob
	privA, privB, privC := chaves[0], chaves[1], chaves[2]
	preview := []byte("apagar o bucket de producao (1.11)")
	semearPendente(t, node, "run-1-11", capIrreversivelDeTeste, preview)

	// CERIMÓNIA 1 — alice e bob.
	rec := postJSON(h, "POST", "/runs/run-1-11/approve",
		corpoDual(t, preview, true, []string{alice, bob}, [][]byte{privA, privB}))
	if rec.Code != http.StatusOK {
		t.Fatalf("a primeira cerimonia devia autorizar; veio %d (%s)", rec.Code, rec.Body.String())
	}

	// CERIMÓNIA 2 — MESMO request_id (o `corpoDual` usa sempre "req-1.10"), par DIFERENTE.
	rec2 := postJSON(h, "POST", "/runs/run-1-11/approve",
		corpoDual(t, preview, true, []string{alice, carol}, [][]byte{privA, privC}))
	if rec2.Code == http.StatusOK {
		t.Errorf("a SEGUNDA cerimonia, com outro par, recebeu 200 — e a prova registada continua a "+
			"nomear o primeiro par. Dois humanos autorizaram e a cadeia nomeia quem nao assinou: %s",
			rec2.Body.String())
	}
}
