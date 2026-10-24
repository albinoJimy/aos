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
//
// `production` espelha `Config.ProductionMode` e chega por PARÂMETRO pela mesma razão que o
// campo existe (AOS-328): a guarda tem de ser exercitável sem mexer no ambiente do processo. Até
// AOS-344 esta função não recebia nada da `Config` — não tinha por onde saber em que postura
// corria, e por isso elegia o driver de referência em produção sem que ninguém o decidisse.
func registerSandboxLaunchers(sec *integration.SecuredRuntime, es EventStorePort, bindings map[string]sandbox.SandboxBinding, semExecutor []string, production bool, log func(string, ...any)) error {
	// A DECLARACAO das orfas vem ANTES do return antecipado, e e deliberado: sem bindings
	// nenhuns nao ha driver a montar, mas «NENHUMA tool tem executor» e precisamente o caso que
	// mais precisa de ser dito — e a primeira versao desta correccao calava-se exactamente ai,
	// contrariando o comentario que eu proprio tinha escrito duas funcoes acima.
	declararSemExecutor(semExecutor, log)
	if len(bindings) == 0 {
		return nil
	}
	// Driver: AOS_SANDBOX_DRIVER ∈ {fake,firecracker,gvisor}; fora de produção o default é
	// "fake" (jail funcional in-process — FS overlay read-only, seccomp default-deny, escape
	// bloqueado), e em produção o "fake" é recusado (AOS-344, ver [sandboxDriverKind]).
	// firecracker/gvisor executam num componente EXTERNO (AOS_SANDBOX_*_URL): o KVM e o runsc
	// são do host desse componente, não do nó. Sem a URL NewMediatedLauncher constrói na mesma,
	// mas a execução falha (ErrDriverUnavailable no firecracker, ErrGVisorExecutorUnset no gvisor).
	kind, err := sandboxDriverKind(os.Getenv("AOS_SANDBOX_DRIVER"), production)
	if err != nil {
		return err
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

// sandboxDriverKind decide o [sandbox.DriverKind] a partir do valor bruto de AOS_SANDBOX_DRIVER
// e da postura do nó (AOS-344). É função à parte, e pura, porque a decisão é a peça que a
// auditoria mediu: mantê-la dentro de [registerSandboxLaunchers] obrigaria cada teste dela a
// montar um SecuredRuntime inteiro para observar uma escolha de três valores.
//
// FORA DE PRODUÇÃO: inalterado — vazio ⇒ [sandbox.DriverFake], qualquer outro valor passa
// tal-qual a [buildSandboxDriver] (que valida o kind).
//
// EM PRODUÇÃO: o driver de referência deixa de ser eleito, por omissão OU por escolha explícita
// ⇒ [ErrProductionNeedsSandboxDriver]. Ver lá o porquê de a recusa cobrir as duas vias e de não
// haver escape declarado. Um kind desconhecido continua a ser problema de [buildSandboxDriver]:
// esta função não é o validador do vocabulário, é a guarda de postura.
func sandboxDriverKind(raw string, production bool) (sandbox.DriverKind, error) {
	v := strings.TrimSpace(raw)
	if !production {
		if v == "" {
			return sandbox.DriverFake, nil
		}
		return sandbox.DriverKind(v), nil
	}
	if v == "" || sandbox.DriverKind(v) == sandbox.DriverFake {
		return "", ErrProductionNeedsSandboxDriver
	}
	return sandbox.DriverKind(v), nil
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
// tentá-la e o operador não sabe porquê. (Desde 2026-09-26 o manifesto de produção já não oferece o
// `web_post` — decisão do dono, ADR-034 §2.7; o de dev-hardened ainda o oferece, e é aí que esta
// declaração continua a aparecer.)
//
// «A postura anunciada = a postura ligada» é a regra desta casa (AOS-203/AOS-248). Uma tool
// oferecida e não executável é postura, e passa a ser anunciada.
func toolsSemExecutor(specs []modelToolSpec, bindings map[string]sandbox.SandboxBinding) []string {
	var out []string
	for _, s := range specs {
		nome := strings.TrimSpace(s.Name)
		if nome == "" || s.Modelo != nil {
			// AOS-530: uma tool com o bloco `modelo` tem executor — a chamada ao gateway do nó,
			// registada no RM pela raiz de composição (ocr_tool.go).
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

// reescritaDoEfeito é a reescrita do efeito que o nó compõe: a dos HANDLES (AOS-550), a da sandbox,
// a das tools de modelo e — com o armazém de artefactos ligado (AOS-548) — a da `artifact_write`,
// que põe na entrada o run, o passo e o titular da tool call REAL. Sem ela a tool recebia os
// argumentos do modelo, e um modelo podia escrever no stream de outro run em nome de outro titular
// (achado 1 da revisão do AOS-548).
//
// A ORDEM DA CADEIA NÃO É ARBITRÁRIA (AOS-550). A reescrita dos handles corre PRIMEIRO, sobre os
// argumentos TAL COMO O MODELO OS EMITIU: é esse o documento em que ela procura referências (só nos
// escalares de topo) e é o MESMO documento de que o [resolveResourceValue] tira o `resource_value`
// auditado. As reescritas por tool vêm depois e reformam o documento num envelope de execução
// (`ExecRequest`, `entradaDoArtefacto`) — procurar referências nesse envelope seria procurá-las
// numa estrutura que o modelo não escreveu.
func reescritaDoEfeito(bindings map[string]sandbox.SandboxBinding, ocrRw, videoRw func(referencemonitor.Call) (referencemonitor.Call, error), artefactos bool, handles *planoDeHandles) func(referencemonitor.Call) (referencemonitor.Call, error) {
	var artRw reescritaDaCall
	if artefactos {
		artRw = newArtifactRewriter()
	}
	// A `artifact_write` sai da cadeia das tools de modelo e passa a ser um elo PRÓPRIO, porque é a
	// única que lê o bilhete (a linhagem). O routing do [comporReescritas] continua a mandar-lhe a
	// call, e as reescritas das tools de modelo deixam-na inalterada — cada uma só toca na sua.
	return emCadeia(
		reescritaDeHandles(handles),
		semBilhete(comporReescritas(newSandboxEffectRewriter(bindings), juntarReescritasDeModelo(ocrRw, videoRw))),
		artRw,
	)
}

// bilheteDaCall é o estado de UMA construção de Call, partilhado pelas reescritas da cadeia e por
// mais ninguém: nasce quando a cadeia começa e morre quando ela acaba.
//
// # PORQUE EXISTE (AOS-552)
//
// A linhagem de um artefacto — de que handles ele se alimentou — só é sabida pela reescrita dos
// HANDLES, que resolve as referências; e só é selável por quem produz o efeito, a reescrita da
// `artifact_write`. As duas correm na MESMA cadeia, sobre a MESMA Call e na MESMA goroutine, pelo
// que a informação passa aqui, por valor, de uma para a outra.
//
// # O QUE ISTO SUBSTITUIU, E PORQUE O ANTERIOR ESTAVA ERRADO
//
// A primeira versão deste ticket passava a linhagem por um mapa no plano de handles, escrito pela
// reescrita e consumido pelo executor da tool. O comentário que a justificava dizia que não havia
// corrida porque os dois corriam «no mesmo instante»: é FALSO, e a revisão adversarial mediu-o.
// Entre a reescrita e o executor corre a mediação inteira do Reference Monitor (política, selo no
// WORM), a selagem do handle e a gravação do artefacto — dezenas de ms, com um mutex do nó pelo
// meio. Com o tecto de 64 entradas do mapa e os 512 runs em voo que o nó admite, **100 calls
// concorrentes perdiam 36 linhagens**, e os eventos saíam sem `inputs`, indistinguíveis de uma
// gravação inline e sem contador que o dissesse.
//
// Aqui não há mapa, chave, tecto nem concorrência — e a linhagem passa a fazer parte do efeito que
// o RM medeia, aprova e sela, que é o que o ADR-043 §2.3 pede.
type bilheteDaCall struct {
	// digestPorArgumento é, por nome de argumento DO MODELO, o digest do handle que a reescrita dos
	// handles resolveu nele. Quem sela a linhagem escolhe os argumentos que lhe interessam — os que
	// TRANSPORTAM CONTEÚDO —, e não todos: uma referência resolvida num argumento que só nomeia o
	// tipo de media não é uma derivação do conteúdo.
	digestPorArgumento map[string]string
}

// digestDoArgumento devolve o digest do handle resolvido no argumento dado, ou vazio. nil-safe.
func (b *bilheteDaCall) digestDoArgumento(nome string) string {
	if b == nil {
		return ""
	}
	return b.digestPorArgumento[nome]
}

// anotarDigest registra o digest do handle resolvido num argumento.
func (b *bilheteDaCall) anotarDigest(nome, digest string) {
	if b == nil || nome == "" || digest == "" {
		return
	}
	if b.digestPorArgumento == nil {
		b.digestPorArgumento = map[string]string{}
	}
	b.digestPorArgumento[nome] = digest
}

// reescritaDaCall é uma reescrita do efeito do nó que participa no [bilheteDaCall].
type reescritaDaCall func(referencemonitor.Call, *bilheteDaCall) (referencemonitor.Call, error)

// semBilhete adapta uma reescrita que não precisa do bilhete (a da sandbox, as das tools de
// modelo). nil ⇒ nil.
func semBilhete(rw func(referencemonitor.Call) (referencemonitor.Call, error)) reescritaDaCall {
	if rw == nil {
		return nil
	}
	return func(call referencemonitor.Call, _ *bilheteDaCall) (referencemonitor.Call, error) {
		return rw(call)
	}
}

// emCadeia aplica as reescritas pela ordem dada, parando na primeira que recuse, com UM bilhete por
// construção de Call. Uma reescrita nil é saltada; todas nil ⇒ nil (nenhuma reescrita,
// comportamento byte-idêntico).
func emCadeia(rws ...reescritaDaCall) func(referencemonitor.Call) (referencemonitor.Call, error) {
	var vivas []reescritaDaCall
	for _, rw := range rws {
		if rw != nil {
			vivas = append(vivas, rw)
		}
	}
	if len(vivas) == 0 {
		return nil
	}
	return func(call referencemonitor.Call) (referencemonitor.Call, error) {
		bilhete := &bilheteDaCall{}
		for _, rw := range vivas {
			out, err := rw(call, bilhete)
			if err != nil {
				return referencemonitor.Call{}, err
			}
			call = out
		}
		return call, nil
	}
}
