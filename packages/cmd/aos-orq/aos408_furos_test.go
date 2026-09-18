package main

// AOS-408 — os furos que a revisão adversarial encontrou, cada um com o seu teste.
//
// Todos foram REPRODUZIDOS com os binários reais antes de existir correcção. Ficam aqui porque uma
// correcção sem teste é uma afirmação, e a classe de defeito destes quatro é a mesma: **o veredicto
// do gate era recalculado a partir de entradas que quem decide fornece, e confrontado com a âncora
// errada**. Um refactor que reintroduza qualquer um deles tem de avermelhar aqui.

import (
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// aos408SnapshotBenigno tem as MESMAS tools que o snapshot real, mas com eixos inócuos: nada é
// irreversível, nada tem egress externo. É a arma do ataque A1 — passar ISTO ao `decide` fazia o
// plano perigoso resolver-se como `safe`.
const aos408SnapshotBenigno = `{
  "hash": "sha256:snap-benigno",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"}
  ]
}`

// TestAOS408_SnapshotTrocadoNaoAprovaNada fecha o furo ALTO A1: o risco do cartão deixa de poder
// ser escolhido por quem decide.
//
// FALHA-ANTES (reproduzida): com `--snapshot` benigno, `forcados` ficava vazio, a classe agregada
// caía para `safe`, a auto-aprovação por nível SALTAVA o canal — logo a assinatura nunca era
// verificada — e um `approver` inventado com assinatura de lixo aprovava um plano `danger`. Depois
// disso, o `serve --plan-doc` materializava-o.
func TestAOS408_SnapshotTrocadoNaoAprovaNada(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-snaptroca")
	// Aprovação FORJADA: aprovador que não está no registo e assinatura de uma chave aleatória.
	_, outraPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, aprovadores := aos408Aprovador(t, dir, "human:alice")
	forjada := aos408Assinar(t, dir, "forjada.json", requestID, "human:mallory", outraPriv, true)
	benigno := filepath.Join(dir, "snap-benigno.json")
	escrever(t, benigno, aos408SnapshotBenigno)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-snaptroca",
		"--plan-doc", doc, "--snapshot", benigno, "--decision", "approve",
		"--approval", forjada, "--approvers", aprovadores)
	if d.code == exitOK {
		t.Fatalf("um snapshot que não é o do plano tinha de ser recusado, saiu 0\nstdout:\n%s", d.stdout)
	}
	if !strings.Contains(d.stderr, "capabilities_hash") && !strings.Contains(d.stderr, "snapshot") {
		t.Fatalf("a recusa tinha de nomear a divergência de snapshot:\n%s", d.stderr)
	}
	// E o plano continua PENDENTE: nada foi decidido em nome de ninguém.
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-snaptroca")
	if !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("o plano tinha de continuar pendente:\n%s", pl.stdout)
	}
	// E não materializa.
	m := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-snaptroca",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--worker", "p2")
	if m.code == exitOK || strings.Contains(m.stdout, "materializado:") {
		t.Fatalf("nada podia materializar: saiu %d\n%s", m.code, m.stdout)
	}
}

// TestAOS408_PlanDocComSnapshotBenignoNaoContorna fecha o furo MÉDIO M5 — a mesma raiz de A1, pela
// porta do `--plan-doc`, que materializava sem gate, sem factos e sem pendente.
func TestAOS408_PlanDocComSnapshotBenignoNaoContorna(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	docPath := filepath.Join(dir, "plano.json")
	escrever(t, docPath, aos408PlanoQueSeDizSeguro)
	benigno := filepath.Join(dir, "snap-benigno.json")
	escrever(t, benigno, aos408SnapshotBenigno)
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-m5",
		"--plan-doc", docPath, "--snapshot", benigno, "--worker", "p1")
	if r.code == exitOK || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("um snapshot que não é o do plano não pode materializar nada: saiu %d\n%s", r.code, r.stdout)
	}
}

// TestAOS408_AprovacaoDeOutroOrganigramaNaoServe fecha o furo ALTO A2: a âncora é o hash da
// DECISÃO, não o do primeiro `plan.validated`.
//
// FALHA-ANTES (reproduzida sem chave nenhuma): decompor o plano perigoso (que sela o
// `plan.validated`), decompor DEPOIS um plano inócuo no MESMO plan_id — que auto-aprova e escreve
// `plan.approved` com outro hash — e materializar o perigoso. O `plans` chegava a imprimir
// `decisao=approved plan_hash=<o do perigoso>`.
func TestAOS408_AprovacaoDeOutroOrganigramaNaoServe(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	perigoso := filepath.Join(dir, "perigoso.json")
	escrever(t, perigoso, aos408PlanoQueSeDizSeguro)
	seguro := filepath.Join(dir, "seguro.json")
	escrever(t, seguro, planoFixtureDuasFolhasComSnapshotAOS408)
	wal := filepath.Join(dir, "es.wal")

	// (1) o plano perigoso fica pendente e sela o `plan.validated`.
	r1 := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-a2",
		"--goal", "recolher e publicar", "--snapshot", snapPath,
		"--decompose-fixture", perigoso, "--worker", "p1")
	if r1.code != exitPendenteDeAprovacao {
		t.Fatalf("esperava pendente, saiu %d\n%s\n%s", r1.code, r1.stdout, r1.stderr)
	}
	// (2) um plano SEM risco no MESMO plano auto-aprova (e escreve `plan.approved` com o SEU hash).
	r2 := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-a2",
		"--goal", "recolher e analisar", "--snapshot", snapPath,
		"--decompose-fixture", seguro, "--release", "--worker", "p2")
	if r2.code != exitOK {
		t.Fatalf("o plano sem risco devia passar, saiu %d\n%s\n%s", r2.code, r2.stdout, r2.stderr)
	}
	// (3) e o perigoso NÃO pode aproveitar essa aprovação.
	r3 := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-a2",
		"--plan-doc", perigoso, "--snapshot", snapPath, "--worker", "p3")
	if strings.Contains(r3.stdout, "materializado:") {
		t.Fatalf("a aprovação de um organigrama não pode materializar outro:\n%s", r3.stdout)
	}
	// A causa, e não só «qualquer código ≠ 0»: há `approved` no log, mas é de OUTRO hash e da
	// MÁQUINA — e é isso que a recusa tem de dizer.
	if r3.code != exitDecisaoRecusada || !strings.Contains(r3.stderr, "auto:autonomy") {
		t.Fatalf("esperava %d a nomear a auto-aprovação alheia, saiu %d\nstdout:\n%s\nstderr:\n%s",
			exitDecisaoRecusada, r3.code, r3.stdout, r3.stderr)
	}
}

// A segunda metade do A2 — uma auto-aprovação da máquina não conta como decisão humana, mesmo com
// o hash certo — é testada onde o predicado vive:
// packages/control-plane/runlifecycle/aos408_plan_decision_reader_test.go. A versão anterior aqui
// testava um literal de string e continuaria verde se o predicado deixasse de a verificar.

// TestAOS408_RecusaSemAssinaturaValidaNaoFechaOPlano fecha o furo ALTO A3: o ramo de recusa
// gravava `plan.rejected` ANTES de qualquer verificação.
//
// FALHA-ANTES (reproduzida): `decide --decision reject` com assinatura de lixo saía 7, escrevia
// `plan.rejected` com `decision_ref: hitl:human:alice` e — pela precedência terminal — impedia para
// sempre a aprovação do plano. Qualquer pessoa sem chave nenhuma matava um plano pendente e o log
// culpava um aprovador pinado.
func TestAOS408_RecusaSemAssinaturaValidaNaoFechaOPlano(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-a3")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	_, outraPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Recusa assinada com a chave ERRADA, em nome da alice.
	recusaForjada := aos408Assinar(t, dir, "recusa-forjada.json", requestID, "human:alice", outraPriv, false)
	snapPath := filepath.Join(dir, "snap.json")

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-a3",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "reject",
		"--approval", recusaForjada, "--approvers", aprovadores)
	if d.code == exitOK {
		t.Fatalf("uma recusa com assinatura inválida não podia ter efeito: saiu 0\n%s", d.stdout)
	}
	// O PLANO CONTINUA PENDENTE — não foi fechado por quem não tem chave.
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-a3")
	if !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("o plano não podia ter sido fechado por uma recusa não verificada:\n%s", pl.stdout)
	}
	// E a decisão LEGÍTIMA da alice ainda funciona (o plano não ficou inutilizado).
	legitima := aos408Assinar(t, dir, "legitima.json", requestID, "human:alice", priv, true)
	ok := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-a3",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", legitima, "--approvers", aprovadores)
	if ok.code != exitOK {
		t.Fatalf("a decisão legítima tinha de continuar possível, saiu %d\n%s\n%s", ok.code, ok.stdout, ok.stderr)
	}
}

// aos408SnapshotComGray resolve `http.get` como GRAY (egress interno, reversível) ao lado do
// `http.post` danger. É a configuração comum — e a que tornava o plano inaprovável.
const aos408SnapshotComGray = `{
  "hash": "sha256:snap-gray",
  "tools": [
    {"name":"http.get","version":"1.0.0","digest":"sha256:ggg","admissible":true,
     "sensitivity":"internal","egress":"internal","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"sensitive","egress":"external","reversibility":"irreversible"}
  ]
}`

const aos408PlanoGrayEDanger = `{
  "plan_version": "1.0.0",
  "objective": "consultar e publicar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-gray"},
  "nodes": [
    {"node_id":"consulta","role":"worker","objective":"consultar","depends_on":[],
     "tools":[{"name":"http.get","version":"1.0.0","digest":"sha256:ggg"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"publicacao","role":"worker","objective":"publicar","depends_on":["consulta"],
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS408_PlanoComGrayEAprovavel fecha o furo ALTO A4: a revisão forçada do gate cobre
// `Class >= gray`, e o revisor declarava revistos só os `danger|gap`.
//
// FALHA-ANTES (reproduzida): com um nó `gray` ao lado do `danger`, a decisão LEGÍTIMA da alice
// saía 7 com «no forcado (>=gray/capability_gap) nao revisto» — e, pior, gravava `plan.rejected`,
// pelo que a segunda tentativa já encontrava o caso fechado. Como `gray` é o caso comum, a
// cerimónia só funcionava na forma exacta das fixtures.
func TestAOS408_PlanoComGrayEAprovavel(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComGray)
	fixPath := filepath.Join(dir, "fixture.json")
	escrever(t, fixPath, aos408PlanoGrayEDanger)
	doc := filepath.Join(dir, "pendente.json")
	wal := filepath.Join(dir, "es.wal")

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-gray",
		"--goal", "consultar e publicar", "--snapshot", snapPath,
		"--decompose-fixture", fixPath, "--plan-out", doc, "--worker", "p1")
	if r.code != exitPendenteDeAprovacao {
		t.Fatalf("esperava pendente, saiu %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-gray")
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", m[1], "human:alice", priv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-gray",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitOK {
		t.Fatalf("um plano com nó gray ao lado do danger TEM de ser aprovável, saiu %d\nstdout:\n%s\nstderr:\n%s",
			d.code, d.stdout, d.stderr)
	}
}

// TestAOS408_PrazoNaoPositivoERecusado fecha o furo MÉDIO M8: `--ttl 0` desligava a expiração, e
// quem o passava era quem decide.
func TestAOS408_PrazoNaoPositivoERecusado(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-m8")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-m8",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores, "--ttl", "0")
	if d.code == exitOK {
		t.Fatalf("--ttl 0 desligaria a expiração e tem de ser recusado: saiu 0\n%s", d.stdout)
	}
	if !strings.Contains(d.stderr, "ttl") && !strings.Contains(d.stderr, "positivo") {
		t.Fatalf("a recusa tinha de nomear o prazo:\n%s", d.stderr)
	}
}

// TestAOS408_ChavePartilhadaEntreAprovadoresERecusada fecha o B11: duas identidades com a mesma
// chave são uma identidade a fingir-se de duas — e seriam a forma óbvia de derrotar o dual-control.
func TestAOS408_ChavePartilhadaEntreAprovadoresERecusada(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-b11")
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	hexPub := hexDe(pub)
	escrever(t, filepath.Join(dir, "approvers.json"), `{"approvers":[
	  {"principal":"human:alice","pubkey":"`+hexPub+`","authority":["approve:danger"]},
	  {"principal":"human:bob","pubkey":"`+hexPub+`","authority":["approve:danger"]}]}`)
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-b11",
		"--plan-doc", doc, "--snapshot", filepath.Join(dir, "snap.json"), "--decision", "approve",
		"--approval", aprovacao, "--approvers", filepath.Join(dir, "approvers.json"))
	if d.code == exitOK {
		t.Fatalf("dois aprovadores com a mesma pubkey têm de ser recusados: saiu 0\n%s", d.stdout)
	}
}

// hexDe é o hex de uma chave pública, para o ficheiro de aprovadores.
func hexDe(pub ed25519.PublicKey) string { return hex.EncodeToString(pub) }

// planoFixtureDuasFolhasComSnapshotAOS408 é o plano SEM risco que declara o snapshot com perigo —
// necessário desde que o snapshot passou a ser amarrado ao `capabilities_hash` do documento.
const planoFixtureDuasFolhasComSnapshotAOS408 = `{
  "plan_version": "1.0.0",
  "objective": "recolher e analisar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"analise","role":"worker","objective":"analisar","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// aos408PlanoRiscoIndependente tem o nó de risco SEM dependências: é elegível logo na primeira
// passagem de despacho, e por isso é apresentado ao oráculo de cartão. (No plano das outras fixtures
// o nó de risco depende do de leitura e nem chega a ser avaliado nessa passagem.)
const aos408PlanoRiscoIndependente = `{
  "plan_version": "1.0.0",
  "objective": "recolher e publicar",
  "budget_total": {"tokens": 100, "cost_micro_usd": 100},
  "planner_meta": {"model":"fixture","prompt_version":"1.2.0","capabilities_hash":"sha256:snap-aos408"},
  "nodes": [
    {"node_id":"recolha","role":"worker","objective":"recolher","depends_on":[],
     "tools":[{"name":"fs.read","version":"1.0.0","digest":"sha256:aaa"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}},
    {"node_id":"publicacao","role":"worker","objective":"publicar","depends_on":[],
     "risk_class":"safe",
     "tools":[{"name":"http.post","version":"2.0.0","digest":"sha256:bbb"}],
     "budget_estimate":{"tokens":10,"cost_micro_usd":10}}
  ]
}`

// TestAOS408_DepoisDaAprovacaoODespachoConsultaOOraculo fecha o furo MÉDIO M7 — para que serve a
// decisão.
//
// FALHA-ANTES (por análise, confirmada): o despacho só corria pelo caminho do `--goal`, e esse
// caminho só chegava ao despacho quando NENHUM nó exigia cartão, pelo que o `CardOracle` nunca era
// consultado e um plano aprovado não tinha caminho para correr.
//
// O que se afirma aqui é o ARRANQUE DO NÓ DE RISCO: `publicacao` exige cartão (é `danger` pelas
// tools, embora se declare `safe`) e só arranca se o oráculo a autorizar. Sem isso o despacho
// arrancaria só a `recolha` (`nos_despachados=1`). A versão anterior deste teste usava um plano em
// que o nó de risco dependia do outro — nunca chegava ao oráculo, e o teste só confirmava que
// «algum» nó despachara.
func TestAOS408_DepoisDaAprovacaoODespachoConsultaOOraculo(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	escrever(t, snapPath, aos408SnapshotComPerigo)
	fixPath := filepath.Join(dir, "fixture.json")
	escrever(t, fixPath, aos408PlanoRiscoIndependente)
	doc := filepath.Join(dir, "pendente.json")
	wal := filepath.Join(dir, "es.wal")
	const run = "run-aos408-m7"

	r1 := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher e publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixPath, "--plan-out", doc, "--worker", "p1")
	if r1.code != exitPendenteDeAprovacao {
		t.Fatalf("esperava pendente, saiu %d\n%s\n%s", r1.code, r1.stdout, r1.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", run)
	m := reRequestID.FindStringSubmatch(pl.stdout)
	if m == nil {
		t.Fatalf("sem request_id:\n%s", pl.stdout)
	}
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", m[1], "human:alice", priv, true)
	d := correr(t, bin, "decide", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", snapPath,
		"--decision", "approve", "--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitOK {
		t.Fatalf("a aprovação legítima falhou: %d\n%s\n%s", d.code, d.stdout, d.stderr)
	}

	// A MESMA invocação que ficou pendente, repetida: o plano é o mesmo (fixture), logo o hash é o
	// mesmo, e a decisão no log é reconhecida.
	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--goal", "recolher e publicar",
		"--snapshot", snapPath, "--decompose-fixture", fixPath, "--worker", "p2")
	if r.code != exitOK {
		t.Fatalf("com a decisão no log, o mesmo comando tinha de prosseguir; saiu %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "gate de plano: APROVADO por humano") {
		t.Fatalf("tinha de reconhecer a decisão humana do log:\n%s", r.stdout)
	}
	// O NÓ DE RISCO ARRANCA — autorizado pelo oráculo de cartão, no despacho.
	if !strings.Contains(r.stdout, "folha publicacao a arrancar") || !strings.Contains(r.stdout, "nos_despachados=2") {
		t.Fatalf("o nó danger tinha de passar o oráculo e arrancar (nos_despachados=2):\n%s", r.stdout)
	}
}

// aos408SnapshotComRotuloCopiado tem o MESMO rótulo `hash` do snapshot real, mas a tool perigosa
// passa a `gray` (egress interno, reversível). O rótulo é texto do próprio ficheiro, pelo que a
// amarra ao `capabilities_hash` não o apanha — só o digest do CONTEÚDO selado no `plan.validated`.
const aos408SnapshotComRotuloCopiado = `{
  "hash": "sha256:snap-aos408",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"http.post","version":"2.0.0","digest":"sha256:bbb","admissible":true,
     "sensitivity":"internal","egress":"internal","reversibility":"reversible"}
  ]
}`

// TestAOS408_EscaladaPorCatalogoComRotuloCopiadoRecusa fecha o achado ALTO 2 da 2.ª revisão.
//
// FALHA-ANTES (reproduzida): uma aprovadora só com `approve:gray` apresentava ao `decide` um
// catálogo com o rótulo do real e a tool perigosa como `gray`; a decisão passava («APROVADA …
// nos_de_risco=0») e depois valia no `serve --plan-doc` com o catálogo REAL — um `danger` aprovado
// por quem só tem autoridade para `gray`.
func TestAOS408_EscaladaPorCatalogoComRotuloCopiadoRecusa(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-r3")
	priv, aprovadores := aos408Aprovador(t, dir, "human:gina", "approve:gray")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:gina", priv, true)
	copiado := filepath.Join(dir, "snap-copiado.json")
	escrever(t, copiado, aos408SnapshotComRotuloCopiado)

	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-r3",
		"--plan-doc", doc, "--snapshot", copiado, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code == exitOK {
		t.Fatalf("um catálogo com conteúdo diferente do selado não pode servir para decidir: saiu 0\n%s", d.stdout)
	}
	if !strings.Contains(d.stderr, "selado") {
		t.Fatalf("a recusa tinha de ser pelo conteúdo do snapshot selado (e não por outra razão):\n%s", d.stderr)
	}
	pl := correr(t, bin, "plans", "--wal", wal, "--run", "run-aos408-r3")
	if !strings.Contains(pl.stdout, "estado=PENDENTE") {
		t.Fatalf("o plano tinha de continuar pendente:\n%s", pl.stdout)
	}
}

// TestAOS408_DecisaoDeUmRunNaoServeOutro fecha o achado ALTO 4 da 2.ª revisão: o `decide` derivava o
// plano do run, mas o `serve` continuava a aceitar `--plan`.
//
// FALHA-ANTES (reproduzida): a alice aprova no runA; `serve --run runB --plan runA-plan --plan-doc`
// materializava os nós no DAG do runB, com o lease e o orçamento do runB.
func TestAOS408_DecisaoDeUmRunNaoServeOutro(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	wal, doc, requestID := aos408Pendente(t, bin, dir, "run-aos408-ra")
	priv, aprovadores := aos408Aprovador(t, dir, "human:alice")
	aprovacao := aos408Assinar(t, dir, "aprovacao.json", requestID, "human:alice", priv, true)
	snapPath := filepath.Join(dir, "snap.json")
	d := correr(t, bin, "decide", "--wal", wal, "--run", "run-aos408-ra",
		"--plan-doc", doc, "--snapshot", snapPath, "--decision", "approve",
		"--approval", aprovacao, "--approvers", aprovadores)
	if d.code != exitOK {
		t.Fatalf("a aprovação legítima no runA falhou: %d\n%s\n%s", d.code, d.stdout, d.stderr)
	}

	r := correr(t, bin, "serve", "--wal", wal, "--run", "run-aos408-rb", "--plan", "run-aos408-ra-plan",
		"--plan-doc", doc, "--snapshot", snapPath, "--worker", "p2")
	if r.code == exitOK || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("a decisão do runA não pode materializar no runB: saiu %d\n%s", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "nao atravessa runs") {
		t.Fatalf("a recusa tinha de nomear a travessia de runs:\n%s", r.stderr)
	}
	insp := correr(t, bin, "inspect", "--wal", wal, "--run", "run-aos408-rb")
	if !strings.Contains(insp.stdout, "nos=0") {
		t.Fatalf("nenhum nó podia ter entrado no DAG do runB:\n%s", insp.stdout)
	}
}
