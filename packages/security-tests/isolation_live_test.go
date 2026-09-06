//go:build gvlive

package securitytests

// CENÁRIO 4-AO-VIVO — ISOLAMENTO CONTRA O EXECUTOR REAL (AOS-358, ADR-004/ADR-007).
//
// O que este ficheiro acrescenta ao `isolation_test.go`: aquele exercita o CONTRATO da
// fronteira sobre `sandbox.NewFakeDriver()` — um jail IN-PROCESS cuja fronteira é o processo
// do nó, não o kernel. Este conduz a MESMA cadeia canónica
//
//	MediatedLauncher → RM (permit) → GVisorDriver → GuestExecutor(HTTP) → componente → runsc
//
// contra o EXECUTOR REAL: o componente `deploy/server/gvisor/component`, que monta um bundle
// OCI efémero por chamada e corre o guest dentro de um sandbox gVisor.
//
// ⚠️ A FRONTEIRA DO gVISOR NÃO É A DO FIRECRACKER. O gVisor interpõe syscalls em user-space
// (plataforma `systrap`); a microVM do Firecracker tem uma fronteira de virtualização de
// hardware e é mais forte. O que se ganha é que o gVisor NÃO exige `/dev/kvm` — e por isso é
// exercitável num runner partilhado, ao contrário do `-tags fclive`, cuja exigência de KVM é
// legítima e continua documentada como procedimento MANUAL em
// `deploy/node/dev-hardened/firecracker/README.md`.
//
// Activação: build tag `gvlive` + `AOS_SANDBOX_GVISOR_URL` a apontar ao `/exec` do componente.
// Sem a URL os cenários SALTAM — e é por isso que o gate `scripts/ci/isolation-live.sh` usa
// `require_tests`: um salto NÃO produz `--- PASS`, logo o gate fica vermelho em vez de verde
// vazio.
//
//	AOS_SANDBOX_GVISOR_URL=http://127.0.0.1:9101/exec \
//	  go test -tags gvlive -run '^TestIsolationLive_|^TestMetaDetectsLive_' ./...

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/sandbox"
)

// envGVisorURL é a MESMA env var que o nó lê (`packages/cmd/aos/firecrackerexecutor.go`), de
// propósito: o que este teste liga é o caminho de produção, não um caminho paralelo.
const envGVisorURL = "AOS_SANDBOX_GVISOR_URL"

// seedMarker é uma frase que só existe no ficheiro semeado read-only do componente
// (`deploy/server/gvisor/seed/notes`). Serve de prova POSITIVA de que a execução chegou
// mesmo ao sandbox: um executor que devolvesse vazio, ou que lesse outra coisa, não a produz.
const seedMarker = "raiz semeada READ-ONLY do sandbox"

// hostOnlyPath é um caminho que EXISTE na imagem do componente (debian-slim) e NÃO existe
// dentro do sandbox (rootfs mínimo com /guest, /seed e /proc). É a sonda da fronteira: se o
// guest o alcançasse, a raiz semeada não seria o único conteúdo alcançável.
const hostOnlyPath = "etc/hostname"

// httpGuestExecutor é o MESMO shape do remoteGVisorExecutor do nó (stdlib HTTP → componente).
// É deliberadamente uma cópia: importar o módulo do nó puxaria as suas dependências para dentro
// da suite e o que se quer provar é o CONTRATO DE FIO, não a partilha de código.
type httpGuestExecutor struct{ url string }

func (e httpGuestExecutor) RunInGuest(ctx context.Context, inst sandbox.Instance, call sandbox.ToolCall) ([]byte, []sandbox.Artifact, int, error) {
	body, _ := json.Marshal(map[string]any{
		"run_id":  inst.ID,
		"step_id": "live",
		"call": map[string]any{
			"tool_id": call.ToolID, "command": call.Command,
			"path": call.Path, "args": call.Args, "write": call.Write,
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, 1, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, 1, fmt.Errorf("componente gvisor: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, 1, fmt.Errorf("componente gvisor: estado HTTP %d", resp.StatusCode)
	}
	var r struct {
		Stdout   []byte `json:"stdout"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, nil, 1, fmt.Errorf("componente gvisor: resposta ilegível: %w", err)
	}
	if r.Error != "" {
		return r.Stdout, nil, r.ExitCode, errors.New(r.Error)
	}
	return r.Stdout, nil, r.ExitCode, nil
}

// hostExecutor é a CONTRAPROVA: o mesmo shape de executor, sem sandbox nenhum, a resolver o
// caminho contra uma raiz do host. Existe para o meta-teste — sem ele, "o caminho foi recusado"
// seria indistinguível de "o caminho nunca existiu".
type hostExecutor struct{ root string }

func (e hostExecutor) RunInGuest(_ context.Context, _ sandbox.Instance, call sandbox.ToolCall) ([]byte, []sandbox.Artifact, int, error) {
	b, err := os.ReadFile(filepath.Join(e.root, filepath.Clean("/"+call.Path)))
	if err != nil {
		return nil, nil, 1, err
	}
	return b, nil, 0, nil
}

// liveLauncher constrói a cadeia canónica com o driver gVisor REAL e o executor dado.
func liveLauncher(t *testing.T, exec sandbox.GuestExecutor) *sandbox.MediatedLauncher {
	t.Helper()
	store := newEventStore(t)
	launcher, err := sandbox.NewLauncher(
		sandbox.NewGVisorDriver(sandbox.WithGVisorExecutor(exec)),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := sandbox.NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	return ml
}

// componentURL devolve a URL do componente, ou SALTA. O salto é seguro porque o gate exige
// `--- PASS` por nome (require_tests): saltar torna o gate VERMELHO, nunca verde vazio.
func componentURL(t *testing.T) string {
	t.Helper()
	url := strings.TrimSpace(os.Getenv(envGVisorURL))
	if url == "" {
		t.Skipf("componente gVisor ausente: define %s (ex.: http://127.0.0.1:9101/exec)", envGVisorURL)
	}
	return url
}

// TestIsolationLive_GVisorExecutaNoSandboxReal prova POSITIVAMENTE que a cadeia chegou ao
// executor real: a tool call atravessa o RM, o GVisorDriver e o componente, corre dentro de um
// sandbox runsc dedicado, e devolve o conteúdo da raiz semeada read-only. O resultado é
// untrusted POR TIPO (ADR-005) — o que vem de dentro do sandbox nunca é confiável.
func TestIsolationLive_GVisorExecutaNoSandboxReal(t *testing.T) {
	ml := liveLauncher(t, httpGuestExecutor{url: componentURL(t)})

	res, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{
		RunID: "run-gv-live", StepID: "s1",
		Call: sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"},
	})
	if err != nil {
		t.Fatalf("Execute (sandbox gVisor real): %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("leitura da raiz semeada falhou: exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}
	if !strings.Contains(string(res.Stdout), seedMarker) {
		t.Fatalf("conteúdo inesperado do sandbox (a execução pode não ter chegado ao runsc): %q", res.Stdout)
	}
	if res.Taint() != sandbox.TaintUntrusted {
		t.Fatalf("resultado do sandbox devia ser untrusted por tipo, veio %v", res.Taint())
	}
	t.Logf("\n"+
		"  CAMINHO   : MediatedLauncher -> RM(permit) -> GVisorDriver -> HTTP -> componente -> runsc\n"+
		"  FRONTEIRA : gVisor (interposição de syscalls em user-space, systrap) — NÃO é a microVM\n"+
		"  BYTES     : %d da raiz semeada read-only\n"+
		"  TAINT     : %v", len(res.Stdout), res.Taint())
}

// TestIsolationLive_GVisorFronteiraRecusaForaDaRaizSemeada prova NEGATIVAMENTE a fronteira: um
// caminho que EXISTE na imagem do componente mas não dentro do sandbox é inalcançável, tanto
// por travessia relativa como em forma absoluta. É a invariante «a raiz semeada é o único
// conteúdo alcançável» medida contra o executor real, e não contra o jail in-process.
//
// LIMITE, apurado pela revisão adversarial e declarado em vez de presumido: isto mede a CADEIA
// COMPOSTA, não o gVisor. O guest do componente faz ele próprio `filepath.Clean` + verificação
// de prefixo e recusa ANTES de qualquer syscall que o sandbox pudesse interceptar — substituir
// o `runsc` por um `exec` cru mantinha este teste verde. Quem prova execução DENTRO do sandbox
// é [TestIsolationLive_GVisorExecutaNoSandboxReal]: o conteúdo devolvido só existe no bundle
// OCI montado para o runsc. Está no `not_proved` do relatório.
func TestIsolationLive_GVisorFronteiraRecusaForaDaRaizSemeada(t *testing.T) {
	ml := liveLauncher(t, httpGuestExecutor{url: componentURL(t)})

	vectores := []struct{ nome, path string }{
		{"travessia relativa", "../../../../" + hostOnlyPath},
		{"caminho absoluto", "/" + hostOnlyPath},
	}
	for i, v := range vectores {
		res, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{
			RunID: fmt.Sprintf("run-gv-escape-%d", i), StepID: "s1",
			Call: sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: v.path},
		})
		// Fail-closed: OU o executor propaga erro, OU devolve exit != 0. O que NÃO pode
		// acontecer é devolver conteúdo do host com exit 0.
		if err == nil && res.ExitCode == 0 {
			t.Fatalf("FRONTEIRA QUEBRADA (%s): %q devolveu conteúdo com exit 0: %q", v.nome, v.path, res.Stdout)
		}
		if len(bytes.TrimSpace(res.Stdout)) != 0 {
			t.Fatalf("FRONTEIRA QUEBRADA (%s): %q devolveu bytes: %q", v.nome, v.path, res.Stdout)
		}
	}
}

// TestMetaDetectsLive_FugaAlcancavelSemSandbox é a prova de DETECÇÃO NÃO-VÁCUA do teste acima,
// no molde dos `TestMetaDetects_*` de `metatests_test.go`: com o MESMO shape de executor mas
// SEM sandbox, o mesmo verbo `read` alcança um ficheiro fora da raiz semeada. Sem isto, «o
// caminho foi recusado» seria indistinguível de «o caminho nunca existiu» — e o cenário de
// fronteira podia passar por razões erradas. Não precisa do componente: mede o contrafactual.
func TestMetaDetectsLive_FugaAlcancavelSemSandbox(t *testing.T) {
	raiz := t.TempDir()
	if err := os.WriteFile(filepath.Join(raiz, "hostname"), []byte("host-real"), 0o600); err != nil {
		t.Fatalf("preparar sonda: %v", err)
	}
	ml := liveLauncher(t, hostExecutor{root: raiz})

	res, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{
		RunID: "run-gv-meta", StepID: "s1",
		Call: sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "/hostname"},
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "host-real") {
		t.Fatalf("META-TESTE VÁCUO: sem sandbox o mesmo caminho DEVIA ser alcançável, veio (err=%v exit=%d out=%q)",
			err, res.ExitCode, res.Stdout)
	}
}

// TestIsolationLive_Report emite a linha marcada AOS_ISOLATION_LIVE_REPORT que o gate consome.
// O campo agregado `pass` é o ÚLTIMO do objecto, como em AOS_SECURITY_REPORT/AOS_DR_REPORT, para
// que o gate possa ancorar a verificação ao fim da linha.
//
// O relatório declara também, por campo, a FRONTEIRA exercitada e o que fica POR EXERCITAR —
// o gate imprime-o, e quem lê o log não fica com a impressão de que isto substitui o
// Firecracker.
func TestIsolationLive_Report(t *testing.T) {
	url := componentURL(t)

	// AOS-358, revisão adversarial — O RELATÓRIO TEM DE MEDIR, NÃO DE DECLARAR.
	//
	// A versão anterior punha `Pass: true` como literal e `Executor` como constante. Medido
	// pelo revisor: com `AOS_SANDBOX_GVISOR_URL=http://127.0.0.1:9/exec` (porta MORTA) este
	// teste passava e emitia `"executor":"real…","pass":true` — e os dois `grep` do gate
	// ficavam satisfeitos SEM nenhuma execução real ter acontecido. Um relatório que asserta
	// valores que ele próprio fabrica é o defeito que este epic fecha, aplicado ao artefacto
	// que o declara fechado.
	//
	// Agora o veredicto é DERIVADO: faz-se a chamada positiva contra o executor e só se
	// declara `pass` se ela tiver mesmo trazido conteúdo que só existe na raiz semeada.
	pass, porque := verificaExecutorReal(t, url)
	// Struct e não map: o `encoding/json` ordena as chaves de um map alfabeticamente, e o
	// gate ancora o veredicto agregado ao FIM da linha. Com um map, `pass` deixaria de ser o
	// último campo e a âncora do gate passaria a casar por acidente.
	rep := struct {
		Gate            string   `json:"gate"`
		Ticket          string   `json:"ticket"`
		Boundary        string   `json:"boundary"`
		BoundaryNot     string   `json:"boundary_not"`
		Executor        string   `json:"executor"`
		ComponentURLSet bool     `json:"component_url_set"`
		Verificacao     string   `json:"verificacao"`
		Scenarios       []string `json:"scenarios"`
		NotProved       []string `json:"not_proved"`
		ContractGate    string   `json:"contract_gate"`
		Pass            bool     `json:"pass"`
	}{
		Gate:            "isolation-live",
		Ticket:          "AOS-358",
		Boundary:        "gvisor/runsc (interposicao de syscalls em user-space, systrap)",
		BoundaryNot:     "firecracker/KVM (virtualizacao de hardware) — exige /dev/kvm, procedimento manual",
		Executor:        "real (componente HTTP externo)",
		ComponentURLSet: url != "",
		Verificacao:     porque,
		Scenarios:       []string{"seed_read_positivo", "fora_da_raiz_recusado", "meta_fuga_sem_sandbox"},
		NotProved: []string{
			"nao_persistencia_do_overlay (o guest do componente so expoe o verbo `read`)",
			"seccomp_allowlist",
			"ausencia_de_socket_do_host",
			"atribuicao_da_recusa_de_P2_ao_gvisor (o guest valida o path ele proprio, antes de " +
				"qualquer syscall interceptavel — substituir runsc por exec cru mantem P2 verde)",
		},
		ContractGate: "security.sh (AOS-075) — contrato sobre FakeDriver, NAO a fronteira",
		Pass:         pass,
	}
	if !pass {
		t.Fatalf("relatorio NAO pode declarar pass: %s", porque)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal do relatório: %v", err)
	}
	t.Logf("AOS_ISOLATION_LIVE_REPORT %s", b)
}

// verificaExecutorReal conduz UMA chamada positiva contra o executor e devolve se ela
// provou execução real, mais a razão em texto (que entra no relatório).
//
// «Provou» é: o sandbox devolveu conteúdo que SÓ existe na raiz semeada desta chamada. Um
// componente ausente, uma porta morta, ou um executor que devolva vazio não passam — que é
// exactamente o que a versão declarativa deste relatório deixava passar.
func verificaExecutorReal(t *testing.T, url string) (bool, string) {
	t.Helper()
	if url == "" {
		return false, "AOS_SANDBOX_GVISOR_URL ausente — nada foi executado"
	}
	ml := liveLauncher(t, httpGuestExecutor{url: url})
	res, err := ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{
		RunID: "run-gv-report", StepID: "s1",
		Call: sandbox.ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"},
	})
	switch {
	case err != nil:
		return false, "a chamada ao executor real FALHOU: " + err.Error()
	case res.ExitCode != 0:
		return false, "o executor real devolveu exit != 0"
	case !strings.Contains(string(res.Stdout), seedMarker):
		return false, "o executor real nao devolveu o conteudo da raiz semeada — nao se pode afirmar execucao real"
	}
	return true, "chamada positiva contra o executor real devolveu o conteudo da raiz semeada"
}
