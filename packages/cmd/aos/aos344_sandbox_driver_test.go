package main

// AOS-344 — O DRIVER DE SANDBOX PASSA A TER PORTA DE PRODUÇÃO.
//
// O achado não é escape de sandbox: o driver de referência não corre processos. É que, dos três
// drivers, era o único que falhava ABERTO. `firecracker` e `gvisor` sem executor provisionado
// devolvem `ErrDriverUnavailable` e a chamada morre no caminho de recusa; o de referência sucede
// em silêncio, e o resultado — que nenhuma fronteira ao nível do kernel produziu — é selado na
// hash-chain WORM como se fosse um efeito real.
//
// E era ELE o default: `deploy/server/docker-compose.prod.yml:364` define
// `AOS_SANDBOX_DRIVER: "${AOS_SANDBOX_DRIVER:-}"` — vazio —, o vazio caía em
// [sandbox.DriverFake], e o mesmo compose (`:372`) monta um catálogo de tools que declara um
// bloco `sandbox` (`deploy/server/model-tools/tools.json:18`). Nenhuma das nove guardas
// `ErrProductionNeeds*` cobria o sandbox, e `registerSandboxLaunchers` nem sequer recebia a
// postura do nó.
//
// Os testes abaixo medem as DUAS metades, porque uma sem a outra não prova nada: a recusa em
// produção, e o CONTROLO NEGATIVO de que fora de produção o driver de referência continua a
// compor — por omissão e por escolha explícita. Uma guarda que partisse o smoke e as demos seria
// revertida no dia seguinte.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	sandbox "github.com/aos-ref/substrate/sandbox"
)

// manifestoDeProducaoComSandbox é o catálogo SANCIONADO que o compose de produção monta. Usa-se
// o ficheiro real e não um fixture: o gatilho que DEF-701/DEF-702 declaravam («catálogo de tools
// não-vazio a executar código não-confiável») ocorreu NESTE ficheiro, e um fixture inventado
// deixaria o teste verde no dia em que alguém removesse de lá o bloco `sandbox`.
const manifestoDeProducaoComSandbox = "../../../deploy/server/model-tools/tools.json"

// aos344ProducaoQuaseCompleta monta uma produção que passa TODAS as colunas de postura, para que
// o teste meça a guarda do sandbox e não outra. Molde de [aos300ProducaoQuaseCompleta].
func aos344ProducaoQuaseCompleta(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_ID", "iss:aos-344")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp-soberania.example")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	// SEM WORM nem execução durável ⇒ a guarda da KEK não entra em jogo e não mascara esta.
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	t.Setenv("AOS_APPROVERS_FILE", "")
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_NAME", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "")
	// AOS-300: a produção exige Event Store durável, incondicionalmente.
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(t.TempDir(), "events.wal"))
}

// TestAOS344_ProducaoSemDriverDeSandboxRecusa é a AC1 pelo caminho que a auditoria mediu: o nó
// inteiro, em produção, com o catálogo sancionado montado e `AOS_SANDBOX_DRIVER` POR DEFINIR —
// exactamente o que o compose de produção entrega hoje.
func TestAOS344_ProducaoSemDriverDeSandboxRecusa(t *testing.T) {
	aos344ProducaoQuaseCompleta(t)
	t.Setenv("AOS_MODEL_TOOLS", manifestoDeProducaoComSandbox)
	t.Setenv("AOS_SANDBOX_DRIVER", "") // o estado sob teste: o default do compose

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsSandboxDriver) {
		t.Fatalf("producao com tools de sandbox e sem AOS_SANDBOX_DRIVER devia abortar com ErrProductionNeedsSandboxDriver, veio: %v", err)
	}
}

// TestAOS344_ProducaoComOFakeExplicitoRecusaNAMESMA fecha a via que ficaria a uma variável de
// distância. A auditoria nomeou a OMISSÃO, mas o valor explícito não acrescenta fronteira
// nenhuma — só tornaria a mesma postura deliberada, e a recusa perderia o sentido.
func TestAOS344_ProducaoComOFakeExplicitoRecusaNAMESMA(t *testing.T) {
	aos344ProducaoQuaseCompleta(t)
	t.Setenv("AOS_MODEL_TOOLS", manifestoDeProducaoComSandbox)
	t.Setenv("AOS_SANDBOX_DRIVER", string(sandbox.DriverFake))

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsSandboxDriver) {
		t.Fatalf("producao com AOS_SANDBOX_DRIVER=fake devia abortar com ErrProductionNeedsSandboxDriver, veio: %v", err)
	}
}

// TestAOS344_ProducaoComGVisorArranca prova que a guarda tem SAÍDA — e que ela é a que os
// deployments sancionados já usam (`deploy/server/README.md` indica `gvisor`). Sem esta metade,
// uma guarda que recusasse tudo passaria os dois testes acima.
//
// Repare-se que NÃO se exige `AOS_SANDBOX_GVISOR_URL`: sem ela o driver fica sem executor
// provisionado e o exec devolve `ErrDriverUnavailable`. Isso é o gap honesto e é precisamente o
// alinhamento que este ticket pede — falhar fechado, não fabricar um resultado.
func TestAOS344_ProducaoComGVisorArranca(t *testing.T) {
	aos344ProducaoQuaseCompleta(t)
	t.Setenv("AOS_MODEL_TOOLS", manifestoDeProducaoComSandbox)
	t.Setenv("AOS_SANDBOX_DRIVER", string(sandbox.DriverGVisor))
	t.Setenv("AOS_SANDBOX_GVISOR_URL", "")

	if err := run(io.Discard); errors.Is(err, ErrProductionNeedsSandboxDriver) {
		t.Fatalf("producao com AOS_SANDBOX_DRIVER=gvisor NAO devia bater na guarda do sandbox, veio: %v", err)
	} else if err != nil {
		t.Fatalf("producao com gvisor devia arrancar, veio: %v", err)
	}
}

// TestAOS344_ProducaoSemToolsDeSandboxNaoExigeDriver fixa que a guarda é CONDICIONAL à opção do
// operador, no molde de [ErrProductionNeedsDurableApproval]. Sem tool nenhuma com bloco
// `sandbox` não há driver a montar nem resultado a selar: exigir a variável ali seria cerimónia
// sobre um caminho que não corre — e partiria todos os nós de produção que não despacham tools.
func TestAOS344_ProducaoSemToolsDeSandboxNaoExigeDriver(t *testing.T) {
	aos344ProducaoQuaseCompleta(t)
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_SANDBOX_DRIVER", "")

	if err := run(io.Discard); errors.Is(err, ErrProductionNeedsSandboxDriver) {
		t.Fatal("producao SEM tools de sandbox nao pode exigir AOS_SANDBOX_DRIVER — a guarda e condicional a opcao do operador")
	}
}

// TestAOS344_ForaDeProducaoOFakeContinuaAFuncionar é o CONTROLO NEGATIVO que a AC2 exige, nas
// suas DUAS formas: o default (variável ausente) e a escolha explícita. É o que o smoke, as
// demos e o `aos serve` de referência correm; se este teste ficar vermelho, a guarda foi longe
// de mais e a correcção custa mais do que o defeito.
func TestAOS344_ForaDeProducaoOFakeContinuaAFuncionar(t *testing.T) {
	casos := []struct {
		nome  string
		valor string
	}{
		{"por omissao", ""},
		{"escolha explicita", string(sandbox.DriverFake)},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var sb strings.Builder
			t.Setenv("AOS_MODE", "") // modo de referência
			t.Setenv("AOS_MODEL_TOOLS", manifestoDeProducaoComSandbox)
			t.Setenv("AOS_SANDBOX_DRIVER", c.valor)

			if err := run(&sb); err != nil {
				t.Fatalf("fora de producao o driver de referencia tem de continuar a compor (%s), veio: %v", c.nome, err)
			}
			if !strings.Contains(sb.String(), "ligadas ao driver \"fake\"") {
				t.Errorf("o banner devia declarar o driver de referencia composto (%s).\nBanner:\n%s", c.nome, sb.String())
			}
		})
	}
}

// TestAOS344_SandboxDriverKind mede a decisão isolada — as quatro casas da matriz
// (postura × valor), incluindo a que o vocabulário não conhece.
func TestAOS344_SandboxDriverKind(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome       string
		raw        string
		producao   bool
		quer       sandbox.DriverKind
		querRecusa bool
	}{
		{"dev + vazio ⇒ referencia", "", false, sandbox.DriverFake, false},
		{"dev + fake explicito", "fake", false, sandbox.DriverFake, false},
		{"dev + gvisor", "gvisor", false, sandbox.DriverGVisor, false},
		{"dev + espacos ⇒ referencia", "   ", false, sandbox.DriverFake, false},
		{"producao + vazio ⇒ recusa", "", true, "", true},
		{"producao + fake explicito ⇒ recusa", "fake", true, "", true},
		{"producao + fake com espacos ⇒ recusa", " fake ", true, "", true},
		{"producao + gvisor", "gvisor", true, sandbox.DriverGVisor, false},
		{"producao + firecracker", "firecracker", true, sandbox.DriverFirecracker, false},
		// O vocabulário é validado por buildSandboxDriver; esta função é a guarda de POSTURA e
		// não pode passar a ser um segundo validador que diverge do primeiro.
		{"producao + kind desconhecido passa adiante", "runsc-ish", true, sandbox.DriverKind("runsc-ish"), false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got, err := sandboxDriverKind(c.raw, c.producao)
			if c.querRecusa {
				if !errors.Is(err, ErrProductionNeedsSandboxDriver) {
					t.Fatalf("queria ErrProductionNeedsSandboxDriver, veio (%q, %v)", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("nao devia recusar: %v", err)
			}
			if got != c.quer {
				t.Errorf("kind = %q, quero %q", got, c.quer)
			}
		})
	}
}

// TestAOS344_OErroNomeiaAsDuasSaidas: um fail-closed que não diz o que definir manda o operador
// adivinhar — e, aqui, adivinhar mal significa voltar ao driver que a guarda acabou de recusar.
func TestAOS344_OErroNomeiaAsDuasSaidas(t *testing.T) {
	t.Parallel()
	msg := ErrProductionNeedsSandboxDriver.Error()
	for _, v := range []string{
		"AOS_SANDBOX_DRIVER=gvisor",
		"AOS_SANDBOX_GVISOR_URL",
		"AOS_SANDBOX_DRIVER=firecracker",
		"AOS_SANDBOX_FIRECRACKER_URL",
		"AOS_MODEL_TOOLS",
	} {
		if !strings.Contains(msg, v) {
			t.Errorf("o erro tem de nomear %s; veio: %s", v, msg)
		}
	}
}
