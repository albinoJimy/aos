package main

// AOS-275 (achado F7) — provas FALSIFICÁVEIS da ROTA EXTERNA do promotion controller.
//
// O que estava partido: o controller era composto (AOS-206) e a via sancionada era alcançável
// IN-PROCESS, mas o binário não tinha superfície nenhuma por onde um operador submetesse uma
// ratificação — o banner declarava-a "DEFERIDA". As provas abaixo atacam exactamente essa
// distância: TUDO o que aqui se afirma passa por `POST /promote`, nunca por
// [PromotionController.Promote]. Se a rota não existisse (ou não estivesse ligada ao gate de
// PRODUÇÃO), cada teste falharia — 404/501 em vez de 200, ou "ratified" em vez de
// "ratification_replayed".

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hitl "github.com/aos-ref/control-plane/governance/hitl"
	audit "github.com/aos-ref/platform/audit"
)

// aos275PromoteBody constrói o corpo de wire de `POST /promote` a partir dos MESMOS valores de
// domínio que o caminho in-process usaria. Nada é recalculado do lado do cliente: o
// `request_id` é o que o ratificador ASSINOU (o anti-transplante é do gate, não do wire).
func aos275PromoteBody(art hitl.SelfModArtifact, signed hitl.SignedApproval) map[string]any {
	return map[string]any{
		"artifact": map[string]any{
			"id":            art.ID,
			"kind":          string(art.Kind),
			"version":       art.Version,
			"content_hash":  base64.StdEncoding.EncodeToString(art.ContentHash),
			"canary_passed": art.CanaryPassed,
			"eval": map[string]any{
				"suite":           art.EvalResult.Suite,
				"eval_id":         art.EvalResult.EvalID,
				"dataset":         string(art.EvalResult.Dataset),
				"verdict":         string(art.EvalResult.Verdict),
				"score":           art.EvalResult.Score,
				"target_trace_id": art.EvalResult.TargetTraceIDHex(),
			},
		},
		"ratification": map[string]any{
			"request_id": signed.RequestID,
			"ratifier":   signed.Approver,
			"approved":   signed.Approved,
			"nonce":      base64.StdEncoding.EncodeToString(signed.Nonce),
			"issued_at":  signed.IssuedAt.Format(time.RFC3339Nano),
			"signature":  base64.StdEncoding.EncodeToString(signed.Signature),
		},
	}
}

// aos275LastSeal devolve o ÚLTIMO registo selado na cadeia de ratificações do artefacto. É por
// aqui que se lê o motivo estável E o PRINCIPAL — a resposta HTTP é uniforme de propósito, pelo
// que a evidência tem de vir do WORM (é essa a afirmação do ticket).
func aos275LastSeal(t *testing.T, worm audit.Store, artifactID string) (audit.AuditRecord, bool) {
	t.Helper()
	recs, err := worm.Read(context.Background(), "ratification:"+artifactID, 1, 1<<62)
	if err != nil {
		t.Fatalf("WORM.Read: %v", err)
	}
	if len(recs) == 0 {
		return audit.AuditRecord{}, false
	}
	return recs[len(recs)-1], true
}

// aos275SealReason extrai o motivo da decisão de um registo selado.
func aos275SealReason(rec audit.AuditRecord) string {
	for _, ob := range rec.Obligations {
		if ob.Type == "ratification_decision" {
			return ob.Params["reason"]
		}
	}
	return ""
}

// aos275PostPromote faz um `POST /promote` com o estado TLS dado (nil ⇒ sem certificado de
// cliente, o default fora do mTLS de controlo).
func aos275PostPromote(h http.Handler, body any, cs *tls.ConnectionState) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/promote", bytes.NewReader(b))
	req.TLS = cs
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// aos275Node compõe um nó com UM ratificador pinado e relógio de ratificação determinista, e
// devolve o nó, o handler HTTP, o signer (que detém a privada) e o principal. opts são as opções
// de API (ex.: [WithControlMTLS]) — o default é nenhuma, como num nó sem mTLS de controlo.
func aos275Node(t *testing.T, opts ...APIOption) (*Node, http.Handler, ratifierSigner, string) {
	t.Helper()
	const ratifier = "human:ratifier-route"
	signer, pub := newRatifier(t, ratifier)

	cfg := tnBaseConfig()
	cfg.Ratifiers = []RatifierConfig{{Principal: ratifier, PubKey: pub}}
	cfg.RatifyClock = tnRatifyClock() // a via sancionada FORÇA a frescura; relógio fixo torna-a determinista.

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	_, h := newAPI(t, node, opts...)
	return node, h, signer, ratifier
}

// ---------------------------------------------------------------------------
// CA1+CA2 — a rota promove, sela com PRINCIPAL, e o replay é bloqueado PELA ROTA
// ---------------------------------------------------------------------------

// TestAOS275_PromoteRouteRatificaESelaComPrincipal prova as duas primeiras metades do ticket
// numa só cerimónia, sem tocar uma única vez em node.Promotion.Promote:
//
//	(1) `POST /promote` com uma ratificação assinada por um ratificador PINADO ⇒ 200 e a
//	    decisão fica SELADA no WORM com reason=ratified e Principal = o ratificador REAL
//	    (não um claim, não o processo) — é o não-repúdio que o ticket exige;
//	(2) o MESMO corpo re-submetido PELA ROTA ⇒ 403 e a decisão selada é
//	    ratification_replayed.
//
// NÃO-VACUIDADE. O passo (1) só devolve 200 se a pré-condição eval-gate+canary, a autoridade
// "ratify:production", o anti-transplante, a assinatura e a frescura tiverem TODAS sido
// satisfeitas pelo gate de PRODUÇÃO — uma rota que respondesse 200 sem chamar o gate não teria
// selo nenhum para o passo seguinte ler. O passo (2) só é alcançável com o nonce-store DURÁVEL
// composto: se a rota tivesse sido ligada ao [hitl.NewRatificationGate] cru (anti-replay
// opcional e por omissão DESLIGADO), a segunda chamada devolveria 200 e o motivo selado seria
// "ratified" — este teste falharia. Distingue, portanto, a via de produção da via crua PELA
// ROTA, que era exactamente o que faltava provar.
func TestAOS275_PromoteRouteRatificaESelaComPrincipal(t *testing.T) {
	node, h, signer, ratifier := aos275Node(t)

	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())
	body := aos275PromoteBody(art, signed)

	// (1) PROMOÇÃO pela rota.
	rec := aos275PostPromote(h, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /promote com ratificacao valida devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resposta 200 nao descodifica: %v", err)
	}
	if resp["status"] != "promoted" {
		t.Fatalf("status da resposta = %v, quero \"promoted\"", resp["status"])
	}
	if got := resp["ratification_id"]; got != art.RatificationID() {
		t.Fatalf("ratification_id devolvido = %v, quero %q (e a chave do selo no WORM)", got, art.RatificationID())
	}

	seal, ok := aos275LastSeal(t, node.WORM, art.ID)
	if !ok {
		t.Fatal("a promocao PELA ROTA nao selou nada no WORM — a rota nao chegou ao gate de producao")
	}
	if r := aos275SealReason(seal); r != hitl.ReasonRatified {
		t.Fatalf("motivo selado = %q, quero %q", r, hitl.ReasonRatified)
	}
	// O PRINCIPAL é a exigência explícita do CA: a ratificação tem de ficar atribuída ao humano
	// REAL. Um selo com principal vazio (ou com o nó) tornaria o registo inútil para o
	// não-repúdio.
	if seal.Principal.NHIID != ratifier {
		t.Fatalf("principal selado = %q, quero %q — sem principal a ratificacao nao e atribuivel", seal.Principal.NHIID, ratifier)
	}
	if seal.Decision != audit.DecisionAllow {
		t.Fatalf("decisao selada = %v, quero allow", seal.Decision)
	}

	// (2) REPLAY pela MESMA rota, com o MESMO corpo (mesmo nonce).
	recReplay := aos275PostPromote(h, body, nil)
	if recReplay.Code != http.StatusForbidden {
		t.Fatalf("re-submissao da MESMA ratificacao devia dar 403, veio %d (%s)", recReplay.Code, recReplay.Body.String())
	}
	sealReplay, ok := aos275LastSeal(t, node.WORM, art.ID)
	if !ok {
		t.Fatal("a decisao do replay devia ficar selada no WORM")
	}
	if r := aos275SealReason(sealReplay); r != hitl.ReasonRatificationReplayed {
		t.Fatalf("motivo do replay = %q, quero %q — e a prova de que o nonce-store DURAVEL vale PELA ROTA", r, hitl.ReasonRatificationReplayed)
	}
	if sealReplay.Principal.NHIID != ratifier {
		t.Fatalf("o replay tambem e atribuivel (assinatura verificada): principal = %q, quero %q", sealReplay.Principal.NHIID, ratifier)
	}
	if sealReplay.Decision != audit.DecisionDeny {
		t.Fatalf("decisao selada do replay = %v, quero deny", sealReplay.Decision)
	}
}

// ---------------------------------------------------------------------------
// CA1 — a rota é AUTENTICADA (non-signing): a assinatura é que manda
// ---------------------------------------------------------------------------

// TestAOS275_PromoteRouteRecusaAssinaturaForjada prova que a rota não é um bypass da
// autenticação: um corpo idêntico ao legítimo, com UM bit trocado na assinatura, é RECUSADO — e,
// crucialmente, NÃO consome o nonce (a ratificação legítima que se segue promove).
//
// NÃO-VACUIDADE. A segunda metade é o que impede a leitura preguiçosa "a rota recusa tudo": o
// MESMO nonce, agora com a assinatura íntegra, promove. E prova também a ordem correcta do gate
// (a assinatura é verificada ANTES do consumo do nonce) — se a rota consumisse o nonce antes de
// verificar, um atacante sem chave nenhuma poderia QUEIMAR a ratificação de um humano (negação
// de serviço sobre a governação) e a segunda chamada daria 403.
func TestAOS275_PromoteRouteRecusaAssinaturaForjada(t *testing.T) {
	node, h, signer, ratifier := aos275Node(t)

	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())

	forged := signed
	forged.Signature = append([]byte(nil), signed.Signature...)
	forged.Signature[0] ^= 0xFF // um bit: deixa de validar contra a pubkey pinada

	if rec := aos275PostPromote(h, aos275PromoteBody(art, forged), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("assinatura FORJADA devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	// A tentativa forjada NÃO é atribuível: nada entra na cadeia do artefacto (vai para a
	// partição de quarentena). Se aparecesse aqui um selo em nome do ratificador, a cadeia de
	// não-repúdio estaria poluída por quem não detém a chave.
	if seal, ok := aos275LastSeal(t, node.WORM, art.ID); ok {
		t.Fatalf("uma tentativa forjada NAO pode selar na cadeia do artefacto (principal=%q, motivo=%q)", seal.Principal.NHIID, aos275SealReason(seal))
	}

	// O nonce continua FRESCO: a ratificação legítima (mesmo nonce) promove.
	if rec := aos275PostPromote(h, aos275PromoteBody(art, signed), nil); rec.Code != http.StatusOK {
		t.Fatalf("apos a tentativa forjada a ratificacao LEGITIMA devia promover (200), veio %d (%s) — a assinatura tem de ser verificada ANTES do consumo do nonce", rec.Code, rec.Body.String())
	}
}

// TestAOS275_PromoteRouteRecusaRatificadorDesconhecido prova o fail-closed do roster: uma
// ratificação PERFEITAMENTE assinada, mas por um principal que não consta de AOS_RATIFIERS, não
// promove. É a contraparte de "a rota não inventa autoridade": quem autentica é a pubkey pinada,
// não o nome que o corpo declara.
func TestAOS275_PromoteRouteRecusaRatificadorDesconhecido(t *testing.T) {
	node, h, _, _ := aos275Node(t)

	const stranger = "human:stranger"
	strangerSigner, _ := newRatifier(t, stranger) // par gerado agora, NUNCA pinado no nó
	art := tnPromotableArtifact()
	signed := signRatification(t, strangerSigner, stranger, art, tnRatifyClock())

	if rec := aos275PostPromote(h, aos275PromoteBody(art, signed), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("ratificador NAO pinado devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if seal, ok := aos275LastSeal(t, node.WORM, art.ID); ok {
		t.Fatalf("um ratificador desconhecido nao e um principal: nada devia selar na cadeia do artefacto (principal=%q)", seal.Principal.NHIID)
	}
}

// ---------------------------------------------------------------------------
// CA1 — MESMA admissão do /approve (o mTLS de controlo governa também esta rota)
// ---------------------------------------------------------------------------

// TestAOS275_PromoteRouteHerdaMTLSDeControlo prova que a rota entrou no plano de CONTROLO e não
// ao lado dele: com o mTLS de controlo composto, um `POST /promote` SEM certificado de cliente
// verificado é 403 ANTES de chegar ao gate (nada é selado), e o MESMO pedido com certificado
// verificado promove. Se o handler tivesse esquecido [apiHandler.admitControlMTLS] — a maneira
// mais fácil de "inventar um esquema novo" sem dar por isso — a primeira metade daria 200.
func TestAOS275_PromoteRouteHerdaMTLSDeControlo(t *testing.T) {
	node, h, signer, ratifier := aos275Node(t, WithControlMTLS("/mnt/control-ca.pem"))

	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())
	body := aos275PromoteBody(art, signed)

	if rec := aos275PostPromote(h, body, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("mTLS de controlo ligado e sem certificado de cliente devia dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if seal, ok := aos275LastSeal(t, node.WORM, art.ID); ok {
		t.Fatalf("recusado no transporte, o pedido NAO devia ter chegado ao gate (selo com motivo %q)", aos275SealReason(seal))
	}

	if rec := aos275PostPromote(h, body, verifiedClientTLS()); rec.Code != http.StatusOK {
		t.Fatalf("com certificado de cliente verificado + assinatura valida devia dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Fronteira — o wire estruturalmente inválido morre ANTES do gate
// ---------------------------------------------------------------------------

// TestAOS275_PromoteRouteFronteiraFailClosed cobre as recusas de FORMA (400), que existem para
// que um erro de operador não vire uma recusa criptográfica enigmática. Cada caso é 400 e NÃO
// deixa selo na cadeia do artefacto: o gate nunca é chamado.
func TestAOS275_PromoteRouteFronteiraFailClosed(t *testing.T) {
	node, h, signer, ratifier := aos275Node(t)
	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())

	casos := []struct {
		nome     string
		mutar    func(map[string]any)
		porque   string
		artefato string
	}{
		{
			nome:   "kind desconhecido",
			mutar:  func(b map[string]any) { b["artifact"].(map[string]any)["kind"] = "skills" },
			porque: "o Kind entra no canonico: um typo daria SILENCIOSAMENTE outra identidade e o erro apareceria como 'transplante'",
		},
		{
			nome:   "content_hash vazio",
			mutar:  func(b map[string]any) { b["artifact"].(map[string]any)["content_hash"] = "" },
			porque: "sem content_hash a ratificacao nao esta amarrada aos BYTES promovidos",
		},
		{
			nome:   "content_hash nao-base64",
			mutar:  func(b map[string]any) { b["artifact"].(map[string]any)["content_hash"] = "nao-e-base64!!" },
			porque: "um campo binario malformado nao pode virar nil em silencio",
		},
		{
			nome:   "version vazia",
			mutar:  func(b map[string]any) { b["artifact"].(map[string]any)["version"] = "" },
			porque: "a versao SemVer e selada: sem ela o nao-repudio nao diz O QUE foi promovido",
		},
		{
			nome:   "ratifier vazio",
			mutar:  func(b map[string]any) { b["ratification"].(map[string]any)["ratifier"] = "" },
			porque: "sem principal nao ha chave pinada contra a qual verificar",
		},
		{
			nome:   "assinatura nao-base64",
			mutar:  func(b map[string]any) { b["ratification"].(map[string]any)["signature"] = "??" },
			porque: "idem: malformada e 400, nao uma assinatura vazia entregue ao gate",
		},
		{
			nome: "target_trace_id com dimensao errada",
			mutar: func(b map[string]any) {
				b["artifact"].(map[string]any)["eval"].(map[string]any)["target_trace_id"] = "abcd"
			},
			porque: "o trace-alvo tem 16 bytes; truncado mudaria a identidade do eval",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			body := aos275PromoteBody(art, signed)
			c.mutar(body)
			rec := aos275PostPromote(h, body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s devia dar 400 (%s), veio %d (%s)", c.nome, c.porque, rec.Code, rec.Body.String())
			}
		})
	}
	// Nenhum dos casos acima chegou ao gate ⇒ a cadeia do artefacto continua VAZIA e o nonce
	// continua fresco (a ratificação legítima ainda promove).
	if seal, ok := aos275LastSeal(t, node.WORM, art.ID); ok {
		t.Fatalf("um corpo malformado nao pode selar decisao nenhuma (motivo=%q)", aos275SealReason(seal))
	}
	if rec := aos275PostPromote(h, aos275PromoteBody(art, signed), nil); rec.Code != http.StatusOK {
		t.Fatalf("apos as recusas de forma a ratificacao legitima devia promover (200), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAOS275_PromoteRouteCampoDesconhecido prova que o wire é FECHADO (DisallowUnknownFields, a
// disciplina do resto da API): um campo a mais é 400. Um esquema em drift silencioso seria a
// via para um operador julgar que enviou algo que o nó ignorou.
func TestAOS275_PromoteRouteCampoDesconhecido(t *testing.T) {
	_, h, signer, ratifier := aos275Node(t)
	art := tnPromotableArtifact()
	signed := signRatification(t, signer, ratifier, art, tnRatifyClock())

	body := aos275PromoteBody(art, signed)
	body["force"] = true // um campo que a API NÃO conhece — e que soa a bypass

	if rec := aos275PostPromote(h, body, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("campo desconhecido no corpo devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// CA3 — o banner deixa de dizer «deferido»
// ---------------------------------------------------------------------------

// TestAOS275_BannerDeixaDeDizerDeferido liga a afirmação do banner ao estado REAL: como a rota
// existe, nenhuma linha do promotion controller pode continuar a chamar-lhe deferida, e o banner
// tem de NOMEAR a rota (é por ele que o operador descobre a superfície). O teste corre o
// arranque REAL — não a função de banner isolada —, pelo que uma linha escrita e nunca emitida
// não o satisfaz.
func TestAOS275_BannerDeixaDeDizerDeferido(t *testing.T) {
	banner := runWithoutTouchingBoardRegions(t)

	if !strings.Contains(banner, "POST /promote") {
		t.Fatalf("o banner devia NOMEAR a rota de ratificacao (POST /promote):\n%s", banner)
	}
	// A asserção negativa é dirigida SÓ às linhas do promotion controller: o banner tem outros
	// deferimentos legítimos (e nomeados) noutros assuntos.
	for _, linha := range strings.Split(banner, "\n") {
		if !strings.Contains(linha, "promotion controller") {
			continue
		}
		if strings.Contains(strings.ToUpper(linha), "DEFERID") {
			t.Fatalf("a submissao de ratificacoes DEIXOU de ser deferida (ha rota); linha do banner:\n%s", linha)
		}
	}
}
