package main

// WIRING DE EXECUÇÃO EM SANDBOX (AOS-005 + AOS-064) — a última peça do loop live: liga as
// tools do registry (AOS_MODEL_TOOLS) ao substrato de sandbox existente, SEM reinventar nada.
//
// Duas metades, ambas ancoradas no Reference Monitor:
//   1. REGISTO (registerSandboxLaunchers): por cada tool com um bloco `sandbox`, constrói UM
//      [sandbox.MediatedLauncher] que regista o seu dispatch NO RM DO NÓ (sec.Monitor()). A
//      partir daí a tool só executa sob permit — no-bypass ESTRUTURAL (o dispatch é
//      não-exportado; só o RM o alcança, ADR-002).
//   2. REESCRITA (newSandboxEffectRewriter → SecuredConfig.EffectRewriter): o dispatch da
//      sandbox espera um ExecRequest JSON, mas o loop passa os args OPACOS do modelo. O
//      EffectRewriter traduz args→ExecRequest com o RunID/StepID REAIS da Call (que só existem
//      no dispatcher) via [sandbox.BuildExecRequest]. NÃO toca na decisão (Principal/Capability/
//      Resource/Taint) — só reshapa o payload; um erro vira Deny fail-closed.
//
// INVARIANTE DE SEGURANÇA: o Command vem SEMPRE do registry trusted; os args untrusted do
// modelo só preenchem VALORES nos slots nomeados. O modelo escolhe QUAL tool e os VALORES,
// nunca o comando executado no guest. Ver [sandbox.SandboxBinding].

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	integration "github.com/aos-ref/integration"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/sandbox"
)

// sandboxBindingsFromEnv lê o MESMO ficheiro AOS_MODEL_TOOLS (via readModelToolSpecs) e
// devolve o mapa toolID → [sandbox.SandboxBinding] das tools que declaram um bloco `sandbox`.
// Vazio ⇒ (nil, nil): nenhuma tool tem executor de sandbox ligado (comportamento inalterado).
// Fail-closed: um bloco `sandbox` sem `command` aborta o arranque.
// Devolve TAMBEM as tools OFERECIDAS ao modelo sem executor: calculam-se AQUI, onde o manifesto
// ja esta lido, e nao a jusante com uma segunda leitura.
//
// A primeira versao relia o ficheiro em `registerSandboxLaunchers` e, se essa leitura falhasse,
// SALTAVA a declaracao sem dizer nada — o mesmo defeito silencioso que a declaracao existe para
// corrigir, dentro da propria correccao.
func sandboxBindingsFromEnv() (map[string]sandbox.SandboxBinding, []string, error) {
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]sandbox.SandboxBinding)
	for _, s := range specs {
		if s.Sandbox == nil {
			continue
		}
		name := strings.TrimSpace(s.Name)
		cmd := strings.TrimSpace(s.Sandbox.Command)
		if cmd == "" {
			return nil, nil, fmt.Errorf("%w: tool %q tem bloco `sandbox` mas `command` vazio (o Command é FIXO e trusted, nunca dos args)", ErrBadModelTools, name)
		}
		out[name] = sandbox.SandboxBinding{
			Command:  cmd,
			PathArg:  strings.TrimSpace(s.Sandbox.PathArg),
			ArgsFrom: s.Sandbox.ArgsFrom,
			WriteArg: strings.TrimSpace(s.Sandbox.WriteArg),
		}
	}
	// As orfas calculam-se com o manifesto que ACABOU de ser lido — mesmo quando nao ha binding
	// nenhum, porque «nenhuma tool tem executor» e precisamente o caso que mais precisa de ser dito.
	orfas := toolsSemExecutor(specs, out)
	if len(out) == 0 {
		return nil, orfas, nil
	}
	return out, orfas, nil
}

// newSandboxEffectRewriter devolve o [integration.SecuredConfig.EffectRewriter] que traduz os
// args do modelo num ExecRequest da sandbox, para as tools com binding. Uma Call de uma tool
// SEM binding passa inalterada (o Input mantém-se). bindings vazio ⇒ nil (sem reescrita:
// comportamento byte-idêntico). Um erro de tradução propaga-se ⇒ o rewritingDispatcher
// materializa-o como Deny fail-closed (nenhum efeito).
func newSandboxEffectRewriter(bindings map[string]sandbox.SandboxBinding) func(referencemonitor.Call) (referencemonitor.Call, error) {
	if len(bindings) == 0 {
		return nil
	}
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		b, ok := bindings[call.ToolID]
		if !ok {
			return call, nil // tool sem sandbox ⇒ Input opaco inalterado
		}
		req, err := sandbox.BuildExecRequest(call.RunID, call.StepID, call.ToolID, call.Input, b)
		if err != nil {
			return referencemonitor.Call{}, err // fail-closed ⇒ Deny no dispatcher
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return referencemonitor.Call{}, err
		}
		call.Input = raw
		return call, nil
	}
}

// registerSandboxLaunchers constrói UM [sandbox.Launcher] (driver + snapshot base + event sink
// no MESMO Event Store do nó) e, por cada tool com binding, regista um [sandbox.MediatedLauncher]
// no RM do nó. Deve correr DEPOIS de NewSecuredRuntime (precisa de sec.Monitor()). bindings
// vazio ⇒ no-op. Fail-closed: qualquer falha de construção/registo aborta o arranque.
func registerSandboxLaunchers(sec *integration.SecuredRuntime, es EventStorePort, bindings map[string]sandbox.SandboxBinding, semExecutor []string, log func(string, ...any)) error {
	// A DECLARACAO das orfas vem ANTES do return antecipado, e e deliberado: sem bindings
	// nenhuns nao ha driver a montar, mas «NENHUMA tool tem executor» e precisamente o caso que
	// mais precisa de ser dito — e a primeira versao desta correccao calava-se exactamente ai,
	// contrariando o comentario que eu proprio tinha escrito duas funcoes acima.
	declararSemExecutor(semExecutor, log)
	if len(bindings) == 0 {
		return nil
	}
	// Driver: AOS_SANDBOX_DRIVER ∈ {fake,firecracker,gvisor}; default "fake" (jail funcional
	// in-process — FS overlay read-only, seccomp default-deny, escape bloqueado). firecracker/
	// gvisor exigem KVM/runsc no host: sem eles NewMediatedLauncher constrói na mesma, mas a
	// execução devolve ErrDriverUnavailable (o caminho de produção fica WIRED, só falta o host).
	kind := sandbox.DriverFake
	if v := strings.TrimSpace(os.Getenv("AOS_SANDBOX_DRIVER")); v != "" {
		kind = sandbox.DriverKind(v)
	}
	// firecracker + AOS_SANDBOX_FIRECRACKER_URL ⇒ injecta o executor remoto (microVM REAL via o
	// componente externo); senão skeleton (ErrDriverUnavailable no exec). Ver firecrackerexecutor.go.
	driver, err := buildSandboxDriver(kind)
	if err != nil {
		return fmt.Errorf("aos: driver de sandbox %q: %w", kind, err)
	}
	snap, err := sandboxSnapshotFromEnv()
	if err != nil {
		return err
	}
	launcher, err := sandbox.NewLauncher(driver,
		sandbox.WithEventSink(sandbox.NewEventStoreSink(es)),
		sandbox.WithSnapshot(snap),
	)
	if err != nil {
		return fmt.Errorf("aos: launcher de sandbox: %w", err)
	}
	// Um MediatedLauncher por tool: partilham o MESMO launcher (o seq é atómico). Cada um
	// regista o seu dispatch sob o seu toolID no RM — no-bypass estrutural.
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		if _, err := sandbox.NewMediatedLauncher(sec.Monitor(), launcher, name); err != nil {
			return fmt.Errorf("aos: registar sandbox para a tool %q: %w", name, err)
		}
		names = append(names, name)
	}
	log("execucao de tools em sandbox (AOS-005/AOS-064): %d tool(s) %v ligadas ao driver %q via MediatedLauncher (no-bypass estrutural; args→ExecRequest pelo EffectRewriter)", len(names), names, kind)
	// AS QUE FICAM DE FORA. Ver [toolsSemExecutor]: podem ser deliberadas, mas nao podem ser MUDAS.
	return nil
}

// sandboxSnapshotFromEnv semeia o RootFS BASE read-only (AOS-066) a partir de
// AOS_SANDBOX_SEED_DIR (opcional): cada ficheiro do directório vira uma entrada base pelo seu
// nome. Sem a variável ⇒ base vazia (a tool lê o que a call montar/produzir). Só o nível de
// topo é lido (sem recursão) — a semente de referência é um punhado de documentos planos.
func sandboxSnapshotFromEnv() (*sandbox.Snapshot, error) {
	base := make(map[string][]byte)
	if dir := strings.TrimSpace(os.Getenv("AOS_SANDBOX_SEED_DIR")); dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("aos: AOS_SANDBOX_SEED_DIR: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, fmt.Errorf("aos: ler semente de sandbox %q: %w", e.Name(), err)
			}
			base[e.Name()] = raw
		}
	}
	snap, err := sandbox.NewSnapshot("img/aos-sandbox-v1", base)
	if err != nil {
		return nil, fmt.Errorf("aos: snapshot de sandbox: %w", err)
	}
	return snap, nil
}

// toolsSemExecutor devolve as tools que o manifesto OFERECE ao modelo e para as quais o nó NÃO
// regista despacho — as que não têm bloco `sandbox`.
//
// PORQUE ISTO É DECLARADO E NÃO RECUSADO. Uma tool sem executor pode ser DELIBERADA: é o que a
// `demo-pdp-tool-deny.sh` demonstra — «o modelo NÃO executa uma tool só porque lha ofereceram; o
// RM re-valida contra o SEU registry assinado». Recusar o arranque destruiria exactamente a
// propriedade que essa demonstração existe para mostrar. Tentei-o antes de ler a demo, e o teste
// `TestRegistriesDoRepoArrancam` apanhou-me.
//
// O QUE FALTAVA, então, não era uma recusa — era a DECLARAÇÃO. O banner dizia «1 tool(s)
// [doc_read] ligadas ao driver» e calava-se sobre a segunda: em produção, o manifesto oferecia
// `doc_read` e `web_post`, e nada dizia que uma delas nunca correria. O modelo gasta um turno a
// tentá-la e o operador não sabe porquê.
//
// «A postura anunciada = a postura ligada» é a regra desta casa (AOS-203/AOS-248). Uma tool
// oferecida e não executável é postura, e passa a ser anunciada.
func toolsSemExecutor(specs []modelToolSpec, bindings map[string]sandbox.SandboxBinding) []string {
	var out []string
	for _, s := range specs {
		nome := strings.TrimSpace(s.Name)
		if nome == "" {
			continue
		}
		if _, tem := bindings[nome]; !tem {
			out = append(out, nome)
		}
	}
	sort.Strings(out)
	return out
}

// declararSemExecutor escreve a linha das tools oferecidas ao modelo que o nó não executa.
//
// Função própria porque tem de ser chamada ANTES do return antecipado de
// [registerSandboxLaunchers]: sem bindings nenhuns não há driver a montar, mas «NENHUMA tool tem
// executor» é o caso que mais precisa de ser dito, e não o menos.
func declararSemExecutor(semExecutor []string, log func(string, ...any)) {
	if len(semExecutor) == 0 {
		return
	}
	log("execucao de tools em sandbox (AOS-005/AOS-064): %d tool(s) %v sao OFERECIDAS ao modelo e "+
		"NAO TEM EXECUTOR neste no — sem bloco `sandbox` nao se regista despacho, e cada chamada "+
		"morre no caminho de recusa DEPOIS de o modelo gastar um turno a tenta-la. Pode ser "+
		"DELIBERADO (e o que a demo de defesa-em-profundidade mostra: oferecer nao e executar); o "+
		"que nao pode e ficar por dizer", len(semExecutor), semExecutor)
}
