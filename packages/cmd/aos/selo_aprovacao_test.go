package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	identity "github.com/aos-ref/platform/identity"
)

// ---------------------------------------------------------------------------------------------
// «QUEM AUTORIZOU» TEM DE ESTAR NA CADEIA — E NÃO ESTAVA PROVADO.
//
// O README declarava esta lacuna como ABERTA: a cadeia sela que UM gate humano foi satisfeito
// (human_gate satisfied) mas não QUEM o satisfez. O código já a fechava — as obrigações
// [controlApproversObl] e [controlGrantObl] existem e o handler preenche-as.
//
// Os TRÊS estados discordavam: o código faz, nada prova, e o documento diz que não faz. Nenhum
// deles é inofensivo. Uma lacuna INVENTADA custa o mesmo que uma escondida — trava a mesma acção
// —, e uma correcção sem prova é uma que a próxima refactorização apaga sem ninguém notar.
//
// Para uma autorização cujo propósito É o não-repúdio de quem autorizou, esta é a propriedade
// central: não um detalhe do registo, mas a coisa que a cerimónia existe para produzir.
// ---------------------------------------------------------------------------------------------

// noComAprovador compõe um nó com UM aprovador (acção reversível: 1 perna basta) e devolve
// também a chave privada, para assinar a perna.
func noComAprovador(t *testing.T, principal string) (*Node, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := tnBaseConfig()
	cfg.IssuerClasses = map[string]identity.ClassPolicy{
		tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
	}
	cfg.Approvers = []ApproverConfig{{
		Principal: principal,
		PubKey:    pub,
		Authority: []string{"approve:" + risk.ClassSafe.String()},
	}}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	if node.FourEyes == nil {
		t.Fatal("o gate four-eyes devia ter sido composto — o cenario nao esta montado")
	}
	return node, priv
}

// corpoDeAprovacao monta o pedido HTTP de uma cerimónia de uma perna.
func corpoDeAprovacao(priv ed25519.PrivateKey, principal, requestID string) map[string]any {
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	req := integration.FourEyesRequest{
		RequestID:           requestID,
		Preview:             []byte("efeito exibido ao humano"),
		RiskClass:           risk.ClassSafe,
		DualControlRequired: false,
	}
	leg := integration.SignFourEyesLeg(priv, req, principal, "sess-1", "cred-1", challenge, nil)
	return map[string]any{
		"request": map[string]any{
			"request_id":            req.RequestID,
			"preview":               base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class":            uint8(req.RiskClass),
			"dual_control_required": req.DualControlRequired,
		},
		"legs": []map[string]any{{
			"approver":   leg.Approver,
			"session":    leg.Session,
			"credential": leg.Credential,
			"challenge":  base64.StdEncoding.EncodeToString(leg.Challenge),
			"signature":  base64.StdEncoding.EncodeToString(leg.Signature),
		}},
	}
}

// NOTA: o auxiliar `selosDeControlo` e a propriedade «um sinal RECUSADO nao sela» ja vivem em
// `aos_a3_selo_controlo_test.go`, para os caminhos `pause` e `steer`. O que faltava — e o que
// este ficheiro acrescenta — é a CERIMÓNIA DE APROVAÇÃO: um ramo diferente do handler, e o único
// onde o selo tem de nomear IDENTIDADES em vez de um emissor só.

func TestAprovacaoSelaQUEMAutorizou(t *testing.T) {
	const aprovador = "human:approver-1"
	node, priv := noComAprovador(t, aprovador)
	_, h := newAPI(t, node)

	// CONTROLO ANTES: a partição está vazia. Sem isto, um selo deixado por outro caminho passaria
	// por prova desta cerimónia.
	if n := len(selosDeControlo(t, node)); n != 0 {
		t.Fatalf("a particao de controlo ja tinha %d selo(s) — o cenario nao esta limpo", n)
	}

	rec := postJSON(h, "POST", "/runs/run-x/approve", corpoDeAprovacao(priv, aprovador, "req-selo-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("/approve devolveu %d: %s", rec.Code, rec.Body.String())
	}

	selos := selosDeControlo(t, node)
	if len(selos) != 1 {
		t.Fatalf("esperava 1 selo de controlo, veio %d", len(selos))
	}
	s := selos[0]

	// A propriedade que interessa: o selo NOMEIA quem autorizou.
	var aprovadores []string
	for _, ob := range s.Obligations {
		if ob.Type == controlApproversObl {
			aprovadores = ob.Fields
		}
	}
	if len(aprovadores) == 0 {
		t.Fatalf("o selo NAO nomeia nenhum aprovador — a cerimonia sela que houve aval e nao de "+
			"quem, e o nao-repudio (que e o proposito dela) nao existe. Obrigacoes: %+v", s.Obligations)
	}
	var achou bool
	for _, a := range aprovadores {
		if a == aprovador {
			achou = true
		}
	}
	if !achou {
		t.Errorf("o selo nomeia %v e o aprovador foi %q", aprovadores, aprovador)
	}
}

// TestUmaAprovacaoRECUSADANaoDeixaSelo é o controlo que impede a cadeia de ser inchada por quem
// inunda o canal — e que impede o teste acima de passar por acidente.
//
// Sem ele, um selo emitido em TODA a chamada faria aquele teste passar mesmo que a autorização
// tivesse sido negada: a cadeia afirmaria um aval que não houve.
func TestUmaAprovacaoRECUSADANaoDeixaSelo(t *testing.T) {
	const aprovador = "human:approver-1"
	node, _ := noComAprovador(t, aprovador)
	_, h := newAPI(t, node)

	// Assinada por OUTRA chave: a perna não verifica.
	_, outra, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rec := postJSON(h, "POST", "/runs/run-x/approve", corpoDeAprovacao(outra, aprovador, "req-recusado"))
	if rec.Code == http.StatusOK {
		t.Fatalf("uma perna assinada pela chave errada foi ACEITE (%d)", rec.Code)
	}
	if n := len(selosDeControlo(t, node)); n != 0 {
		t.Errorf("uma aprovacao RECUSADA deixou %d selo(s) — quem inunda o canal incha o trilho, "+
			"e a cadeia passa a afirmar avales que nao houve", n)
	}
}

// TestOSeloNaoTransportaMaterialSecreto — o selo nomeia identidades, e só isso.
//
// A assinatura e o challenge da perna NÃO podem entrar na cadeia: são material de autenticação, e
// a hash-chain é lida por quem tem autoridade de AUDITORIA, que não é a mesma coisa que
// autoridade para reapresentar uma prova.
func TestOSeloNaoTransportaMaterialSecreto(t *testing.T) {
	const aprovador = "human:approver-1"
	node, priv := noComAprovador(t, aprovador)
	_, h := newAPI(t, node)

	corpo := corpoDeAprovacao(priv, aprovador, "req-selo-2")
	if rec := postJSON(h, "POST", "/runs/run-x/approve", corpo); rec.Code != http.StatusOK {
		t.Fatalf("/approve devolveu %d", rec.Code)
	}
	legs := corpo["legs"].([]map[string]any)
	assinatura := legs[0]["signature"].(string)
	challenge := legs[0]["challenge"].(string)

	selos := selosDeControlo(t, node)
	if len(selos) != 1 {
		t.Fatalf("esperava 1 selo, veio %d", len(selos))
	}
	var tudo strings.Builder
	for _, ob := range selos[0].Obligations {
		tudo.WriteString(ob.Type)
		tudo.WriteString(strings.Join(ob.Fields, " "))
	}
	tudo.WriteString(selos[0].Resource.Value)
	tudo.WriteString(selos[0].Principal.NHIID)
	texto := tudo.String()
	if strings.Contains(texto, assinatura) {
		t.Error("o selo transporta a ASSINATURA da perna — material de autenticacao na cadeia")
	}
	if strings.Contains(texto, challenge) {
		t.Error("o selo transporta o CHALLENGE da perna — material de uso-unico na cadeia")
	}
}
