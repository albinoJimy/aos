package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-377, metade (a) — a SUBIDA a L4/L5 por AOS_AUTONOMY_LEVELS exige PROVA ASSINADA.
//
// O DEFEITO QUE ISTO FECHA. `AOS_AUTONOMY_LEVELS` + reinício aplicava QUALQUER nível SEM
// assinatura, contornando o dual-control que `POST /autonomy` impõe a L4/L5 (o limiar em que
// `danger` deixa de esperar por um humano). A opção A2 do dono: a subida pelo ficheiro passa a
// exigir a MESMA prova de duas assinaturas — transportada em `AOS_AUTONOMY_PROOFS` — reutilizando a
// verificação da rota e da rehidratação. Sem prova válida a subida é RECUSADA AO NÍVEL (o par fica
// no anterior), NUNCA abortando o boot — o mesmo fail-closed sem modo de tijolo da rehidratação.
//
// Estes testes fixam a regra nas DUAS direcções, o selo com actor `config:node`, a direcção
// declarada, a retro-compatibilidade de um nível já selado, e o CONTROLO NEGATIVO (com o gate
// desarmado a subida sem prova aplica — a prova de que é o gate que a recusa, e não outra coisa).

// arrancaComGateDeProva corre a FASE 2 do arranque com o gate de prova de subida ARMADO — a linha
// do [Bootstrap] (armarGateDeProva + provision com o validador de rehidratação), sem o resto do nó.
func arrancaComGateDeProva(worm audit.Store, specs []autonomyLevelSpec, pubs map[string]ed25519.PublicKey, setters map[string]bool, provas map[string][]autonomy.LevelChangeProof) (*autonomyWiring, error) {
	w := buildAutonomyOracle(specs, autonomy.L0)
	if w == nil {
		return nil, nil
	}
	w.provasPorPar = provas
	w.armarGateDeProva(pubs, setters)
	return w, w.provision(context.Background(), worm,
		autonomy.WithRehydrateValidator(autonomyRehydrateValidator(pubs, setters)))
}

// TestAOS377_SubidaAL5SemProvaRecusadaComProvaAplicadaDescidaLivre — o AC3 nos dois sentidos, num
// só teste porque as três metades são a mesma regra vista de três lados.
func TestAOS377_SubidaAL5SemProvaRecusadaComProvaAplicadaDescidaLivre(t *testing.T) {
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	// (a) SEM prova: o par está selado a L1 (config:node) e o ambiente pede L5 ⇒ a subida é
	// RECUSADA ao nível. O par fica em L1, o boot NÃO aborta, e a recusa é declarada.
	semProva := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, semProva, "agt-1", "fs", autonomy.L1)
	w1, err := arrancaComGateDeProva(semProva, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, pubs, setters, nil)
	if err != nil {
		t.Fatalf("uma subida sem prova NAO pode abortar o arranque (fail-closed ao nivel, nao modo de tijolo): %v", err)
	}
	if got := w1.registry.LevelFor("agt-1", "fs"); got != autonomy.L1 {
		t.Fatalf("sem prova a subida a L5 devia ser recusada e o par ficar em L1, veio %s", got)
	}
	if len(w1.recusadosPorProva) != 1 || !strings.Contains(w1.recusadosPorProva[0], "agt-1:fs=L5") {
		t.Fatalf("a recusa tem de ser declarada com o par: %v", w1.recusadosPorProva)
	}
	// E o banner grita-o, com o remédio.
	banner1 := strings.Join(autonomyPostureBanner(w1), "\n")
	for _, exigido := range []string{"RECUSADA", "AOS_AUTONOMY_PROOFS", "L4/L5"} {
		if !strings.Contains(banner1, exigido) {
			t.Errorf("o banner de recusa nao contem %q:\n%s", exigido, banner1)
		}
	}

	// (b) COM duas provas de setters DISTINTOS: a MESMA subida é aplicada e selada como
	// `config:node` COM as provas (AC4).
	comProva := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, comProva, "agt-1", "fs", autonomy.L1)
	provas := map[string][]autonomy.LevelChangeProof{
		"agt-1:fs=L5": {
			provaDe(t, privs, "op:a", "agt-1", "fs", "L5", autonomyProvisionReason),
			provaDe(t, privs, "op:b", "agt-1", "fs", "L5", autonomyProvisionReason),
		},
	}
	w2, err := arrancaComGateDeProva(comProva, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, pubs, setters, provas)
	if err != nil {
		t.Fatalf("a subida COM duas provas validas devia aplicar: %v", err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("com duas provas a subida devia aplicar L5, veio %s", got)
	}
	if len(w2.recusadosPorProva) != 0 {
		t.Errorf("uma subida com prova valida nao pode aparecer como recusada: %v", w2.recusadosPorProva)
	}
	// AC4: selado com actor config:node E carregando as provas.
	last, ok := w2.registry.LastChange("agt-1", "fs")
	if !ok || last.Actor != autonomyProvisionActor {
		t.Fatalf("a subida aplicada tem de ficar selada como actor %q, veio %q", autonomyProvisionActor, last.Actor)
	}
	if len(last.Proofs) != 2 {
		t.Fatalf("o selo config:node tem de transportar as DUAS provas que o autorizaram, veio %d", len(last.Proofs))
	}

	// (c) DESCIDA para L0: aplicada SEM prova (AC2 — a de-escalada é a alavanca de incidente).
	descida := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, descida, "agt-1", "fs", autonomy.L5)
	w3, err := arrancaComGateDeProva(descida, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L0}}, pubs, setters, nil)
	if err != nil {
		t.Fatalf("uma descida nao pode ser gated: %v", err)
	}
	if got := w3.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("a descida para L0 devia aplicar sem prova, veio %s", got)
	}
	if len(w3.recusadosPorProva) != 0 {
		t.Errorf("uma descida nunca e recusada por prova: %v", w3.recusadosPorProva)
	}
}

// TestAOS377_ParNovoAL5SemProvaRecusado — um par que nunca esteve no WORM a nascer em L5 é a MESMA
// remoção de supervisão que um L1 promovido, e o gate trata-o igual: sem prova, fica no piso.
func TestAOS377_ParNovoAL5SemProvaRecusado(t *testing.T) {
	pubs, _ := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	worm := audit.NewMemStore() // vazio: par novo, sem selo anterior
	w, err := arrancaComGateDeProva(worm, []autonomyLevelSpec{{agent: "agt-novo", domain: "fs", level: autonomy.L5}}, pubs, setters, nil)
	if err != nil {
		t.Fatalf("recusa ao nivel, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-novo", "fs"); got != autonomy.L0 {
		t.Fatalf("um par novo a L5 sem prova devia ficar no piso L0, veio %s", got)
	}
	if len(w.recusadosPorProva) != 1 {
		t.Fatalf("a recusa devia ser declarada: %v", w.recusadosPorProva)
	}
}

// TestAOS377_UmaSoProvaNaoChegaParaSubirAL5 — a subida pelo ficheiro não pode ser a porta das
// traseiras do dual-control: uma prova VÁLIDA de um setter, para L5, é insuficiente.
func TestAOS377_UmaSoProvaNaoChegaParaSubirAL5(t *testing.T) {
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	worm := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)
	provas := map[string][]autonomy.LevelChangeProof{
		"agt-1:fs=L5": {provaDe(t, privs, "op:a", "agt-1", "fs", "L5", autonomyProvisionReason)},
	}
	w, err := arrancaComGateDeProva(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, pubs, setters, provas)
	if err != nil {
		t.Fatalf("recusa ao nivel, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L1 {
		t.Fatalf("uma so prova nao chega para L5; o par devia ficar em L1, veio %s", got)
	}
	if len(w.recusadosPorProva) != 1 || !strings.Contains(w.recusadosPorProva[0], "exigidas 2") {
		t.Fatalf("a recusa devia dizer quantas provas faltam: %v", w.recusadosPorProva)
	}
}

// TestAOS377_ProvaDeQuemNaoDetemAutonomySetNaoConta — a autoridade é a de AGORA: uma assinatura
// válida de quem não está em AOS_AUTONOMY_SETTERS não autoriza a subida, tal como na rota.
func TestAOS377_ProvaDeQuemNaoDetemAutonomySetNaoConta(t *testing.T) {
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:c")
	setters := map[string]bool{"op:a": true} // op:c assina, mas nao e setter

	worm := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)
	provas := map[string][]autonomy.LevelChangeProof{
		"agt-1:fs=L5": {
			provaDe(t, privs, "op:a", "agt-1", "fs", "L5", autonomyProvisionReason),
			provaDe(t, privs, "op:c", "agt-1", "fs", "L5", autonomyProvisionReason),
		},
	}
	w, err := arrancaComGateDeProva(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, pubs, setters, provas)
	if err != nil {
		t.Fatalf("recusa ao nivel, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L1 {
		t.Fatalf("com so um setter valido a subida a L5 devia ser recusada, veio %s", got)
	}
	if len(w.recusadosPorProva) != 1 || !strings.Contains(w.recusadosPorProva[0], autonomySetCapability) {
		t.Fatalf("a recusa devia nomear o direito em falta (%s): %v", autonomySetCapability, w.recusadosPorProva)
	}
}

// TestAOS377_ControloNegativoSemGateArmadoSubidaSemProvaAplica — a NÃO-VACUOSIDADE. Com o gate
// DESARMADO (o comportamento de um provision de teste de módulo, e o de antes deste ticket) a
// mesma subida sem prova APLICA-SE. É a prova de que é o gate — e não outra coisa — que a recusa.
func TestAOS377_ControloNegativoSemGateArmadoSubidaSemProvaAplica(t *testing.T) {
	worm := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)

	w := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, autonomy.L0)
	// NÃO se arma o gate: provaGateArmado fica false.
	if err := w.provision(context.Background(), worm); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("controlo negativo: SEM gate armado a subida sem prova DEVIA aplicar L5 — veio %s (se veio L1, o gate dispara sem estar armado, e os testes de recusa mediriam a maquinaria em vez do gate)", got)
	}
	if len(w.recusadosPorProva) != 0 {
		t.Fatalf("sem gate armado nada e recusado por prova: %v", w.recusadosPorProva)
	}
}

// TestAOS377_RetroCompatNivelJaSeladoNaoPedeProva — a retro-compatibilidade crítica: um par já
// selado a L5 (config:node) com AOS_AUTONOMY_LEVELS INALTERADO reinicia sem pedir prova. A subida
// idempotente cai no ramo de salto ("mesmo valor já selado"), que está ANTES do gate.
func TestAOS377_RetroCompatNivelJaSeladoNaoPedeProva(t *testing.T) {
	pubs, _ := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	worm := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L5) // já selado config:node L5
	w, err := arrancaComGateDeProva(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L5}}, pubs, setters, nil)
	if err != nil {
		t.Fatalf("um deployment inalterado nao pode falhar: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("o par ja selado a L5 devia continuar L5 sem prova, veio %s", got)
	}
	if len(w.recusadosPorProva) != 0 {
		t.Fatalf("um deployment INALTERADO com o nivel ja selado NAO pode pedir prova (retro-compat): %v", w.recusadosPorProva)
	}
}

// TestAOS377_AmbienteEditadoNomeiaDirecao — AC5: quando o ambiente ganha a uma decisão de
// operador, a linha declarada nomeia a DIRECÇÃO (subida/descida), não só o par e o valor.
func TestAOS377_AmbienteEditadoNomeiaDirecao(t *testing.T) {
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}
	const motivo = "operador subiu por decisao assinada"

	// DESCIDA sobre operador: base L1, operador subiu a L5, ambiente agora pede L2 (abaixo de L4,
	// logo o gate não se aplica — é uma de-escalada). A linha tem de dizer "descida".
	worm := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)
	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L1, New: autonomy.L5, Reason: motivo, Actor: "op:a,op:b",
		Proofs: []autonomy.LevelChangeProof{
			provaDe(t, privs, "op:a", "agt-1", "fs", "L5", motivo),
			provaDe(t, privs, "op:b", "agt-1", "fs", "L5", motivo),
		},
	})
	w, err := arrancaComGateDeProva(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L2}}, pubs, setters, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L2 {
		t.Fatalf("o ambiente editado (descida) devia ganhar: veio %s", got)
	}
	if len(w.ambienteEditado) != 1 || !strings.Contains(w.ambienteEditado[0], "descida") {
		t.Fatalf("a linha de ambiente editado tem de nomear a direccao 'descida': %v", w.ambienteEditado)
	}

	// SUBIDA (abaixo de L4) sobre operador: base L2, operador baixou a L0, ambiente agora pede L3.
	// L3 não atravessa o limiar, logo não é gated, mas a direcção é "subida".
	worm2 := audit.NewMemStore()
	apendeBaseDeProvisionamento(t, worm2, "agt-2", "http", autonomy.L2)
	apendeAlteracao(t, worm2, autonomy.LevelChange{
		Agent: "agt-2", Domain: "http", Old: autonomy.L2, New: autonomy.L0, Reason: "operador baixou",
		Actor:  "op:a",
		Proofs: []autonomy.LevelChangeProof{provaDe(t, privs, "op:a", "agt-2", "http", "L0", "operador baixou")},
	})
	w2, err := arrancaComGateDeProva(worm2, []autonomyLevelSpec{{agent: "agt-2", domain: "http", level: autonomy.L3}}, pubs, setters, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor("agt-2", "http"); got != autonomy.L3 {
		t.Fatalf("subida abaixo de L4 nao e gated: veio %s", got)
	}
	if len(w2.ambienteEditado) != 1 || !strings.Contains(w2.ambienteEditado[0], "subida") {
		t.Fatalf("a linha de ambiente editado tem de nomear a direccao 'subida': %v", w2.ambienteEditado)
	}
}

// TestAOS377_ParseAutonomyProofs — o parser fail-closed de AOS_AUTONOMY_PROOFS, no molde dos
// testes de parseAutonomySetters: vazio ⇒ nil; formas malformadas ⇒ ErrBadAutonomyProofs; forma
// válida ⇒ mapa com a chave normalizada.
func TestAOS377_ParseAutonomyProofs(t *testing.T) {
	if m, err := parseAutonomyProofs(""); m != nil || err != nil {
		t.Fatalf("vazio devia dar (nil, nil), veio (%v, %v)", m, err)
	}
	for _, mau := range []string{
		`nao e json`,
		`{}`,
		`{"agt-1:fs": [{"emitter_id":"op:a"}]}`, // chave sem =Ln
		`{"agt-1:fs=L9": [{"emitter_id":"op:a"}]}`,                          // nivel invalido
		`{"semdoispontos=L5": [{"emitter_id":"op:a"}]}`,                     // par sem :
		`{"agt-1:fs=L5": []}`,                                               // sem provas
		`{"agt-1:fs=L5": [{"emitter_id":""}]}`,                              // prova sem emitter_id
		`{"agt-1:fs=L5": [{"campo_desconhecido":"x","emitter_id":"op:a"}]}`, // campo estranho (DisallowUnknownFields)
	} {
		if _, err := parseAutonomyProofs(mau); !errors.Is(err, ErrBadAutonomyProofs) {
			t.Errorf("%q devia dar ErrBadAutonomyProofs, veio %v", mau, err)
		}
	}
	// Válido: chave normalizada (nível em maiúsculas, espaços de contorno removidos).
	m, err := parseAutonomyProofs(`{" agt-1:fs = l5 ": [{"emitter_id":"op:a","signature_b64":"AA==","nonce_b64":"AA==","issued_at":"2026-01-01T00:00:00Z"}]}`)
	if err != nil {
		t.Fatalf("forma valida recusada: %v", err)
	}
	if _, ok := m["agt-1:fs=L5"]; !ok {
		t.Fatalf("a chave devia ser normalizada para 'agt-1:fs=L5', veio %v", m)
	}
}

// TestAOS377_PisoDangerRecusado — um AOS_AUTONOMY_DEFAULT >= L4 ABORTA o arranque (AOS-377).
//
// É o bypass irmão que a revisão adversarial apanhou: fechar a SUBIDA por-par a L4/L5 sem fechar o
// PISO deixaria a escalada MAIS larga (frota inteira, e os agent_id são por-run) na cerimónia MAIS
// baixa (uma env var em branco, sem prova). O piso danger é recusado; L0..L3 e ausente passam.
func TestAOS377_PisoDangerRecusado(t *testing.T) {
	for _, danger := range []string{"L4", "L5"} {
		t.Setenv("AOS_AUTONOMY_DEFAULT", danger)
		if _, err := parseAutonomyDefault(); !errors.Is(err, ErrAutonomyDefaultDanger) {
			t.Errorf("AOS_AUTONOMY_DEFAULT=%s devia dar ErrAutonomyDefaultDanger, veio %v", danger, err)
		}
	}
	// Controlo: o piso supervisionado e a ausência continuam a passar (retro-compat da de-escalada).
	for _, ok := range []struct {
		env  string
		quer autonomy.Level
	}{{"", autonomy.L0}, {"L0", autonomy.L0}, {"L3", autonomy.L3}} {
		t.Setenv("AOS_AUTONOMY_DEFAULT", ok.env)
		lvl, err := parseAutonomyDefault()
		if err != nil {
			t.Errorf("AOS_AUTONOMY_DEFAULT=%q devia passar, veio %v", ok.env, err)
		}
		if lvl != ok.quer {
			t.Errorf("AOS_AUTONOMY_DEFAULT=%q deu %s, quero %s", ok.env, lvl, ok.quer)
		}
	}
}
