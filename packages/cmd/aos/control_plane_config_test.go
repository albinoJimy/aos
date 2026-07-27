package main

// AOS-193 — CAMINHO DE CONFIGURAÇÃO DO PLANO DE CONTROLO (achado ORF-02 + metade de STR-04).
//
// Este ficheiro prova as quatro propriedades do ticket:
//
//  1. PARSING fail-closed de AOS_OPERATORS (env, padrão AOS_BOARD_REGIONS) e de
//     AOS_APPROVERS_FILE (ficheiro JSON montado, padrão ADR-017 ponto 2);
//  2. o composition-root RECUSA entradas que os registos subjacentes descartariam em silêncio;
//  3. PROVA POSITIVA fim-a-fim: um nó configurado SÓ por ambiente (AOS_OPERATORS) aceita um
//     `aos steer` REAL, assinado pela CLI com a chave privada do operador, através do handler
//     HTTP REAL — o caminho que antes devolvia sempre 403 (ErrUnknownEmitter);
//  4. PROVA NEGATIVA: o bind-guardrail DISCRIMINA — dois nós saídos do MESMO Bootstrap, iguais
//     em tudo excepto nos operadores, obtêm respostas OPOSTAS ao mesmo bind 0.0.0.0.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	integration "github.com/aos-ref/integration"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
)

// ---------------------------------------------------------------------------
// (1) AOS_OPERATORS — parsing fail-closed
// ---------------------------------------------------------------------------

// tnPubHex devolve uma pubkey ed25519 fresca em hex (o formato de AOS_OPERATORS) e a privada
// correspondente. A privada NUNCA é escrita em config/log — só assina no lado do operador.
func tnPubHex(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return hex.EncodeToString(pub), priv
}

// TestParseOperatorsFailClosed cobre a gramática e a disciplina fail-closed de AOS_OPERATORS.
// Cada caso INVÁLIDO tem de ABORTAR (ErrBadOperators) e não "registar os que der": um
// operador silenciosamente descartado dá um nó que arranca a anunciar um canal de controlo e
// recusa TODOS os sinais desse operador com ErrUnknownEmitter.
func TestParseOperatorsFailClosed(t *testing.T) {
	pubA, _ := tnPubHex(t)
	pubB, _ := tnPubHex(t)

	t.Run("nao configurado ⇒ nil sem erro (default-deny deliberado)", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "\t"} {
			ops, err := parseOperators(raw)
			if err != nil || ops != nil {
				t.Fatalf("parseOperators(%q) = (%v,%v); quer (nil,nil)", raw, ops, err)
			}
		}
	})

	t.Run("entradas validas ⇒ mapa id→pubkey", func(t *testing.T) {
		ops, err := parseOperators("human:alice=" + pubA + ", svc:deployer=" + pubB + ",")
		if err != nil {
			t.Fatalf("parseOperators valido: %v", err)
		}
		if len(ops) != 2 {
			t.Fatalf("esperava 2 operadores, veio %d", len(ops))
		}
		if hex.EncodeToString(ops["human:alice"]) != pubA || hex.EncodeToString(ops["svc:deployer"]) != pubB {
			t.Fatalf("pubkeys mal mapeadas: %v", ops)
		}
	})

	// SEED != PUBKEY é INDISTINGUÍVEL estruturalmente (ambas 32 bytes) — mas uma chave
	// PRIVADA COMPLETA ed25519 (64 bytes / 128 hex) É rejeitada. É a fronteira honesta do que
	// a validação consegue impor; está documentada no README do nó.
	privWhole := hex.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))

	invalid := map[string]string{
		"entrada sem '='":       "human:alice",
		"emitterID vazio":       "=" + pubA,
		"pubkey vazia":          "human:alice=",
		"pubkey nao-hex":        "human:alice=zz" + pubA[2:],
		"pubkey curta":          "human:alice=" + pubA[:62],
		"chave privada inteira": "human:alice=" + privWhole,
		"emitterID duplicado":   "human:alice=" + pubA + ",human:alice=" + pubB,
		"so segmentos vazios":   ",, ,",
		// ATRIBUIÇÃO: dois emitterIDs com a MESMA pubkey arrancariam e um
		// `aos steer --emitter svc:deployer` assinado pela chave de human:alice seria ACEITE e
		// selado no WORM como sendo de svc:deployer — o nome do emissor deixaria de ser
		// evidência. É o conflito de autoridade do emitterID duplicado, invertido.
		"pubkey partilhada por dois emitterIDs": "human:alice=" + pubA + ",svc:deployer=" + pubA,
	}
	for name, raw := range invalid {
		t.Run(name, func(t *testing.T) {
			ops, err := parseOperators(raw)
			if !errors.Is(err, ErrBadOperators) {
				t.Fatalf("parseOperators(%q) devia abortar com ErrBadOperators, veio (%v,%v)", raw, ops, err)
			}
			if ops != nil {
				t.Fatalf("parseOperators invalido NAO pode devolver config parcial, veio %v", ops)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// (1b) AOS_APPROVERS_FILE — parsing fail-closed do ficheiro montado
// ---------------------------------------------------------------------------

// writeApproversFile escreve conteúdo bruto num ficheiro temporário e devolve o caminho.
func writeApproversFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approvers.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestParseApproversFileFailClosed prova que o caminho de configuração RICO (principal +
// pubkey + autoridade) é tão fail-closed como o das pubkeys de operador: qualquer ficheiro
// configurado-mas-inválido ABORTA em vez de deixar o FourEyesGate silenciosamente DESLIGADO
// (que é como POST /runs/{id}/approve voltaria a responder 501).
func TestParseApproversFileFailClosed(t *testing.T) {
	pubA, _ := tnPubHex(t)
	pubB, _ := tnPubHex(t)

	t.Run("nao configurado ⇒ nil sem erro (four-eyes desligado por declaracao)", func(t *testing.T) {
		aps, err := parseApproversFile("")
		if err != nil || aps != nil {
			t.Fatalf("parseApproversFile(\"\") = (%v,%v); quer (nil,nil)", aps, err)
		}
	})

	t.Run("ficheiro valido ⇒ roster completo", func(t *testing.T) {
		path := writeApproversFile(t, `{"approvers":[
			{"principal":"human:alice","pubkey":"`+pubA+`","authority":["approve:danger","approve:gray"]},
			{"principal":"human:bob","pubkey":"`+pubB+`","authority":["approve:danger"]}
		]}`)
		aps, err := parseApproversFile(path)
		if err != nil {
			t.Fatalf("parseApproversFile valido: %v", err)
		}
		if len(aps) != 2 || aps[0].Principal != "human:alice" || len(aps[0].Authority) != 2 {
			t.Fatalf("roster mal lido: %+v", aps)
		}
		if hex.EncodeToString(aps[1].PubKey) != pubB {
			t.Fatalf("pubkey do 2.o aprovador mal lida")
		}
	})

	invalid := map[string]string{
		"json malformado":     `{"approvers":[`,
		"campo desconhecido":  `{"approvers":[{"principal":"a","pub_key":"` + pubA + `","authority":["approve:danger"]}]}`,
		"lista vazia":         `{"approvers":[]}`,
		"principal vazio":     `{"approvers":[{"principal":"  ","pubkey":"` + pubA + `","authority":["approve:danger"]}]}`,
		"pubkey invalida":     `{"approvers":[{"principal":"a","pubkey":"nao-hex","authority":["approve:danger"]}]}`,
		"autoridade ausente":  `{"approvers":[{"principal":"a","pubkey":"` + pubA + `"}]}`,
		"autoridade so vazia": `{"approvers":[{"principal":"a","pubkey":"` + pubA + `","authority":["  "]}]}`,
		"principal duplicado": `{"approvers":[{"principal":"a","pubkey":"` + pubA + `","authority":["approve:danger"]},` +
			`{"principal":"a","pubkey":"` + pubB + `","authority":["approve:gray"]}]}`,
		// BYPASS DO 4-EYES (o caso que o ficheiro montado torna alcançável por copy-paste): dois
		// principals DISTINTOS com a MESMA pubkey. A distinção de authorizeDual é sobre
		// approver/session/credential — TRÊS strings escolhidas pelo CLIENTE na perna —, pelo que
		// a pubkey pinada é a ÚNICA âncora criptográfica de "duas pessoas": partilhada, UMA chave
		// privada assina as duas pernas e o dual-control é anulado em silêncio.
		"pubkey partilhada por dois principals": `{"approvers":[{"principal":"human:alice","pubkey":"` + pubA + `","authority":["approve:danger"]},` +
			`{"principal":"human:bob","pubkey":"` + pubA + `","authority":["approve:danger"]}]}`,
		// VOCABULÁRIO FECHADO: hasAuthority compara string EXACTA e hitl.RequiredAuthority só
		// produz approve:{safe,gray,danger}. Um typo é fail-closed mas SILENCIOSO — um aprovador
		// contado no banner que nunca aprova nada.
		"capability com typo": `{"approvers":[{"principal":"a","pubkey":"` + pubA + `","authority":["approve:dangerous"]}]}`,
		"capability wildcard": `{"approvers":[{"principal":"a","pubkey":"` + pubA + `","authority":["approve:*"]}]}`,
		"capability fora do eixo": `{"approvers":[{"principal":"a","pubkey":"` + pubA + `",` +
			`"authority":["approve:danger","admin:tudo"]}]}`,
	}
	for name, body := range invalid {
		t.Run(name, func(t *testing.T) {
			aps, err := parseApproversFile(writeApproversFile(t, body))
			if !errors.Is(err, ErrBadApproversFile) {
				t.Fatalf("parseApproversFile devia abortar com ErrBadApproversFile, veio (%v,%v)", aps, err)
			}
			if aps != nil {
				t.Fatalf("parseApproversFile invalido NAO pode devolver roster parcial, veio %+v", aps)
			}
		})
	}

	t.Run("ficheiro inexistente ⇒ aborta", func(t *testing.T) {
		_, err := parseApproversFile(filepath.Join(t.TempDir(), "nao-existe.json"))
		if !errors.Is(err, ErrBadApproversFile) {
			t.Fatalf("ficheiro inexistente devia abortar com ErrBadApproversFile, veio %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// (1c) A costura ambiente → Config (a que faltava: ORF-02)
// ---------------------------------------------------------------------------

// TestNodeConfigFromEnvFillsControlPlane é a asserção CENTRAL do achado ORF-02: os campos
// Config.Operators e Config.Approvers passam a ser ESCRITOS por [nodeConfigFromEnv]. Antes
// deste ticket nenhum caminho de leitura os preenchia e — porque Config vive em `package
// main` — registar uma pubkey exigia forkar e recompilar.
func TestNodeConfigFromEnvFillsControlPlane(t *testing.T) {
	pubOp, _ := tnPubHex(t)
	pubAp, _ := tnPubHex(t)
	approversPath := writeApproversFile(t,
		`{"approvers":[{"principal":"human:alice","pubkey":"`+pubAp+`","authority":["approve:danger"]}]}`)

	t.Setenv("AOS_OPERATORS", "human:alice="+pubOp)
	t.Setenv("AOS_APPROVERS_FILE", approversPath)

	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	if hex.EncodeToString(cfg.Operators["human:alice"]) != pubOp {
		t.Fatalf("Config.Operators nao foi preenchida a partir de AOS_OPERATORS: %v", cfg.Operators)
	}
	if len(cfg.Approvers) != 1 || cfg.Approvers[0].Principal != "human:alice" {
		t.Fatalf("Config.Approvers nao foi preenchida a partir de AOS_APPROVERS_FILE: %+v", cfg.Approvers)
	}
}

// TestNodeConfigFromEnvControlPlaneAborts prova que a fronteira de ambiente é fail-closed: um
// valor presente mas inválido ABORTA o arranque (não degrada para um canal de controlo mudo).
func TestNodeConfigFromEnvControlPlaneAborts(t *testing.T) {
	t.Run("AOS_OPERATORS malformada", func(t *testing.T) {
		t.Setenv("AOS_OPERATORS", "human:alice")
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadOperators) {
			t.Fatalf("devia abortar com ErrBadOperators, veio %v", err)
		}
		// E o arranque REAL (o entrypoint) propaga-o.
		if err := run(io.Discard); !errors.Is(err, ErrBadOperators) {
			t.Fatalf("run() devia abortar com ErrBadOperators, veio %v", err)
		}
	})
	t.Run("AOS_APPROVERS_FILE invalido", func(t *testing.T) {
		t.Setenv("AOS_APPROVERS_FILE", writeApproversFile(t, `{"approvers":[]}`))
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadApproversFile) {
			t.Fatalf("devia abortar com ErrBadApproversFile, veio %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// (2) O composition-root recusa entradas que os registos descartariam em silêncio
// ---------------------------------------------------------------------------

// TestBootstrapRejectsSilentlyDiscardedControlEntries prova a guarda (1a) de [Bootstrap]:
// [integration.Ed25519Authenticator.Register] DESCARTA sem se queixar uma pubkey de tamanho
// errado e [hitl.MemApproverRegistry.Register] aceita um roster inútil. O nó recusa ambos, de
// modo que a cardinalidade que o bind-guardrail lê (EmitterCount) nunca mente.
func TestBootstrapRejectsSilentlyDiscardedControlEntries(t *testing.T) {
	pub, _ := tnPubHex(t)
	good, _ := hex.DecodeString(pub)

	other, _ := tnPubHex(t)
	otherRaw, _ := hex.DecodeString(other)

	cases := map[string]func(*Config){
		"operador com emitterID vazio": func(c *Config) {
			c.Operators = map[string]ed25519.PublicKey{"": ed25519.PublicKey(good)}
		},
		"operador com pubkey truncada": func(c *Config) {
			c.Operators = map[string]ed25519.PublicKey{"human:alice": ed25519.PublicKey(good[:16])}
		},
		// A colisão de chave NÃO é vista por nenhum registo subjacente (o Register aceita as
		// duas), e Config é alcançável IN-PROCESS — a guarda tem de estar aqui, não só no parser.
		"dois operadores com a MESMA pubkey": func(c *Config) {
			c.Operators = map[string]ed25519.PublicKey{
				"ops:alice": ed25519.PublicKey(good),
				"ops:bob":   ed25519.PublicKey(good),
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := tnBaseConfig()
			mutate(&cfg)
			node, err := Bootstrap(context.Background(), cfg, io.Discard)
			if !errors.Is(err, ErrBadOperatorEntry) {
				if node != nil {
					_ = node.Close()
				}
				t.Fatalf("Bootstrap devia abortar com ErrBadOperatorEntry, veio %v", err)
			}
		})
	}

	approverCases := map[string][]ApproverConfig{
		"principal vazio":  {{Principal: "", PubKey: ed25519.PublicKey(good), Authority: []string{"approve:danger"}}},
		"pubkey truncada":  {{Principal: "human:alice", PubKey: ed25519.PublicKey(good[:16]), Authority: []string{"approve:danger"}}},
		"autoridade vazia": {{Principal: "human:alice", PubKey: ed25519.PublicKey(good)}},
		"capability fora do vocabulario": {
			{Principal: "human:alice", PubKey: ed25519.PublicKey(good), Authority: []string{"approve:dangerous"}},
		},
		// hitl.MemApproverRegistry.Register SOBREPÕE em silêncio: "o último ganha" seria uma
		// escolha de autoridade feita por acidente de ordenação.
		"principal duplicado": {
			{Principal: "human:alice", PubKey: ed25519.PublicKey(good), Authority: []string{"approve:danger"}},
			{Principal: "human:alice", PubKey: ed25519.PublicKey(otherRaw), Authority: []string{"approve:gray"}},
		},
		// O BYPASS DO DUAL-CONTROL pela via IN-PROCESS: dois principals, UMA chave privada.
		"dois principals com a MESMA pubkey": {
			{Principal: "human:alice", PubKey: ed25519.PublicKey(good), Authority: []string{"approve:danger"}},
			{Principal: "human:bob", PubKey: ed25519.PublicKey(good), Authority: []string{"approve:danger"}},
		},
	}
	for name, ap := range approverCases {
		t.Run(name, func(t *testing.T) {
			cfg := tnBaseConfig()
			cfg.Approvers = ap
			node, err := Bootstrap(context.Background(), cfg, io.Discard)
			if !errors.Is(err, ErrBadApproverEntry) {
				if node != nil {
					_ = node.Close()
				}
				t.Fatalf("Bootstrap devia abortar com ErrBadApproverEntry, veio %v", err)
			}
		})
	}
}

// TestBootstrapComposesFourEyesFromConfig prova que o caminho de configuração TORNA COMPONÍVEL
// o FourEyesGate: com aprovadores lidos do ficheiro, node.FourEyes deixa de ser nil e o
// endpoint POST /runs/{id}/approve deixa de responder 501 (passa a JULGAR o pedido — um pedido
// sem pernas válidas é 403, que é o fail-closed correcto, não "não implementado").
func TestBootstrapComposesFourEyesFromConfig(t *testing.T) {
	pubAp, _ := tnPubHex(t)
	path := writeApproversFile(t,
		`{"approvers":[{"principal":"human:alice","pubkey":"`+pubAp+`","authority":["approve:danger"]}]}`)
	approvers, err := parseApproversFile(path)
	if err != nil {
		t.Fatalf("parseApproversFile: %v", err)
	}

	// SEM aprovadores ⇒ gate não composto ⇒ 501 (o estado do binário entregue antes de AOS-193).
	bare, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = bare.Close() }()
	if bare.FourEyes != nil {
		t.Fatal("sem aprovadores o four-eyes NAO devia estar composto")
	}
	_, hBare := newAPI(t, bare)
	if rec := postJSON(hBare, "POST", "/runs/r/approve", map[string]any{
		"request": map[string]any{"request_id": "req-1"}, "legs": []any{},
	}); rec.Code != http.StatusNotImplemented {
		t.Fatalf("sem aprovadores o approve devia dar 501, veio %d", rec.Code)
	}

	// COM aprovadores vindos do ficheiro ⇒ gate composto ⇒ o endpoint JULGA (403), não 501.
	cfg := tnBaseConfig()
	cfg.SteerClock = tnClock()
	cfg.Approvers = approvers
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap com aprovadores: %v", err)
	}
	defer func() { _ = node.Close() }()
	if node.FourEyes == nil {
		t.Fatal("com aprovadores configurados o FourEyesGate TEM de estar composto")
	}
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs/r/approve", map[string]any{
		"request": map[string]any{"request_id": "req-1"}, "legs": []any{},
	})
	if rec.Code == http.StatusNotImplemented {
		t.Fatal("com aprovadores o approve NAO pode continuar a responder 501 (endpoint inatingivel)")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("approve sem pernas validas devia dar 403 (fail-closed), veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (3) PROVA POSITIVA — nó configurado SÓ por ambiente aceita um `aos steer` real
// ---------------------------------------------------------------------------

// TestEnvConfiguredOperatorSteerAcceptedEndToEnd é a PROVA POSITIVA do ticket, sem atalhos:
//
//	seed do operador em ficheiro (máquina do operador)
//	  → `aos operator-pubkey` deriva a PUBKEY
//	    → AOS_OPERATORS (a ÚNICA superfície de config do binário)
//	      → nodeConfigFromEnv → Bootstrap → handler HTTP REAL (httptest)
//	        → `aos steer` (CLI real: assina, serializa, faz POST)
//	          → Ed25519Authenticator REAL → SteerChannel REAL
//
// Nada é injectado à mão: a pubkey entra pelo mesmo caminho que um operador usaria num
// contentor. NÃO-VACUOSO: sem o caminho de configuração de AOS-193 o authenticator teria ZERO
// emissores e este steer levaria 403 (ErrUnknownEmitter) — é o que a variante de controlo
// (AOS_OPERATORS ausente) assere no fim.
func TestEnvConfiguredOperatorSteerAcceptedEndToEnd(t *testing.T) {
	const emitterID = "human:alice"
	const runID = "run-aos193-positivo"

	// (i) a chave do OPERADOR vive num ficheiro do operador — nunca no nó.
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("rand seed: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "operator.seed")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatalf("WriteFile seed: %v", err)
	}

	// (ii) a CLI deriva a entrada "emitterID=hexpubkey" pronta para AOS_OPERATORS.
	var out strings.Builder
	if err := dispatch([]string{"operator-pubkey", "--key", keyPath, "--emitter", emitterID}, &out); err != nil {
		t.Fatalf("aos operator-pubkey: %v", err)
	}
	entry := strings.TrimSpace(out.String())
	if !strings.HasPrefix(entry, emitterID+"=") || len(entry) != len(emitterID)+1+64 {
		t.Fatalf("operator-pubkey devia imprimir emitterID=<64 hex>, veio %q", entry)
	}
	// A PRIVADA nunca sai: o que a CLI imprime é a pubkey derivada, não a seed.
	if strings.Contains(entry, hex.EncodeToString(seed)) {
		t.Fatal("operator-pubkey NUNCA pode imprimir material privado")
	}

	// (iii) configuração do NÓ exclusivamente por ambiente.
	t.Setenv("AOS_OPERATORS", entry)
	t.Setenv("AOS_BOARD_REGIONS", "") // read-path legado: este teste é sobre o plano de CONTROLO
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv: %v", err)
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()
	if got := node.SteerAuth.EmitterCount(); got != 1 {
		t.Fatalf("o no devia ter 1 operador registado a partir do ambiente, tem %d", got)
	}

	// (iv) API REAL sobre o nó real.
	_, h := newAPI(t, node)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// (v) submete um run e conduz-no com a CLI REAL (assinatura ed25519 no lado do operador).
	if err := dispatch([]string{"run", "--addr", srv.URL, "--run-id", runID,
		"--objective", "prova positiva AOS-193", "--nhi", "nhi:" + runID}, io.Discard); err != nil {
		t.Fatalf("aos run: %v", err)
	}
	const correction = "aperta o ambito ao ticket"
	if err := dispatch([]string{"steer", "--addr", srv.URL, "--run-id", runID,
		"--emitter", emitterID, "--key", keyPath, "--correction", correction}, io.Discard); err != nil {
		t.Fatalf("aos steer com operador configurado por AOS_OPERATORS devia ser ACEITE, veio: %v", err)
	}

	// (vi) EFEITO REAL: a correcção chegou ao SteerChannel (não só um 202 de cortesia).
	got, ok := node.Steer.PendingCorrection(runID)
	if !ok || string(got) != correction {
		t.Fatalf("correccao pendente = (%q,%v); quer (%q,true)", got, ok, correction)
	}

	// (vii) CONTROLO (não-vacuosidade): o MESMO steer contra um nó SEM AOS_OPERATORS — o
	// binário tal como era antes de AOS-193 — é RECUSADO (403 / ErrUnknownEmitter).
	t.Setenv("AOS_OPERATORS", "")
	bareCfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv (sem operadores): %v", err)
	}
	if len(bareCfg.Operators) != 0 {
		t.Fatalf("sem AOS_OPERATORS a config devia ficar sem operadores, veio %v", bareCfg.Operators)
	}
	bare, err := Bootstrap(context.Background(), bareCfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (sem operadores): %v", err)
	}
	defer func() { _ = bare.Close() }()
	_, hBare := newAPI(t, bare)
	bareSrv := httptest.NewServer(hBare)
	defer bareSrv.Close()
	err = dispatch([]string{"steer", "--addr", bareSrv.URL, "--run-id", runID,
		"--emitter", emitterID, "--key", keyPath, "--correction", correction}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("um no SEM operadores tem de RECUSAR o mesmo steer com 403, veio %v", err)
	}
}

// TestApproversFileAuthorizesDualControlEndToEnd é a PROVA POSITIVA do caminho dos APROVADORES
// — a contraparte de [TestEnvConfiguredOperatorSteerAcceptedEndToEnd] para o four-eyes. Sem ela
// a rede de regressão do /approve era ASSIMÉTRICA: todas as asserções existentes sobre o roster
// vindo de ficheiro esperavam 403/501, e um bug que tornasse o endpoint PERMANENTEMENTE negador
// (ex.: pubkey pinada mal lida, ou `authority` descartada) passaria em todas elas — exactamente
// a assimetria que tornou o CA de AOS-166 uma sobre-reivindicação.
//
//	approvers.json (ficheiro montado) → parseApproversFile → Config.Approvers → Bootstrap
//	  → FourEyesGate REAL → handler HTTP REAL → 200 + os DOIS aprovadores na decisão
//
// Exercita o caminho DUAL (dual_control_required=true), que é o que o roster de ficheiro existe
// para servir, com DUAS chaves privadas distintas — a única forma legítima de o satisfazer
// desde que a colisão de pubkey passou a ABORTAR (ver o caso "pubkey partilhada por dois
// principals" em TestParseApproversFileFailClosed).
//
// ARMADILHA DE ENCODING fixada aqui de propósito: `risk.ClassDanger` é o VALOR-ZERO (0) e
// `risk.ClassGray` é 2 — o wire `risk_class` é o valor NUMÉRICO, pelo que "0" significa
// "danger" (fail-closed), não "não classificado".
func TestApproversFileAuthorizesDualControlEndToEnd(t *testing.T) {
	pubA, privA := tnPubHex(t)
	pubB, privB := tnPubHex(t)
	const alice, bob = "human:alice", "human:bob"

	path := writeApproversFile(t, `{"approvers":[
		{"principal":"`+alice+`","pubkey":"`+pubA+`","authority":["approve:danger"]},
		{"principal":"`+bob+`","pubkey":"`+pubB+`","authority":["approve:danger","approve:gray"]}
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
		t.Fatalf("Bootstrap com o roster do ficheiro: %v", err)
	}
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	req := integration.FourEyesRequest{
		RequestID:           "req-aos193-dual",
		Preview:             []byte("apagar o bucket de producao"),
		RiskClass:           risk.ClassDanger,
		DualControlRequired: true,
	}
	chalA, chalB := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(chalA); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(chalB); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// Cada perna é assinada NO LADO DO APROVADOR (a API é non-signing) pela chave privada
	// correspondente à pubkey PINADA no ficheiro.
	legA := integration.SignFourEyesLeg(privA, req, alice, "sess-a", "cred-a", chalA, nil)
	legB := integration.SignFourEyesLeg(privB, req, bob, "sess-b", "cred-b", chalB, nil)

	wire := func(leg integration.ApprovalLeg) map[string]any {
		return map[string]any{
			"approver":   leg.Approver,
			"session":    leg.Session,
			"credential": leg.Credential,
			"challenge":  base64.StdEncoding.EncodeToString(leg.Challenge),
			"signature":  base64.StdEncoding.EncodeToString(leg.Signature),
		}
	}
	body := map[string]any{
		"request": map[string]any{
			"request_id":            req.RequestID,
			"preview":               base64.StdEncoding.EncodeToString(req.Preview),
			"risk_class":            uint8(req.RiskClass), // 0 == danger (valor-zero fail-closed)
			"dual_control_required": true,
		},
		"legs": []map[string]any{wire(legA), wire(legB)},
	}

	rec := postJSON(h, "POST", "/runs/run-aos193/approve", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("o roster vindo de AOS_APPROVERS_FILE tem de conseguir AUTORIZAR (200), veio %d (%s)",
			rec.Code, rec.Body.String())
	}
	var got struct {
		Status    string   `json:"status"`
		Approvers []string `json:"approvers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta ilegivel: %v (%s)", err, rec.Body.String())
	}
	if got.Status != "authorized" || len(got.Approvers) != 2 {
		t.Fatalf("a decisao devia nomear os DOIS aprovadores autenticados, veio %+v", got)
	}

	// NÃO-VACUOSIDADE (a rede que faltava, do outro lado): a MESMA cerimónia com a perna de
	// `bob` assinada pela chave de `alice` — a assinatura cruzada que a pubkey pinada existe
	// para apanhar — é RECUSADA. Sem isto, um 200 acima poderia vir de um gate que não verifica.
	// Challenges FRESCOS: reutilizar os anteriores faria a recusa vir do anti-replay e a
	// asserção seria vácua. Assim, a ÚNICA diferença para a cerimónia que deu 200 é a chave
	// que assinou a 2.ª perna.
	chalA2, chalB2 := make([]byte, 32), make([]byte, 32)
	if _, err := rand.Read(chalA2); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if _, err := rand.Read(chalB2); err != nil {
		t.Fatalf("rand: %v", err)
	}
	forged := integration.SignFourEyesLeg(privA, req, bob, "sess-b2", "cred-b2", chalB2, nil)
	bodyForged := map[string]any{
		"request": body["request"],
		"legs": []map[string]any{
			wire(integration.SignFourEyesLeg(privA, req, alice, "sess-a2", "cred-a2", chalA2, nil)),
			wire(forged),
		},
	}
	if rec := postJSON(h, "POST", "/runs/run-aos193/approve", bodyForged); rec.Code != http.StatusForbidden {
		t.Fatalf("uma perna assinada pela chave ERRADA tem de dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// (4) PROVA NEGATIVA — o bind-guardrail passa a DISCRIMINAR (STR-04)
// ---------------------------------------------------------------------------

// TestBindGuardrailRequiresAtLeastOneOperator é a PROVA NEGATIVA e, ao mesmo tempo, a prova de
// que a condição deixou de ser vácua. Dois nós REAIS saídos do MESMO [Bootstrap], idênticos em
// tudo excepto no registo de operadores:
//
//   - o PREDICADO ANTIGO (SteerAuth != nil ∧ IdentityMode real) é VERDADEIRO nos DOIS —
//     é o que o teste assere explicitamente, e é a definição de vácuo: nenhum nó saído do
//     composition-root o podia falsificar (os testes existentes só o conseguiam mutando
//     `node.SteerAuth = nil` à mão);
//   - o PREDICADO NOVO separa-os: 0 operadores ⇒ bind a 0.0.0.0 RECUSADO
//     (ErrRefuseNonLoopbackBind, sem Listen); 1 operador ⇒ o MESMO bind é permitido.
func TestBindGuardrailRequiresAtLeastOneOperator(t *testing.T) {
	// (a) nó SEM operadores — o que hoje faria bind a 0.0.0.0 sem plano de controlo utilizável.
	bare, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = bare.Close() }()
	// (b) nó COM um operador — a única diferença.
	withOp, _ := newAPINode(t, &countingModel{}, true)
	defer func() { _ = withOp.Close() }()

	// O PREDICADO ANTIGO não distingue os dois (era identicamente verdadeiro).
	for name, n := range map[string]*Node{"sem operadores": bare, "com operador": withOp} {
		oldPredicate := n.SteerAuth != nil &&
			(n.IdentityMode == IdentityModeReal || n.IdentityMode == IdentityModeRealHardened)
		if !oldPredicate {
			t.Fatalf("%s: o predicado ANTIGO devia ser verdadeiro (e por isso vacuo) — veio falso", name)
		}
	}
	if bare.SteerAuth.EmitterCount() != 0 || withOp.SteerAuth.EmitterCount() != 1 {
		t.Fatalf("cardinalidades inesperadas: bare=%d withOp=%d",
			bare.SteerAuth.EmitterCount(), withOp.SteerAuth.EmitterCount())
	}

	svcBare, _ := newAPI(t, bare)
	srvBare, err := NewAPIServer(svcBare, bare)
	if err != nil {
		t.Fatalf("NewAPIServer (bare): %v", err)
	}
	if srvBare.controlAuthenticated() {
		t.Fatal("um no com ZERO operadores NAO pode contar como canal de controlo autenticado")
	}
	// PROVA NEGATIVA: bind não-loopback RECUSADO, sem sequer abrir o socket.
	if err := srvBare.Serve("0.0.0.0:0"); !errors.Is(err, ErrRefuseNonLoopbackBind) {
		t.Fatalf("bind 0.0.0.0 com ZERO operadores devia dar ErrRefuseNonLoopbackBind, veio %v", err)
	}
	// O loopback continua permitido (o canal de controlo só é alcançável do próprio host).
	ln, err := srvBare.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("loopback devia continuar permitido sem operadores, veio %v", err)
	}
	_ = ln.Close()

	// CONTRAPROVA: um operador registado ⇒ o MESMO bind não-loopback é permitido. Declara-se a
	// terminação TLS a montante (AOS-209) para ISOLAR o eixo dos operadores: sem isso, a quarta
	// conjunção do bind-guardrail (transporte cifrado) recusaria o bind em texto-claro e o teste
	// deixaria de medir o discriminante que lhe interessa (operadores).
	svcOp, _ := newAPI(t, withOp)
	srvOp, err := NewAPIServer(svcOp, withOp, WithExternalTLSTermination(true))
	if err != nil {
		t.Fatalf("NewAPIServer (withOp): %v", err)
	}
	if !srvOp.controlAuthenticated() {
		t.Fatal("um no com identidade real + 1 operador TEM de contar como autenticado")
	}
	lnOp, err := srvOp.listen("0.0.0.0:0")
	if err != nil {
		t.Fatalf("bind 0.0.0.0 com 1 operador devia ser PERMITIDO, veio %v", err)
	}
	_ = lnOp.Close()
}

// TestServeAPIRefusesNonLoopbackWithoutOperators leva a prova negativa ao CAMINHO DE PRODUÇÃO
// (serveAPI — a função que o entrypoint invoca quando AOS_API_ADDR está definido), sobre um nó
// INTACTO (nada é mutado à mão): a recusa vem só de não haver operadores configurados.
func TestServeAPIRefusesNonLoopbackWithoutOperators(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()

	err := serveAPI(context.Background(), io.Discard, node, "0.0.0.0:0")
	if !errors.Is(err, ErrRefuseNonLoopbackBind) {
		t.Fatalf("serveAPI a 0.0.0.0 sem operadores devia RECUSAR com ErrRefuseNonLoopbackBind, veio %v", err)
	}
}

// TestBootstrapBannerDeclaresControlPlaneState prova que o banner não deixa o operador
// adivinhar: com zero operadores DECLARA que steer/pause serão recusados e que o bind
// não-loopback é recusado; com operadores declara a cardinalidade REAL (lida do authenticator).
func TestBootstrapBannerDeclaresControlPlaneState(t *testing.T) {
	pub, _ := tnPubHex(t)
	raw, _ := hex.DecodeString(pub)

	var bare strings.Builder
	n1, err := Bootstrap(context.Background(), tnBaseConfig(), &bare)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = n1.Close() }()
	if !strings.Contains(bare.String(), "SEM OPERADORES") || !strings.Contains(bare.String(), "AOS_OPERATORS") {
		t.Fatalf("o banner devia declarar o canal de controlo SEM OPERADORES, veio:\n%s", bare.String())
	}
	if !strings.Contains(bare.String(), "four-eyes gate (AOS-162): DESLIGADO") {
		t.Fatalf("o banner devia declarar o four-eyes DESLIGADO, veio:\n%s", bare.String())
	}

	cfg := tnBaseConfig()
	cfg.Operators = map[string]ed25519.PublicKey{"human:alice": ed25519.PublicKey(raw)}
	var withOp strings.Builder
	n2, err := Bootstrap(context.Background(), cfg, &withOp)
	if err != nil {
		t.Fatalf("Bootstrap (com operador): %v", err)
	}
	defer func() { _ = n2.Close() }()
	if !strings.Contains(withOp.String(), "1 operador(es) registado(s) via AOS_OPERATORS") {
		t.Fatalf("o banner devia declarar 1 operador registado, veio:\n%s", withOp.String())
	}
}

// TestApproversFileHoldsNoPrivateMaterial é uma guarda de INVARIANTE do projecto: o esquema do
// ficheiro de aprovadores só tem campos públicos. Um documento que traga um campo com material
// privado é rejeitado pelo DisallowUnknownFields — nunca ignorado em silêncio.
func TestApproversFileHoldsNoPrivateMaterial(t *testing.T) {
	pub, _ := tnPubHex(t)
	body := `{"approvers":[{"principal":"human:alice","pubkey":"` + pub + `",` +
		`"authority":["approve:danger"],"private_key":"deadbeef"}]}`
	if _, err := parseApproversFile(writeApproversFile(t, body)); !errors.Is(err, ErrBadApproversFile) {
		t.Fatalf("um campo de material privado tem de ABORTAR, veio %v", err)
	}
	// E o esquema declarado não tem, de todo, onde o guardar.
	blob, err := json.Marshal(approversDoc{Approvers: []approverDoc{{
		Principal: "human:alice", PubKey: pub, Authority: []string{"approve:danger"},
	}}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, forbidden := range []string{"private", "seed", "secret"} {
		if strings.Contains(strings.ToLower(string(blob)), forbidden) {
			t.Fatalf("o esquema de aprovadores nao pode ter campo %q: %s", forbidden, blob)
		}
	}
}
