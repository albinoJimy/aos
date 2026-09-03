package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-307, achado de revisão de segurança — QUEM AUTORIZA O QUE SE REIDRATA DO WORM.
//
// A medição do revisor: desde que o arranque reidrata os níveis a partir da partição
// `autonomy`, quem escreve no FICHEIRO do WORM apende um `autonomy.level_changed` com
// `actor:"op:mallory"` e `new_level:L5`, reinicia o nó, e o par passa a servir L5. Sem uma
// única assinatura — e portanto contornando o dual-control que AOS-305 acabou de instalar.
// O `EntryHash` é um SHA-256 SEM chave: `audit.VerifyStore` re-encadeia e um append
// bem-formado passa; a verificação ancorada corre até ao último checkpoint e não vê o que
// foi apendido DEPOIS.
//
// Estes testes fixam a regra nova: um registo de OPERADOR só se reidrata se trouxer a(s)
// assinatura(s) do pedido que o originou, reverificáveis contra AOS_OPERATORS e
// AOS_AUTONOMY_SETTERS — duas distintas para L4/L5. O que não verificar é SALTADO (nunca
// aplicado) e declarado no banner com o seq: abortar o arranque por causa de um registo
// entregaria um modo de tijolo permanente ao MESMO adversário que a verificação contém.

// operadoresDeRehidratacao devolve o mapa emitterID→pubkey e as privadas correspondentes,
// no molde de `operadoresParaTeste` mas sem rota: aqui o que se exercita é o ARRANQUE.
func operadoresDeRehidratacao(t *testing.T, ids ...string) (map[string]ed25519.PublicKey, map[string]ed25519.PrivateKey) {
	t.Helper()
	pubs := make(map[string]ed25519.PublicKey, len(ids))
	privs := make(map[string]ed25519.PrivateKey, len(ids))
	for _, id := range ids {
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		pubs[id], privs[id] = pub, priv
	}
	return pubs, privs
}

// provaDe assina o payload canónico de (agente, domínio, nivelAssinado, motivo) com a chave
// de `id` e devolve-o na forma SELADA. `nivelAssinado` é parâmetro separado de propósito:
// é o que permite construir uma prova cuja assinatura cobre OUTRO nível.
func provaDe(t *testing.T, privs map[string]ed25519.PrivateKey, id, agente, dominio, nivelAssinado, motivo string) autonomy.LevelChangeProof {
	t.Helper()
	payload := integration.CanonicalAutonomyPayload(agente, dominio, nivelAssinado, motivo)
	em, err := integration.SignEmitter(id, privs[id], integration.AutonomyScope, control.SignalAutonomy, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return autonomyProofFromEmitter(em)
}

// apendeAlteracao escreve um `autonomy.level_changed` DIRECTAMENTE na partição do WORM —
// exactamente o que faz quem tem escrita no ficheiro. Não passa pelo registo nem pela rota.
func apendeAlteracao(t *testing.T, worm audit.Store, ch autonomy.LevelChange) uint64 {
	t.Helper()
	if ch.At.IsZero() {
		ch.At = time.Now().UTC()
	}
	rec, err := worm.Append(context.Background(), autonomy.BuildLevelChangedRecord(ch, autonomy.DefaultAutonomyPartition))
	if err != nil {
		t.Fatal(err)
	}
	return rec.AuditSeq
}

// apendeBaseDeProvisionamento escreve a linha de base que QUALQUER arranque real escreve antes
// de um operador poder mudar o que quer que seja: o selo `config:node` do nível declarado em
// AOS_AUTONOMY_LEVELS. Sem ela o estado semeado não existe em produção — e a precedência de
// AOS-307 usa precisamente esse selo como referência para decidir se o ficheiro foi EDITADO.
func apendeBaseDeProvisionamento(t *testing.T, worm audit.Store, agente, dominio string, nivel autonomy.Level) {
	t.Helper()
	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: agente, Domain: dominio, Old: autonomy.L0, New: nivel,
		Reason: autonomyProvisionReason, Actor: autonomyProvisionActor,
	})
}

// arrancaCom corre a FASE 2 do arranque sobre um WORM já povoado, com o validador do nó
// ligado — é a linha do [Bootstrap], sem o resto do nó.
func arrancaCom(worm audit.Store, specs []autonomyLevelSpec, pubs map[string]ed25519.PublicKey, setters map[string]bool) (*autonomyWiring, error) {
	// `specs` vazias ⇒ [buildAutonomyOracle] devolve nil e `provision` é no-op — o oráculo
	// desligado. Como o que aqui se mede é a REHIDRATAÇÃO e não o provisionamento, um teste
	// que não declare níveis recebe uma entrada NEUTRA (outro par) só para o oráculo existir.
	if len(specs) == 0 {
		specs = []autonomyLevelSpec{{agent: "agt-neutro", domain: "http", level: autonomy.L1}}
	}
	w := buildAutonomyOracle(specs, autonomy.L0)
	return w, w.provision(context.Background(), worm,
		autonomy.WithRehydrateValidator(autonomyRehydrateValidator(pubs, setters)))
}

// TestAOS307_PoCDoRevisorRegistoForjadoNaoElevaNivel — O POC, agora a falhar.
//
// Um `autonomy.level_changed` apendido à mão, com um actor inventado e L5, SEM prova
// nenhuma. Antes: o nó reiniciava e servia L5. Agora: o registo é SALTADO, o banner nomeia o
// AuditSeq, e o par fica no nível que o ambiente declara.
func TestAOS307_PoCDoRevisorRegistoForjadoNaoElevaNivel(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, _ := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	// A base que qualquer nó real já tem: o ambiente declarou L1 e o arranque anterior selou-o.
	// É ela que torna o ataque realista — com o ficheiro inalterado, a precedência de AOS-307 dá
	// a vitória ao último selo de operador, que é o forjado.
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)
	seq := apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L1, New: autonomy.L5,
		Reason: "PoC do revisor: escrita directa no ficheiro do WORM",
		Actor:  "op:mallory", // um actor que o atacante escolhe; nunca assinou nada
	})

	// ANTES (o comportamento que o revisor mediu, aqui reproduzido pelo caminho SEM
	// validador): o registo forjado é reidratado e o nó passa a servir L5. Fica no teste
	// para que o vector não seja uma afirmação em prosa — e para que reintroduzi-lo, por
	// exemplo esquecendo o validador no [Bootstrap], deixe de parecer inofensivo.
	antes := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	if err := antes.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := antes.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("sem validador o PoC devia reproduzir-se (nivel = %s, esperado L5) — se ja nao reproduz, o teste deixou de medir o vector", got)
	}

	// DEPOIS: o mesmo WORM, o mesmo registo forjado, com o validador do nó. O arranque NÃO
	// aborta — abortar daria um modo de tijolo a quem escreve no ficheiro — mas o forjado é
	// SALTADO, nomeado com o seq, e o par fica no que o ambiente declara.
	w, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, pubs, setters)
	if err != nil {
		t.Fatalf("um registo forjado NAO pode impedir o arranque (seria um DoS para quem escreve no WORM): %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L1 {
		t.Fatalf("nivel = %s, quero L1 (o do ambiente) — o L5 forjado NAO pode ficar aplicado", got)
	}
	if len(w.rejeitados) != 1 || w.rejeitados[0].AuditSeq != seq || w.rejeitados[0].Actor != "op:mallory" {
		t.Fatalf("o registo forjado tem de ser DECLARADO com o seu seq=%d e actor: %+v", seq, w.rejeitados)
	}
	if !strings.Contains(w.rejeitados[0].Motivo, "prova") {
		t.Errorf("o motivo devia nomear a prova em falta: %q", w.rejeitados[0].Motivo)
	}
	linha := strings.Join(autonomyPostureBanner(w), "\n")
	for _, exigido := range []string{"SALTADO(S)", fmt.Sprintf("seq=%d", seq), "op:mallory", "FORJADO", "MIGRACAO", "NAO se conseguir confirmar"} {
		if !strings.Contains(linha, exigido) {
			t.Errorf("o banner nao contem %q — o salto ficaria silencioso:\n%s", exigido, linha)
		}
	}
}

// TestAOS307_RegistoLegadoSemProvasEMigradoSemTijolo — o cenário que o smoke do repositório
// expôs: um WORM escrito ANTES do mecanismo de provas tem um `autonomy.level_changed` legítimo
// de operador, sem provas. O nó tem de arrancar, servir o nível do ambiente para esse par, e
// dizer no banner o que saltou e como se repõe (reassinar por POST /autonomy).
func TestAOS307_RegistoLegadoSemProvasEMigradoSemTijolo(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}

	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L4)
	// O registo legado: actor real, decisão real, ZERO provas (o formato de antes).
	legado := apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L4, New: autonomy.L5,
		Reason: "smoke anterior ao mecanismo de provas", Actor: "op:jimy",
	})

	w, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L4}}, pubs, setters)
	if err != nil {
		t.Fatalf("a migracao nao pode deixar o no sem arrancar: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L4 {
		t.Fatalf("o par legado devia ficar no nivel do ambiente (L4), veio %s", got)
	}
	if len(w.rejeitados) != 1 || w.rejeitados[0].AuditSeq != legado {
		t.Fatalf("o registo legado tem de ser declarado com o seq=%d: %+v", legado, w.rejeitados)
	}
	// Reassinar por POST /autonomy repõe a decisão — e a partir daí reidrata sem salto.
	const motivo = "reassinado apos a migracao"
	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L4, New: autonomy.L5, Reason: motivo, Actor: "op:a,op:b",
		Proofs: []autonomy.LevelChangeProof{
			provaDe(t, privs, "op:a", "agt-1", "fs", "L5", motivo),
			provaDe(t, privs, "op:b", "agt-1", "fs", "L5", motivo),
		},
	})
	w2, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L4}}, pubs, setters)
	if err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("depois de reassinar, o nivel devia reidratar (L5), veio %s", got)
	}
	// O legado continua a ser saltado e declarado — reassinar nao o apaga do trilho, e nao deve.
	if len(w2.rejeitados) != 1 || w2.rejeitados[0].AuditSeq != legado {
		t.Errorf("o registo legado continua no trilho e tem de continuar declarado: %+v", w2.rejeitados)
	}
}

// TestAOS307_RegistoDeOperadorComDuasProvasReidrataEPrevalece — o caso LEGÍTIMO de
// AOS-307, que não pode regredir: duas assinaturas válidas de setters distintos, L5, e o
// WORM prevalece sobre o que AOS_AUTONOMY_LEVELS diz agora.
func TestAOS307_RegistoDeOperadorComDuasProvasReidrataEPrevalece(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}
	const motivo = "rotina madura, duas pessoas concordam"

	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)
	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L1, New: autonomy.L5,
		Reason: motivo, Actor: "op:a,op:b",
		Proofs: []autonomy.LevelChangeProof{
			provaDe(t, privs, "op:a", "agt-1", "fs", "L5", motivo),
			provaDe(t, privs, "op:b", "agt-1", "fs", "L5", motivo),
		},
	})

	w, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, pubs, setters)
	if err != nil {
		t.Fatalf("a decisao assinada de duas pessoas devia reidratar: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("nivel = %s, quero L5 (a decisao assinada no WORM prevalece sobre o ambiente)", got)
	}
	if len(w.preservedOverEnv) != 1 || !strings.Contains(w.preservedOverEnv[0], "agt-1:fs=L5(env L1") {
		t.Errorf("preservedOverEnv = %v, quero o par preservado declarado", w.preservedOverEnv)
	}
	if head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition); head != 2 { // base de provisionamento + a decisao do operador
		t.Errorf("head = %d, quero 2 (base + decisao) — reidratar nao volta a selar", head)
	}
}

// TestAOS307_UmaSoProvaNaoChegaParaL5 — a rehidratação não pode ser a porta das traseiras
// do dual-control de AOS-305: uma assinatura VÁLIDA de um setter, para L5, é recusada.
func TestAOS307_UmaSoProvaNaoChegaParaL5(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:b")
	setters := map[string]bool{"op:a": true, "op:b": true}
	const motivo = "sozinho a remover a supervisao"

	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L0, New: autonomy.L5,
		Reason: motivo, Actor: "op:a",
		Proofs: []autonomy.LevelChangeProof{provaDe(t, privs, "op:a", "agt-1", "fs", "L5", motivo)},
	})

	w, err := arrancaCom(worm, nil, pubs, setters)
	if err != nil {
		t.Fatalf("um registo que nao verifica e SALTADO, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("nivel = %s apos recusa, quero L0", got)
	}
	if len(w.rejeitados) != 1 || !strings.Contains(w.rejeitados[0].Motivo, "exigidas 2") {
		t.Fatalf("o salto devia ser declarado a dizer quantas provas faltam: %+v", w.rejeitados)
	}
}

// TestAOS307_ProvaDeQuemNaoTemAutonomySetENaoConta — a autoridade confrontada é a de
// AGORA. `op:c` assinou validamente e está em AOS_OPERATORS, mas não detém `autonomy:set`:
// a rota recusá-lo-ia mesmo para L1, e a rehidratação não pode aceitar o que a rota recusa.
func TestAOS307_ProvaDeQuemNaoTemAutonomySetENaoConta(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, privs := operadoresDeRehidratacao(t, "op:a", "op:c")
	setters := map[string]bool{"op:a": true} // op:c assina, mas não é setter
	const motivo = "assinatura valida de quem nao decide isto"

	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L0, New: autonomy.L3,
		Reason: motivo, Actor: "op:c",
		Proofs: []autonomy.LevelChangeProof{provaDe(t, privs, "op:c", "agt-1", "fs", "L3", motivo)},
	})

	w, err := arrancaCom(worm, nil, pubs, setters)
	if err != nil {
		t.Fatalf("um registo que nao verifica e SALTADO, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("nivel = %s apos recusa, quero L0", got)
	}
	if len(w.rejeitados) != 1 || !strings.Contains(w.rejeitados[0].Motivo, autonomySetCapability) {
		t.Fatalf("o salto devia nomear o direito em falta (%s): %+v", autonomySetCapability, w.rejeitados)
	}
}

// TestAOS307_AssinaturaSobreOutroNivelNaoServe — o payload canónico amarra o NÍVEL. Uma
// assinatura legítima de "L1" não pode ser reapresentada, dentro de um registo forjado,
// como se autorizasse L3.
func TestAOS307_AssinaturaSobreOutroNivelNaoServe(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, privs := operadoresDeRehidratacao(t, "op:a")
	setters := map[string]bool{"op:a": true}
	const motivo = "assinei L1 e alguem escreveu L3"

	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L0, New: autonomy.L3,
		Reason: motivo, Actor: "op:a",
		// A assinatura cobre L1; o registo diz L3.
		Proofs: []autonomy.LevelChangeProof{provaDe(t, privs, "op:a", "agt-1", "fs", "L1", motivo)},
	})

	w, err := arrancaCom(worm, nil, pubs, setters)
	if err != nil {
		t.Fatalf("um registo que nao verifica e SALTADO, nao aborta: %v", err)
	}
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("nivel = %s apos recusa, quero L0", got)
	}
	if len(w.rejeitados) != 1 {
		t.Fatalf("a assinatura sobre outro nivel devia ser declarada como salto: %+v", w.rejeitados)
	}
}

// TestAOS307_RegistoDeConfigNodeEAceiteSemProvaECedeAoAmbiente — o provisionamento não tem
// assinatura para trazer, e não precisa: pela precedência de AOS-307, um selo de
// `config:node` CEDE a AOS_AUTONOMY_LEVELS, pelo que forjá-lo não concede nada.
func TestAOS307_RegistoDeConfigNodeEAceiteSemProvaECedeAoAmbiente(t *testing.T) {
	worm := audit.NewMemStore()
	pubs, _ := operadoresDeRehidratacao(t, "op:a")
	setters := map[string]bool{"op:a": true}

	// Um "config:node" forjado a pedir L5 — o pior caso que este caminho permite.
	apendeAlteracao(t, worm, autonomy.LevelChange{
		Agent: "agt-1", Domain: "fs", Old: autonomy.L0, New: autonomy.L5,
		Reason: "provisionamento forjado a pedir o maximo", Actor: autonomyProvisionActor,
	})

	w, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L2}}, pubs, setters)
	if err != nil {
		t.Fatalf("um registo de %s sem prova devia ser ACEITE: %v", autonomyProvisionActor, err)
	}
	// E cede: o ambiente sobrepõe-se-lhe no mesmo provision, que é o que torna o caminho
	// seguro sem assinatura.
	if got := w.registry.LevelFor("agt-1", "fs"); got != autonomy.L2 {
		t.Fatalf("nivel = %s, quero L2 — um selo de %s tem de CEDER a AOS_AUTONOMY_LEVELS", got, autonomyProvisionActor)
	}
	if len(w.preservedOverEnv) != 0 {
		t.Errorf("um selo de provisionamento nao pode contar como decisao de operador preservada: %v", w.preservedOverEnv)
	}
}

// TestAOS307_PontaAPontaRotaAssinadaSobreviveAoReinicioVerificada — o circuito completo:
// `POST /autonomy` com duas assinaturas produz um registo COM prova, e o arranque seguinte
// sobre o MESMO WORM reidrata-o E verifica-o. É o teste que liga o produtor ao consumidor:
// se a forma selada divergisse do que o validador reconstrói, ele falha aqui e em mais
// lado nenhum.
func TestAOS307_PontaAPontaRotaAssinadaSobreviveAoReinicioVerificada(t *testing.T) {
	h, privs, worm := operadoresParaTeste(t)
	// A linha de base que o arranque real escreve antes de a rota existir: sem ela, o reinício
	// leria «o ambiente declara um par que nunca provisionou» e daria a vitória ao ficheiro.
	apendeBaseDeProvisionamento(t, worm, "agt-1", "fs", autonomy.L1)

	w := httptest.NewRecorder()
	h.handleAutonomySet(w, pedidoDual(t, privs, "op:a", "op:b", "agt-1", "fs", "L5", "duas pessoas, pela rota"))
	if w.Code != http.StatusOK {
		t.Fatalf("POST /autonomy devia aplicar: %d %s", w.Code, w.Body.String())
	}

	// O selo transporta as DUAS provas — sem isto o arranque não teria o que verificar.
	head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition)
	recs, err := worm.Read(context.Background(), autonomy.DefaultAutonomyPartition, head, head)
	if err != nil || len(recs) != 1 {
		t.Fatalf("ler o selo: %v (%d registos)", err, len(recs))
	}
	var provas []autonomy.LevelChangeProof
	if err := json.Unmarshal([]byte(recs[0].Obligations[0].Params[autonomy.LevelChangeProofsParam]), &provas); err != nil {
		t.Fatalf("o selo nao transporta provas descodificaveis: %v", err)
	}
	if len(provas) != 2 || provas[0].EmitterID != "op:a" || provas[1].EmitterID != "op:b" {
		t.Fatalf("provas seladas = %+v, quero op:a e op:b", provas)
	}

	// REINÍCIO: registo novo, mesmo WORM, ambiente a dizer L1, validador ligado.
	pubs := map[string]ed25519.PublicKey{
		"op:a": privs["op:a"].Public().(ed25519.PublicKey),
		"op:b": privs["op:b"].Public().(ed25519.PublicKey),
	}
	w2, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}},
		pubs, map[string]bool{"op:a": true, "op:b": true})
	if err != nil {
		t.Fatalf("o registo produzido pela rota devia reverificar no arranque: %v", err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("nivel apos reinicio = %s, quero L5", got)
	}

	// E o CONTROLO negativo no mesmo circuito: retirar op:b da lista de setters invalida o
	// registo — a autoridade é a de agora, não a de então. O registo é saltado (não aborta) e
	// o par cai para o ambiente.
	w3, err := arrancaCom(worm, []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, pubs, map[string]bool{"op:a": true})
	if err != nil {
		t.Fatalf("saltar nao e abortar: %v", err)
	}
	if got := w3.registry.LevelFor("agt-1", "fs"); got != autonomy.L1 || len(w3.rejeitados) != 1 {
		t.Fatalf("com op:b fora de AOS_AUTONOMY_SETTERS o registo L5 devia ser SALTADO e o par ficar em L1: nivel=%s rejeitados=%+v", got, w3.rejeitados)
	}
}
