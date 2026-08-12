package main

// AOS-263 — A SUPERFÍCIE DO OPERADOR: `aos continue` e `aos abort` contra o NÓ REAL.
//
// A rota de decisão só vale o que valer a via por onde um humano a exerce. Estes testes fecham
// a lacuna que ficou da parte 3: a CLI produzia a assinatura e NADA provava que o nó a
// aceitava — se o tuplo canónico da CLI divergisse do que o [integration.Ed25519Authenticator]
// verifica (kind, payload, ordem dos campos), o operador só descobriria no dia em que
// precisasse de parar um run, com o run já parado à espera dele.
//
// A prova é a mesma de [TestCLI_RunSteerObserve_E2E]: servidor HTTP real, handler real do nó,
// chave privada só do lado do operador. E é NÃO-VACUOSA nos dois sentidos — a chave errada é
// recusada (403) e a certa executa, com efeito verificável no estado durável.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	integration "github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// aos263CLIServer levanta um nó REAL (four-eyes composto + operador pinado) atrás de um
// servidor HTTP, com um run já SUSPENSO por prompt de exaustão — o estado exacto em que o
// operador encontra o run quando vai responder.
//
// O relógio de frescura é o REAL (não o `tnClock` fixo dos testes de rota): a CLI carimba com
// `time.Now`, e um nó de relógio congelado recusaria tudo por stale — o que faria estes testes
// falhar por uma razão que não é a que eles provam.
func aos263CLIServer(t *testing.T, runID string) (url, keyFile string, node *Node, prompt integration.PendingRecord) {
	t.Helper()
	opPub, opPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(operador): %v", err)
	}
	apPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(aprovador): %v", err)
	}
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}
	cfg.SteerClock = nil // relógio REAL — casa com o time.Now da CLI
	cfg.Operators = map[string]ed25519.PublicKey{aos263OperatorID: opPub}
	cfg.Approvers = []ApproverConfig{{Principal: "human:alice", PubKey: apPub, Authority: []string{"approve:danger"}}}

	node, err = Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })

	prompt = aos263Suspende(t, node, runID, 4, 880, 1000)

	_, h := newAPI(t, node)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	kf := filepath.Join(t.TempDir(), "op.key")
	if err := os.WriteFile(kf, []byte(hex.EncodeToString(opPriv.Seed())), 0o600); err != nil {
		t.Fatalf("escrever seed: %v", err)
	}
	return srv.URL, kf, node, prompt
}

// TestAOS263_CLIAbortAceitePeloNo: `aos abort` produz uma decisão que o nó ACEITA, e o run
// acaba `killed` no log durável. Se a assinatura da CLI não casasse com o autenticador do nó
// (ou o payload canónico divergisse), viria 403 e o run continuaria suspenso.
func TestAOS263_CLIAbortAceitePeloNo(t *testing.T) {
	const run = "run-263-cli-abort"
	url, keyFile, node, prompt := aos263CLIServer(t, run)

	var b strings.Builder
	err := dispatch([]string{"abort", "--addr", url, "--run-id", run,
		"--step-id", prompt.StepID, "--emitter", aos263OperatorID, "--key", keyFile}, &b)
	if err != nil {
		t.Fatalf("cmdExhaustionDecision abort (assinatura da CLI recusada pelo no?): %v", err)
	}
	if !strings.Contains(b.String(), "killed") || !strings.Contains(b.String(), run) {
		t.Fatalf("a CLI tem de dizer o que ficou feito e a quem: %q", b.String())
	}
	if st := aos263Estado(t, node, run); st != state.Killed {
		t.Fatalf("o abort pela CLI tem de materializar killed no log; esta em %q", st)
	}
	if _, ainda := aos263Prompt(t, node, run); ainda {
		t.Fatal("a pergunta respondida tem de sair da lista")
	}
}

// TestAOS263_CLIContinueAceitePeloNo: a outra metade. `aos continue` é aceite, o run NÃO é
// transitado (continua suspenso — a re-hospedagem é um acto à parte) e a pergunta sai da lista
// por decisão.
func TestAOS263_CLIContinueAceitePeloNo(t *testing.T) {
	const run = "run-263-cli-continue"
	url, keyFile, node, prompt := aos263CLIServer(t, run)

	var b strings.Builder
	err := dispatch([]string{"continue", "--addr", url, "--run-id", run,
		"--step-id", prompt.StepID, "--emitter", aos263OperatorID, "--key", keyFile}, &b)
	if err != nil {
		t.Fatalf("cmdExhaustionDecision continue (assinatura da CLI recusada pelo no?): %v", err)
	}
	if !strings.Contains(b.String(), exhaustionResumeRoute) {
		t.Fatalf("a CLI tem de nomear a re-hospedagem que se segue a decisao: %q", b.String())
	}
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("o continue NAO transita o run; esta em %q", st)
	}
	if _, ainda := aos263Prompt(t, node, run); ainda {
		t.Fatal("a pergunta respondida tem de sair da lista")
	}
}

// TestAOS263_CLIChaveErradaRecusadaPeloNo: a CLI não contorna a autenticação. Uma seed que não
// corresponde à pubkey pinada produz uma assinatura que o nó RECUSA — e nada acontece ao run.
// É o que torna os dois testes acima não-vacuosos.
func TestAOS263_CLIChaveErradaRecusadaPeloNo(t *testing.T) {
	const run = "run-263-cli-errada"
	url, _, node, prompt := aos263CLIServer(t, run)

	_, errada, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	kf := filepath.Join(t.TempDir(), "errada.key")
	if err := os.WriteFile(kf, []byte(hex.EncodeToString(errada.Seed())), 0o600); err != nil {
		t.Fatalf("escrever seed: %v", err)
	}

	for _, sub := range []string{"abort", "continue"} {
		t.Run(sub, func(t *testing.T) {
			err := dispatch([]string{sub, "--addr", url, "--run-id", run,
				"--step-id", prompt.StepID, "--emitter", aos263OperatorID, "--key", kf}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "403") {
				t.Fatalf("chave errada devia ser recusada (403) pelo no, tive %v", err)
			}
		})
	}
	if st := aos263Estado(t, node, run); st != state.WaitingOnHuman {
		t.Fatalf("nenhuma tentativa recusada pode mexer no run; esta em %q", st)
	}
	if n := len(aos263Selos(t, node)); n != 0 {
		t.Fatalf("uma decisao recusada nao sela nada; selou %d", n)
	}
}

// TestAOS263_CLIDecisaoExigeOsCamposQueAmarram: fail-closed do lado do cliente, ANTES de
// qualquer rede. O `--step-id` é o que amarra a assinatura à pergunta concreta: sem ele a CLI
// não pode adivinhar a que se responde, e um default seria responder a outra pergunta.
func TestAOS263_CLIDecisaoExigeOsCamposQueAmarram(t *testing.T) {
	for _, sub := range []string{"abort", "continue"} {
		t.Run(sub+": sem --addr", func(t *testing.T) {
			if err := dispatch([]string{sub, "--run-id", "r", "--step-id", "s", "--emitter", "o", "--key", "k"}, io.Discard); !errors.Is(err, ErrAddrRequired) {
				t.Fatalf("esperava ErrAddrRequired, tive %v", err)
			}
		})
		t.Run(sub+": sem --step-id", func(t *testing.T) {
			if err := dispatch([]string{sub, "--addr", "http://x", "--run-id", "r", "--emitter", "o", "--key", "k"}, io.Discard); !errors.Is(err, ErrStepIDRequired) {
				t.Fatalf("esperava ErrStepIDRequired, tive %v", err)
			}
		})
		t.Run(sub+": sem --emitter", func(t *testing.T) {
			if err := dispatch([]string{sub, "--addr", "http://x", "--run-id", "r", "--step-id", "s", "--key", "k"}, io.Discard); !errors.Is(err, ErrEmitterRequired) {
				t.Fatalf("esperava ErrEmitterRequired, tive %v", err)
			}
		})
		t.Run(sub+": sem --run-id", func(t *testing.T) {
			if err := dispatch([]string{sub, "--addr", "http://x", "--step-id", "s", "--emitter", "o", "--key", "k"}, io.Discard); !errors.Is(err, ErrRunIDRequired) {
				t.Fatalf("esperava ErrRunIDRequired, tive %v", err)
			}
		})
	}
}
