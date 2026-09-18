package main

// AOS-408 — a cerimónia de decisão, pelo PROCESSO REAL.
//
// O que se prova aqui é a governação da decisão, não a sua mecânica: quem pode decidir (chave
// PINADA com autoridade para a classe), sobre QUE organigrama (o `request_id` amarra plano+hash),
// UMA só vez (nonce consumido por CAS durável), dentro do prazo (imposto na decisão, não por um
// varredor), e que o documento reapresentado é o mesmo que foi selado (confronto por hash).
//
// Cada teste tem a sua contraprova: a aprovação legítima materializa. Sem isso, um `decide` que
// recusasse tudo passaria os cinco testes de recusa.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/hitl"
)

// aos408Aprovador gera um par de chaves e o ficheiro de aprovadores PINADOS, no mesmo formato que
// o nó usa em AOS_APPROVERS_FILE.
func aos408Aprovador(t *testing.T, dir, principal string, autoridade ...string) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(autoridade) == 0 {
		autoridade = []string{"approve:danger"}
	}
	corpo := map[string]any{"approvers": []map[string]any{{
		"principal": principal,
		"pubkey":    hex.EncodeToString(pub),
		"authority": autoridade,
	}}}
	raw, err := json.Marshal(corpo)
	if err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(dir, "approvers.json")
	escrever(t, caminho, string(raw))
	return priv, caminho
}

// assinadorDeTeste assina com uma chave local — o análogo em teste do `aos-issuer
// plan-approve-sign`, que é a via do operador.
type assinadorDeTeste struct{ priv ed25519.PrivateKey }

func (a assinadorDeTeste) Sign(_ context.Context, _ string, msg []byte) ([]byte, error) {
	return ed25519.Sign(a.priv, msg), nil
}

// aos408Assinar produz o ficheiro JSON da decisão assinada. Usa a MESMA função que o verificador
// usa para reconstruir os bytes canónicos — assinar por outro caminho provaria outra coisa.
func aos408Assinar(t *testing.T, dir, nome, requestID, approver string, priv ed25519.PrivateKey, aprovada bool) string {
	t.Helper()
	// Nonce DISTINTO por ficheiro de aprovação (determinista, para o teste ser reprodutível): o
	// nonce é uso-único, e reutilizá-lo entre duas aprovações do mesmo pedido faria a segunda ser
	// recusada como replay — um artefacto do teste, não do sistema.
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	for i := 0; i < len(nome) && i < len(nonce); i++ {
		nonce[i] ^= nome[i]
	}
	assinada, err := hitl.SignApproval(context.Background(), assinadorDeTeste{priv: priv}, hitl.SignedApproval{
		RequestID: requestID,
		Approver:  approver,
		Approved:  aprovada,
		Nonce:     nonce,
		IssuedAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("SignApproval: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"request_id": assinada.RequestID,
		"approver":   assinada.Approver,
		"approved":   assinada.Approved,
		"nonce":      hex.EncodeToString(assinada.Nonce),
		"issued_at":  assinada.IssuedAt.Format(time.RFC3339Nano),
		"signature":  hex.EncodeToString(assinada.Signature),
	})
	if err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(dir, nome)
	escrever(t, caminho, string(raw))
	return caminho
}

var reRequestID = regexp.MustCompile(`request_id=(plan:[^\s]+)`)

// aos408Pendente corre o `serve --goal` com o plano de risco e devolve (wal, doc, requestID).
func aos408Pendente(t *testing.T, bin, dir, runID string) (string, string, string) {
	t.Helper()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fixPath := filepath.Join(dir, "fixture.json")
	escrever(t, fixPath, aos408PlanoQueSeDizSeguro)
	doc := filepath.Join(dir, "pendente.json")
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", runID,
		"--goal", "recolher e publicar", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--plan-out", doc, "--worker", "p1")
	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("esperava pendente (%d), saiu %d\nstdout:\n%s\nstderr:\n%s", exitPendenteDeAprovacao, r.code, r.stdout, r.stderr)
	}
	// O `plans` é a superfície de leitura da cerimónia: é dele que sai o request_id a assinar.
	pl := correr(t, bin, "plans", "--wal", wal, "--run", runID)
	if !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("o `plans` tinha de listar o plano como PENDENTE:\n%s", pl.stdout)
	}
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("o `plans` tinha de imprimir o request_id a assinar:\n%s", pl.stdout)
	}
	return wal, doc, m[1]
}

// TestAOS408_DecisaoAssinadaAprovaEDepoisMaterializa é a contraprova positiva de toda a cerimónia.
func TestAOS408_DecisaoAssinadaAprovaEDepoisMaterializa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-aprova")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)
	snapPath := filepath.Join(dir, "snap.json")

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-aprova",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitOK {
		t.Fatalf("a decisão assinada legítima tinha de passar, saiu %d\nstdout:\n%s\nstderr:\n%s", d.code, d.stdout, d.stderr)
	}
	if !strings.Contains(d.stdout, "decisao APROVADA por human:alice") {
		t.Fatalf("a decisão tinha de nomear o aprovador:\n%s", d.stdout)
	}
	// O `plans` passa a mostrar DECIDIDO — o estado vive no log, não na memória do processo.
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-aprova")
	if !strings.Contains(pl.stdout, "estado=DECIDIDO") || !strings.Contains(pl.stdout, "decisao=approved") {
		t.Fatalf("o log tinha de ter a decisão aprovada:\n%s", pl.stdout)
	}

	// E SÓ AGORA materializa: o `serve --plan-doc` lê a decisão do log e admite os nós.
	m := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-aprova",
		"--plan-doc", doc, "--snapshot", snapPath, "--worker", "p2")
	if m.code != exitOK {
		t.Fatalf("com decisão aprovada no log tinha de materializar, saiu %d\nstdout:\n%s\nstderr:\n%s", m.code, m.stdout, m.stderr)
	}
	if !strings.Contains(m.stdout, "gate de plano: APROVADO por humano") {
		t.Fatalf("a materialização tinha de declarar que leu a decisão do log:\n%s", m.stdout)
	}
	if !strings.Contains(m.stdout, "materializado:") {
		t.Fatalf("o plano aprovado tinha de materializar:\n%s", m.stdout)
	}
}

// TestAOS408_PlanDocSemDecisaoNaoContornaOGate fecha o contorno: a flag chama-se `--plan-doc` e
// afirma «documento APROVADO», mas nada o verificava.
func TestAOS408_PlanDocSemDecisaoNaoContornaOGate(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, aos408PlanoQueSeDizSeguro)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-contorno",
		"--plan-doc", docPath, "--snapshot", snapPath, "--worker", "p1")
	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("um plano de risco por --plan-doc tinha de ficar pendente (%d), saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitPendenteDeAprovacao, r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("nada podia materializar:\n%s", r.stdout)
	}
}

// TestAOS408_AssinaturaForjadaRecusa: a chave que assina não é a PINADA.
func TestAOS408_AssinaturaForjadaRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-forja")
	_, aprovadores := aos408Aprovador(t, dir, "human:alice")
	// Chave DIFERENTE da pinada — o atacante assina bem, mas com a chave errada.
	_, outraPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	aprovacao := aos408Assinar(t, dir, "forjada.json", requestID, "human:alice", outraPriv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-forja",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitDecisaoRecusada {
		t.Fatalf("assinatura forjada tinha de sair %d, saiu %d\nstdout:\n%s\nstderr:\n%s", exitDecisaoRecusada, d.code, d.stdout, d.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-forja")
	if strings.Contains(pl.stdout, "decisao=approved") {
		t.Fatalf("uma assinatura forjada não podia aprovar nada:\n%s", pl.stdout)
	}
}

// TestAOS408_AprovadorSemAutoridadeParaAClasseRecusa: a chave é a pinada, mas a autoridade do
// principal não cobre `danger`. Autenticar não é autorizar.
func TestAOS408_AprovadorSemAutoridadeParaAClasseRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-autoridade")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice", "approve:gray")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-autoridade",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitDecisaoRecusada {
		t.Fatalf("aprovador só com approve:gray não pode aprovar danger: saiu %d\nstdout:\n%s\nstderr:\n%s", d.code, d.stdout, d.stderr)
	}
}

// TestAOS408_ReplayDaMesmaAprovacaoRecusa: o nonce é uso-único, com CAS durável no Event Store.
func TestAOS408_ReplayDaMesmaAprovacaoRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-replay")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)
	snapPath := filepath.Join(dir, "snap.json")

	args := []string{"decide", "--wal", wal, "--run", "run-aos408-replay",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores}
	if primeira := correr(t, bin, args...); primeira.code != exitOK {
		t.Fatalf("a primeira decisão tinha de passar, saiu %d\n%s\n%s", primeira.code, primeira.stdout, primeira.stderr)
	}
	segunda := correr(t, bin, args...)
	if segunda.code != exitDecisaoRecusada {
		t.Fatalf("o replay da MESMA aprovação tinha de sair %d, saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitDecisaoRecusada, segunda.code, segunda.stdout, segunda.stderr)
	}
}

// TestAOS408_DocumentoAdulteradoRecusa: entre a proposta e a decisão, o documento muda. O humano
// decidiria sobre outro organigrama.
func TestAOS408_DocumentoAdulteradoRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-adulterado")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)

	// Adultera o objectivo: muda o hash canónico sem mudar mais nada.
	escrever(t, doc, strings.Replace(aos408PlanoQueSeDizSeguro, `"objective": "recolher e publicar"`, `"objective": "recolher e publicar TUDO"`, 1))

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-adulterado",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitDecisaoRecusada {
		t.Fatalf("documento adulterado tinha de sair %d, saiu %d\nstdout:\n%s\nstderr:\n%s", exitDecisaoRecusada, d.code, d.stdout, d.stderr)
	}
	if !strings.Contains(d.stderr, "hash") {
		t.Fatalf("a recusa tinha de nomear a divergência de hash:\n%s", d.stderr)
	}
}

// TestAOS408_PrazoExpiradoRecusaSemFecharOPlano: o prazo é imposto no momento da decisão — e a
// expiração NÃO escreve nada.
//
// A primeira versão deste teste chamava-se «...FechaOCaso» e afirmava que uma decisão com
// `--ttl 1ns` gravava `plan.rejected` e fechava o plano para sempre. Isso era o ATAQUE, não a
// defesa: a segunda revisão adversarial reproduziu-o com ficheiros que nem existiam — quem quer que
// invocasse o comando fechava qualquer plano pendente. Agora a expiração é DERIVADA (instante do
// `plan.validated` + prazo da política), como o próprio pendente: um `--ttl` curto só recusa a
// decisão de quem o passa, e o plano continua pendente para toda a gente.
func TestAOS408_PrazoExpiradoRecusaSemFecharOPlano(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-prazo")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)
	snapPath := filepath.Join(dir, "snap.json")

	// `--ttl 1ns`: para ESTA decisão o pendente já está fora do prazo. É recusada.
	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-prazo",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores, "--ttl", "1ns")
	if d.code != exitDecisaoRecusada || !strings.Contains(d.stdout, "prazo do pendente expirou") {
		t.Fatalf("decisão fora do prazo tinha de sair %d nomeando o prazo, saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitDecisaoRecusada, d.code, d.stdout, d.stderr)
	}
	// E NÃO FICOU FACTO NENHUM: o plano continua pendente.
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-prazo")
	if !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("a expiração não pode escrever decisão — o plano tinha de continuar pendente:\n%s", pl.stdout)
	}
	// A prova de que não fechou: a decisão legítima, dentro do prazo da política, APROVA.
	ok := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-prazo",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if ok.code != exitOK {
		t.Fatalf("a decisão legítima dentro do prazo tinha de aprovar, saiu %d\n%s\n%s", ok.code, ok.stdout, ok.stderr)
	}
}

// TestAOS408_PrazoNaoPodeSerAlargadoPorQuemDecide: com um `--ttl` livre, quem quer aprovar um plano
// velho passava um prazo enorme e ressuscitava-o. O prazo só se encurta.
func TestAOS408_PrazoNaoPodeSerAlargadoPorQuemDecide(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-alargar")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-alargar",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores, "--ttl", "8760h")
	if d.code == exitOK {
		t.Fatalf("um --ttl maior que o da política tinha de ser recusado: saiu 0\n%s", d.stdout)
	}
	if !strings.Contains(d.stderr, "alarga") {
		t.Fatalf("a recusa tinha de dizer que o prazo não se alarga:\n%s", d.stderr)
	}
}

// TestAOS408_RecusaAssinadaFechaOPlano: uma RECUSA também é decisão assinada e também é
// não-repúdio. O plano fecha, não fica pendente para sempre.
func TestAOS408_RecusaAssinadaFechaOPlano(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-recusa")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	recusa := aos408Assinar(t, dir, "recusa.json", requestID, "human:alice", priv, false)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-recusa",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "reject",
		"--approval", recusa, "--approvers", aprovadores)
	if d.code != exitDecisaoRecusada {
		t.Fatalf("uma recusa assinada sai %d (decisão tomada, e foi não), saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitDecisaoRecusada, d.code, d.stdout, d.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-recusa")
	if !strings.Contains(pl.stdout, "decisao=rejected") {
		t.Fatalf("a recusa tinha de ficar no log como facto:\n%s", pl.stdout)
	}
}
