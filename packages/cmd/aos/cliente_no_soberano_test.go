package main

import (
	"net/http/httptest"

	govsov "github.com/aos-ref/control-plane/governance/sovereignty"
	"github.com/aos-ref/platform/audit"
	"strings"
	"testing"
)

// O CLIENTE OFICIAL TEM DE CONSEGUIR SUBMETER A UM NO SOBERANO.
//
// Achado do teste do FLUXO REAL de 2026-08-25, e so apareceu por USAR o sistema em vez de o
// validar: `POST /runs` atravessa a MESMA governacao de leitura soberana que o
// `GET /runs/{id}`, e o subcomando `run` nao transportava `X-Aos-Reader`/`X-Aos-Board`. O
// `observe` transportava — a assimetria estava na CLI, nao no servidor.
//
// Medido no binario real, mesmo no e mesmo pedido:
//
//	aos run --addr … --objective … --run-id … --nhi …   ->  403 nao autorizado
//	curl + X-Aos-Reader + X-Aos-Board                    ->  201 accepted
//
// A soberania esta composta POR OMISSAO — e e a postura de producao. Um no cuja porta de
// entrada nao e alcancavel pelo seu proprio cliente oficial e, na pratica, um no sem porta.
//
// PORQUE O E2E QUE JA EXISTIA NAO APANHAVA ISTO: o `newCLIServer` compoe um no em modo
// LEGADO, sem soberania. O `cmdRun` passava por nao haver gate nenhum a atravessar. Aqui
// compoe-se o gate — que e a unica diferenca que importa.
func TestCliente_SubmeteANoSoberano(t *testing.T) {
	// Molde de aos182_residency_test.go: a soberania entra pela APIOption da casa.
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	regions := govsov.NewRegistry(map[string]string{"board:demo": "eu"})
	_, h := newAPI(t, node, WithReadSovereignty(regions, audit.NewMemStore()))

	srv := httptest.NewServer(h)
	defer srv.Close()

	// (1) SEM as flags: tem de ser RECUSADO. E a reproducao do defeito.
	var semFlags strings.Builder
	err := cmdRun([]string{"--addr", srv.URL, "--run-id", "sem-flags", "--objective", "x", "--nhi", "nhi:a"}, &semFlags)
	if err == nil {
		t.Fatal("sem --reader/--board o submit devia ser RECUSADO por um no soberano")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("esperava 403 do gate soberano, veio: %v", err)
	}

	// (2) COM as flags: tem de PASSAR. E a correccao.
	var comFlags strings.Builder
	if err := cmdRun([]string{
		"--addr", srv.URL, "--run-id", "com-flags", "--objective", "x", "--nhi", "nhi:a",
		"--reader", "human:auditor", "--board", "board:demo",
	}, &comFlags); err != nil {
		t.Fatalf("com --reader/--board o submit devia PASSAR: %v", err)
	}
	if !strings.Contains(comFlags.String(), "com-flags") {
		t.Fatalf("output do submit nao nomeia o run: %q", comFlags.String())
	}
}

// CONTROLO ANTI-VACUIDADE: UM BOARD DESCONHECIDO CONTINUA A SER RECUSADO.
//
// Sem este caso, uma "correccao" que mandasse os headers e o no que os aceitasse sem os
// resolver passaria o teste de cima — e a soberania, que e o ponto, teria sido desligada em
// vez de satisfeita.
func TestCliente_BoardDesconhecidoContinuaRecusado(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	regions := govsov.NewRegistry(map[string]string{"board:demo": "eu"})
	_, h := newAPI(t, node, WithReadSovereignty(regions, audit.NewMemStore()))
	srv := httptest.NewServer(h)
	defer srv.Close()

	var b strings.Builder
	err := cmdRun([]string{
		"--addr", srv.URL, "--run-id", "board-mau", "--objective", "x", "--nhi", "nhi:a",
		"--reader", "human:auditor", "--board", "board:QUE-NAO-EXISTE",
	}, &b)
	if err == nil {
		t.Fatal("CONTROLO: um board DESCONHECIDO devia continuar a ser recusado — a correccao nao pode ser 'aceitar tudo'")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("CONTROLO: esperava 403, veio: %v", err)
	}
}

// CONTROLO 2: UM NO LEGADO (sem soberania) CONTINUA A ACEITAR SEM FLAGS.
//
// A correccao nao pode passar a EXIGIR o que antes era opcional — quem tem um no sem
// soberania composta nao deve ser obrigado a inventar um board.
func TestCliente_NoLegadoContinuaAAceitarSemFlags(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node) // SEM WithReadSovereignty: modo LEGADO
	srv := httptest.NewServer(h)
	defer srv.Close()

	var b strings.Builder
	if err := cmdRun([]string{"--addr", srv.URL, "--run-id", "legado", "--objective", "x", "--nhi", "nhi:a"}, &b); err != nil {
		t.Fatalf("CONTROLO: um no LEGADO devia continuar a aceitar sem flags: %v", err)
	}
}
