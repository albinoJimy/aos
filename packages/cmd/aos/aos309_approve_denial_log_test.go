package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-309 — toda a negação do `/approve` fica no log do operador, com a razão dedicada. A
// resposta HTTP continua UNIFORME (403 «aprovacao recusada»).
//
// A auditoria não conseguiu, em seis tentativas, distinguir «challenge inválido» de «cerimónia
// malformada»: todas devolveram o mesmo 403 e nenhuma via tinha a razão.
//
// ESTE TESTE FOI REESCRITO depois de uma revisão adversarial mostrar que a primeira versão era
// TAUTOLÓGICA: comparava as LINHAS DE LOG INTEIRAS, que já contêm `request_id="req-A|B|C"` e
// portanto diferem sempre — passaria com todas as razões vazias. E cobria três casos na via
// `authorizeSingle`, onde o gate devolve a MESMA `Reason` («aprovação não verificada») para as
// catorze causas de `verifyLeg`, deixando por exercer as quatro classes que o CA nomeia — que
// vivem todas em `authorizeDual`.
//
// Agora: extrai-se o segmento `razao=…erro=…` (sem o request_id) e exige-se que ele DISTINGA as
// quatro classes de duplo controlo.

// razaoDoLog isola o par (razão, erro) de uma linha AOS-309, deixando de fora o request_id e o
// run — que são os campos que tornavam a comparação anterior vácua.
var razaoDoLog = regexp.MustCompile(`razao=(".*?")\s+erro=(.*)$`)

func segmentoDeRazao(t *testing.T, saida, requestID string) string {
	t.Helper()
	for _, l := range strings.Split(saida, "\n") {
		if !strings.Contains(l, "AOS-309") || !strings.Contains(l, `request_id="`+requestID+`"`) {
			continue
		}
		m := razaoDoLog.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("linha AOS-309 de %s sem segmento razao=/erro=: %s", requestID, l)
		}
		return m[1] + "|" + m[2]
	}
	t.Fatalf("nao ha linha AOS-309 para %s em:\n%s", requestID, saida)
	return ""
}

// pernaSpec descreve UMA perna da cerimónia. A sessão e a credencial entram na ASSINATURA
// (`fourEyesMessage` cobre-as), pelo que uma colisão estrutural tem de ser assinada como tal —
// mutar a perna depois de assinada só produz «assinatura inválida» e nunca chega ao invariante
// que se quer exercitar. Foi este o erro da primeira versão deste teste.
type pernaSpec struct {
	aprovador  string
	priv       []byte
	sessao     string
	credencial string
}

// cerimoniaDual monta um corpo de duplo controlo a partir das pernas dadas, cada uma assinada
// com os seus próprios valores.
func cerimoniaDual(t *testing.T, requestID string, preview []byte, pernasSpec []pernaSpec) map[string]any {
	t.Helper()
	req := integration.FourEyesRequest{
		RequestID:           requestID,
		Preview:             preview,
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true,
	}
	pernas := make([]map[string]any, 0, len(pernasSpec))
	for _, p := range pernasSpec {
		chal := make([]byte, 32)
		if _, err := rand.Read(chal); err != nil {
			t.Fatal(err)
		}
		leg := integration.SignFourEyesLeg(p.priv, req, p.aprovador, p.sessao, p.credencial, chal, nil)
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
			"dual_control_required": true,
		},
		"legs": pernas,
	}
}

// TestAOS309_AsQuatroClassesDeDuploControloDistinguemSeNoLog — as classes que o CA nomeia:
// contagem de pernas, auto-aprovação, mesma sessão, mesma credencial. Todas em `authorizeDual`.
func TestAOS309_AsQuatroClassesDeDuploControloDistinguemSeNoLog(t *testing.T) {
	node, _, alice, bob, privA, privB := noComDoisAprovadores(t)

	var log bytes.Buffer
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithServiceLog(&log))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatal(err)
	}

	preview := []byte("apagar o bucket de producao (AOS-309)")
	semearPendente(t, node, "run-309", capIrreversivelDeTeste, preview)

	casos := []struct {
		nome      string
		requestID string
		corpo     map[string]any
		esperaNo  string // fragmento que a razão TEM de nomear
	}{
		{
			nome: "contagem de pernas", requestID: "req-309-pernas",
			corpo: cerimoniaDual(t, "req-309-pernas", preview, []pernaSpec{
				{alice, privA, "sess-a", "cred-a"},
			}),
			esperaNo: "aprova",
		},
		{
			nome: "auto-aprovacao (mesmo principal)", requestID: "req-309-self",
			corpo: cerimoniaDual(t, "req-309-self", preview, []pernaSpec{
				{alice, privA, "sess-a", "cred-a"},
				{alice, privA, "sess-b", "cred-b"}, // mesmo principal, tudo o resto distinto
			}),
			esperaNo: "principal",
		},
		{
			nome: "mesma sessao", requestID: "req-309-sessao",
			corpo: cerimoniaDual(t, "req-309-sessao", preview, []pernaSpec{
				{alice, privA, "sess-partilhada", "cred-a"},
				{bob, privB, "sess-partilhada", "cred-b"}, // sessão em colisão, ASSINADA assim
			}),
			esperaNo: "sess",
		},
		{
			nome: "mesma credencial", requestID: "req-309-cred",
			corpo: cerimoniaDual(t, "req-309-cred", preview, []pernaSpec{
				{alice, privA, "sess-a", "cred-partilhada"},
				{bob, privB, "sess-b", "cred-partilhada"},
			}),
			esperaNo: "credencial",
		},
	}

	segmentos := map[string]string{}
	for _, k := range casos {
		rec := postJSON(h, "POST", "/runs/run-309/approve", k.corpo)
		// A resposta é UNIFORME — a mensagem HTTP não pode ser um oráculo.
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "aprovacao recusada") {
			t.Fatalf("%s: resposta %d %q, quero 403 uniforme", k.nome, rec.Code, rec.Body.String())
		}
		seg := segmentoDeRazao(t, log.String(), k.requestID)
		if !strings.Contains(strings.ToLower(seg), strings.ToLower(k.esperaNo)) {
			t.Errorf("%s: a razao registada nao nomeia %q: %s", k.nome, k.esperaNo, seg)
		}
		segmentos[k.nome] = seg
	}

	// A asserção que a versão anterior não fazia: os segmentos (SEM request_id) têm de ser
	// distintos entre si. Com razões vazias, este ciclo falha.
	for a, sa := range segmentos {
		for b, sb := range segmentos {
			if a < b && sa == sb {
				t.Errorf("as classes %q e %q registam o MESMO segmento — o log nao as distingue: %s", a, b, sa)
			}
		}
	}
}

// TestAOS309_ValoresDeSessaoECredencialNaoEntramNoLog — o log diz a CLASSE, não o valor.
//
// `ErrSameSession` e `ErrSameCredential` interpolam o identificador da sessão viva e o
// credential-id do aprovador. Escrevê-los quebraria a disciplina que o resto deste eixo declara
// e cumpre («nunca a assinatura nem o nonce»).
func TestAOS309_ValoresDeSessaoECredencialNaoEntramNoLog(t *testing.T) {
	node, _, alice, bob, privA, privB := noComDoisAprovadores(t)

	var log bytes.Buffer
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithServiceLog(&log))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatal(err)
	}
	preview := []byte("apagar o bucket de producao (AOS-309 redaccao)")
	semearPendente(t, node, "run-309r", capIrreversivelDeTeste, preview)

	corpo := cerimoniaDual(t, "req-309-redigido", preview, []pernaSpec{
		{alice, privA, "sess-SEGREDO-VIVO", "cred-a"},
		{bob, privB, "sess-SEGREDO-VIVO", "cred-b"},
	})
	if rec := postJSON(h, "POST", "/runs/run-309r/approve", corpo); rec.Code != http.StatusForbidden {
		t.Fatalf("colisao de sessao devia dar 403, veio %d", rec.Code)
	}
	saida := log.String()
	if strings.Contains(saida, "sess-SEGREDO-VIVO") {
		t.Errorf("o identificador de sessao entrou no log:\n%s", saida)
	}
	if !strings.Contains(saida, "redigido") {
		t.Errorf("a redaccao nao esta declarada na linha — quem lê tem de saber que o valor foi omitido:\n%s", saida)
	}

}

// TestAOS309_ARecusaFicaNaHashChain — o residual do ticket, fechado.
//
// A AC admitia «no mínimo, um log correlável», e era isso que existia: o log do serviço, que é
// volátil. A prova durável de que dois humanos tentaram autorizar uma acção irreversível e foram
// recusados não existia em lado nenhum.
//
// Isto NÃO contradiz `TestA3_SinalRecusado_NaoSela`: aquele protege o trilho de um sinal com
// alvo errado, que qualquer um pode inundar. Uma cerimónia só chega ao gate depois de o nó
// reconhecer a preview que ele PRÓPRIO escalou — o volume é limitado pelas suas pendências.
func TestAOS309_ARecusaFicaNaHashChain(t *testing.T) {
	node, _, alice, bob, privA, privB := noComDoisAprovadores(t)

	var log bytes.Buffer
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithServiceLog(&log))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewAPIHandler(svc, node)
	if err != nil {
		t.Fatal(err)
	}
	preview := []byte("apagar o bucket de producao (AOS-309 selo)")
	semearPendente(t, node, "run-309s", capIrreversivelDeTeste, preview)

	// Auto-aprovação: mesmo principal nas duas pernas.
	corpo := cerimoniaDual(t, "req-309-selo", preview, []pernaSpec{
		{alice, privA, "sess-a", "cred-a"},
		{alice, privA, "sess-b", "cred-b"},
	})
	if rec := postJSON(h, "POST", "/runs/run-309s/approve", corpo); rec.Code != http.StatusForbidden {
		t.Fatalf("auto-aprovacao devia dar 403, veio %d", rec.Code)
	}

	head, err := node.WORM.Head(context.Background(), controlSealPartition)
	if err != nil || head == 0 {
		t.Fatalf("a recusa devia estar selada (head=%d, err=%v)", head, err)
	}
	recs, err := node.WORM.Read(context.Background(), controlSealPartition, head, head)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ler o selo: %v (%d)", err, len(recs))
	}
	selo := recs[0]
	if selo.Decision != audit.DecisionDeny {
		t.Errorf("o selo de uma recusa tem de ser deny, veio %v", selo.Decision)
	}
	if len(selo.Obligations) == 0 || selo.Obligations[0].Type != controlDenialObl {
		t.Fatalf("o selo nao transporta a obrigacao de recusa: %+v", selo.Obligations)
	}
	if selo.Obligations[0].Params["request_id"] != "req-309-selo" {
		t.Errorf("o selo tem de correlacionar pelo request_id: %+v", selo.Obligations[0].Params)
	}
	classe := selo.Obligations[0].Fields[0]
	if !strings.Contains(strings.ToLower(classe), "principal") {
		t.Errorf("o selo tem de nomear a CLASSE da recusa: %q", classe)
	}
	// E nunca o VALOR: a mesma disciplina do log.
	if strings.Contains(classe, "sess-a") || strings.Contains(classe, "cred-a") {
		t.Errorf("o selo transporta um identificador em vez da classe: %q", classe)
	}

	// CONTROLO — uma cerimónia BEM SUCEDIDA continua a selar como allow, e não como deny.
	// Run PRÓPRIO: `semearPendente` regista uma pendência por run, e reutilizar o run da recusa
	// deixaria a segunda preview sem escalada correspondente.
	preview2 := []byte("apagar o bucket de producao (AOS-309 sucesso)")
	semearPendente(t, node, "run-309ok", capIrreversivelDeTeste, preview2)
	ok := cerimoniaDual(t, "req-309-ok", preview2, []pernaSpec{
		{alice, privA, "sess-a", "cred-a"},
		{bob, privB, "sess-b", "cred-b"},
	})
	if rec := postJSON(h, "POST", "/runs/run-309ok/approve", ok); rec.Code != http.StatusOK {
		t.Fatalf("a cerimonia valida devia autorizar: %d %s", rec.Code, rec.Body.String())
	}
	h2, _ := node.WORM.Head(context.Background(), controlSealPartition)
	r2, _ := node.WORM.Read(context.Background(), controlSealPartition, h2, h2)
	if len(r2) != 1 || r2[0].Decision != audit.DecisionAllow {
		t.Errorf("o sucesso tem de continuar a selar como allow: %+v", r2)
	}
}
